"""A durable, append-only file destination for invocation evidence records.

``FileEvidenceSink`` implements the ``EvidenceSink`` protocol (``evidence_sink.py``).
It is OFF by default: nothing here runs unless ``ZTAX_EVIDENCE_FILE_PATH`` is set, and
then ``default_evidence_sink()`` keeps the structured-log sink as the primary
destination and adds the file as a second one.

What the file holds
-------------------
One JSON object per line, UTF-8, ``\\n`` terminated, in the same shape and key order as
the line the logging sink emits (``record.as_dict()``, sorted keys). The sink does not
pick fields out of the record, so a field added to the record later (a tenant id, for
example) is written without a change here, and ``read_evidence_file`` returns whatever
keys a line carries.

Failure rules
-------------
* A write never raises and never waits on anything but the disk. On any failure the
  sink counts it (``write_failures``) and logs one fixed line. The log line, the counter
  and every error text are fixed: no record content, path or exception text is in them.
  The Gateway's behaviour for the logging sink (fail closed) is unchanged, because the
  logging sink stays primary.
* A record whose line is longer than ``max_record_bytes`` is dropped and counted
  (``records_dropped_oversize``).
* A failed or short write is rolled back (the file is truncated to its size before the
  write), so a failure does not leave a torn line in the middle of the file. After a
  failure the descriptor is closed and reopened by the next write, so a full disk that
  is later freed recovers on its own.
* At open, bytes after the last newline (the tail of a write cut off by a crash) are
  removed. They can never be a complete record, because every write is one line ending
  in a newline.

Concurrency
-----------
A lock makes writes from the threads of one process safe. The file is opened with
``O_APPEND``; several processes appending small lines are not coordinated here.
"""

from __future__ import annotations

import contextlib
import json
import logging
import os
import threading
from collections.abc import Mapping
from pathlib import Path
from typing import Any, Final

from .evidence_sink import EvidenceSink, LoggingEvidenceSink
from .invocation_evidence import InvocationEvidenceRecord

__all__: list[str] = [
    "DEFAULT_MAX_RECORD_BYTES",
    "ENV_BASE_DIR",
    "ENV_PATH",
    "EvidenceFileConfigError",
    "EvidenceFileError",
    "FanOutEvidenceSink",
    "FileEvidenceSink",
    "default_evidence_sink",
    "read_evidence_file",
]

_log = logging.getLogger("ztax_gateway.evidence")

ENV_PATH: Final = "ZTAX_EVIDENCE_FILE_PATH"
# Optional. When the file's folder does not exist it is created only if it lies inside this one.
ENV_BASE_DIR: Final = "ZTAX_EVIDENCE_FILE_BASE"

# Longest line, newline included, that is written. A record is a few KiB; this leaves room.
DEFAULT_MAX_RECORD_BYTES: Final = 65_536

# A line is never reported or logged more than once per this many repeats of the same failure.
_LOG_EVERY: Final = 1000

# ---------------------------------------------------------------------------
# Fixed texts. None of them carries a path, a record or an exception message.
# ---------------------------------------------------------------------------

_ERR_PATH_TYPE: Final = "evidence file sink: the path must be a text path"
_ERR_PATH_EMPTY: Final = "evidence file sink: the path is empty"
_ERR_PATH_NUL: Final = "evidence file sink: the path contains a null byte"
_ERR_PATH_DIR: Final = "evidence file sink: the path is a directory, not a file"
_ERR_PARENT_NOT_DIR: Final = "evidence file sink: the path's parent is not a directory"
_ERR_PARENT_MISSING: Final = (
    "evidence file sink: the path's folder does not exist and no base folder was given"
)
_ERR_PARENT_OUTSIDE: Final = "evidence file sink: the path's folder is outside the base folder"
_ERR_PARENT_CREATE: Final = "evidence file sink: the path's folder could not be created"
_ERR_CAP: Final = "evidence file sink: max_record_bytes must be a positive whole number"
_ERR_OPEN: Final = "evidence file sink: the file could not be opened for appending"
_ERR_REPAIR: Final = "evidence file sink: the file's tail could not be checked"
_ERR_CORRUPT: Final = "evidence file: a line in the middle of the file is not a JSON object"
_LOG_WRITE_FAILED: Final = (
    "evidence_file_sink: a record could not be written to the file; "
    "AUDIT GAP in the file copy (see write_failures)"
)
_LOG_OVERSIZE: Final = (
    "evidence_file_sink: a record over the size cap was not written to the file "
    "(see records_dropped_oversize)"
)
_LOG_REPAIRED: Final = "evidence_file_sink: removed an unfinished last line from the file"
_LOG_EXTRA_FAILED: Final = "evidence_sink: an additional evidence destination raised; ignored"


class EvidenceFileError(Exception):
    """A problem with an evidence file. The text is fixed and carries no content."""


class EvidenceFileConfigError(EvidenceFileError, ValueError):
    """The path or settings given to the file sink are not acceptable."""


# ---------------------------------------------------------------------------
# Thin wrappers over the two system calls, so a test can simulate a full disk.
# ---------------------------------------------------------------------------


def _write_fd(fd: int, data: memoryview) -> int:
    return os.write(fd, data)


def _fsync_fd(fd: int) -> None:
    os.fsync(fd)


# ---------------------------------------------------------------------------
# Path checks
# ---------------------------------------------------------------------------


def _checked_path(path: str | os.PathLike[str], base_dir: str | os.PathLike[str] | None) -> Path:
    """Return the absolute file path, or raise EvidenceFileConfigError.

    Refuses an empty path, a path with a null byte, a directory, and a folder that does not
    exist unless it lies inside *base_dir* (in which case it is created).
    """
    try:
        raw = os.fspath(path)
    except TypeError:
        raise EvidenceFileConfigError(_ERR_PATH_TYPE) from None
    if not isinstance(raw, str):
        raise EvidenceFileConfigError(_ERR_PATH_TYPE)
    if "\x00" in raw:
        raise EvidenceFileConfigError(_ERR_PATH_NUL)
    if not raw.strip():
        raise EvidenceFileConfigError(_ERR_PATH_EMPTY)
    if raw.endswith(("/", "\\")):
        raise EvidenceFileConfigError(_ERR_PATH_DIR)
    candidate = Path(raw)
    if candidate.name in ("", ".", ".."):
        raise EvidenceFileConfigError(_ERR_PATH_DIR)
    try:
        if candidate.is_dir():
            raise EvidenceFileConfigError(_ERR_PATH_DIR)
        parent = candidate.parent.resolve(strict=False)
        if parent.exists():
            if not parent.is_dir():
                raise EvidenceFileConfigError(_ERR_PARENT_NOT_DIR)
            return parent / candidate.name
        if base_dir is None:
            raise EvidenceFileConfigError(_ERR_PARENT_MISSING)
        base_raw = os.fspath(base_dir)
        if "\x00" in base_raw or not base_raw.strip():
            raise EvidenceFileConfigError(_ERR_PARENT_OUTSIDE)
        base = Path(base_raw).resolve(strict=False)
        if not parent.is_relative_to(base):
            raise EvidenceFileConfigError(_ERR_PARENT_OUTSIDE)
        parent.mkdir(parents=True, exist_ok=True)
    except EvidenceFileConfigError:
        raise
    except (OSError, ValueError):
        raise EvidenceFileConfigError(_ERR_PARENT_CREATE) from None
    return parent / candidate.name


# ---------------------------------------------------------------------------
# FileEvidenceSink
# ---------------------------------------------------------------------------


class FileEvidenceSink:
    """Appends each record as one JSON line to a file. Never raises from ``write``.

    Parameters
    ----------
    path:
        The evidence file. Created (mode 0600 where the platform has modes) if missing.
    base_dir:
        When the file's folder does not exist, it is created only if it lies inside this
        folder; otherwise construction is refused. ``None`` means "never create a folder".
    max_record_bytes:
        A line longer than this (newline included) is dropped and counted.
    fsync:
        Flush to stable storage after every record. ``True`` is the durable setting; a
        slow disk then slows each call by the cost of one ``fsync``.
    """

    def __init__(
        self,
        path: str | os.PathLike[str],
        *,
        base_dir: str | os.PathLike[str] | None = None,
        max_record_bytes: int = DEFAULT_MAX_RECORD_BYTES,
        fsync: bool = True,
    ) -> None:
        if type(max_record_bytes) is not int or max_record_bytes <= 0:
            raise EvidenceFileConfigError(_ERR_CAP)
        self._path = _checked_path(path, base_dir)
        self._max_bytes = max_record_bytes
        self._fsync = fsync
        self._lock = threading.Lock()
        self._fd: int | None = None
        self._written = 0
        self._failures = 0
        self._oversize = 0
        self._repaired_bytes = 0
        with self._lock:
            self._open_locked()

    # -- counters ----------------------------------------------------------

    @property
    def path(self) -> Path:
        return self._path

    @property
    def records_written(self) -> int:
        with self._lock:
            return self._written

    @property
    def write_failures(self) -> int:
        with self._lock:
            return self._failures

    @property
    def records_dropped_oversize(self) -> int:
        with self._lock:
            return self._oversize

    @property
    def repaired_tail_bytes(self) -> int:
        """Bytes of an unfinished last line removed when the file was opened."""
        with self._lock:
            return self._repaired_bytes

    # -- the EvidenceSink interface ------------------------------------------

    def write(self, record: InvocationEvidenceRecord) -> None:
        """Append *record*. Never raises: a failure is counted and logged as fixed text."""
        try:
            line = json.dumps(record.as_dict(), sort_keys=True, default=str).encode("utf-8")
            line += b"\n"
        except Exception:
            self._note_failure()
            return
        if len(line) > self._max_bytes:
            self._note_oversize()
            return
        with self._lock:
            try:
                self._append_locked(line)
            except Exception:
                self._failures += 1
                if self._failures == 1 or self._failures % _LOG_EVERY == 0:
                    _log.error(_LOG_WRITE_FAILED, extra={"write_failures": self._failures})
            else:
                self._written += 1

    def close(self) -> None:
        """Close the file. A later write reopens it."""
        with self._lock:
            self._close_locked()

    # -- internals -----------------------------------------------------------

    def _note_failure(self) -> None:
        with self._lock:
            self._failures += 1
            if self._failures == 1 or self._failures % _LOG_EVERY == 0:
                _log.error(_LOG_WRITE_FAILED, extra={"write_failures": self._failures})

    def _note_oversize(self) -> None:
        with self._lock:
            self._oversize += 1
            if self._oversize == 1 or self._oversize % _LOG_EVERY == 0:
                _log.error(_LOG_OVERSIZE, extra={"records_dropped_oversize": self._oversize})

    def _close_locked(self) -> None:
        fd, self._fd = self._fd, None
        if fd is not None:
            with contextlib.suppress(OSError):
                os.close(fd)

    def _open_locked(self) -> None:
        flags = (
            os.O_WRONLY
            | os.O_APPEND
            | os.O_CREAT
            | getattr(os, "O_CLOEXEC", 0)
            | getattr(os, "O_BINARY", 0)
        )
        try:
            fd = os.open(self._path, flags, 0o600)
        except OSError:
            raise EvidenceFileConfigError(_ERR_OPEN) from None
        try:
            self._repair_tail(fd)
        except BaseException:
            os.close(fd)
            raise
        self._fd = fd

    def _repair_tail(self, fd: int) -> None:
        """Remove bytes after the last newline: the cut-off end of a crashed write."""
        try:
            size = os.fstat(fd).st_size
            if size == 0:
                return
            window = min(size, self._max_bytes + 1)
            with open(self._path, "rb") as reader:
                reader.seek(size - window)
                tail = reader.read(window)
            if tail.endswith(b"\n"):
                return
            cut = tail.rfind(b"\n")
            if cut < 0 and window < size:
                # A last "line" longer than any record this sink writes: not ours to repair.
                raise EvidenceFileConfigError(_ERR_REPAIR)
            keep = (size - window) + cut + 1 if cut >= 0 else 0
            os.ftruncate(fd, keep)
            self._repaired_bytes = size - keep
        except EvidenceFileConfigError:
            raise
        except OSError:
            raise EvidenceFileConfigError(_ERR_REPAIR) from None
        _log.warning(_LOG_REPAIRED, extra={"bytes_removed": self._repaired_bytes})

    def _append_locked(self, line: bytes) -> None:
        """Write one whole line, or leave the file as it was and raise."""
        if self._fd is None:
            self._open_locked()
        fd = self._fd
        assert fd is not None
        before = os.fstat(fd).st_size
        try:
            view = memoryview(line)
            sent = 0
            while sent < len(line):
                count = _write_fd(fd, view[sent:])
                if count <= 0:
                    raise OSError
                sent += count
            if self._fsync:
                _fsync_fd(fd)
        except BaseException:
            # Roll back a short write so no torn line stays in the middle of the file,
            # then drop the descriptor so the next write starts from a clean open.
            with contextlib.suppress(OSError):
                os.ftruncate(fd, before)
            self._close_locked()
            raise


# ---------------------------------------------------------------------------
# More than one destination
# ---------------------------------------------------------------------------


class FanOutEvidenceSink:
    """Writes to a primary sink, then to extra sinks whose failures are ignored.

    The primary's exception propagates, exactly as when it was the only sink, so the
    Gateway's fail-closed behaviour is unchanged. An extra sink raising is logged as
    fixed text and does not affect the call.
    """

    def __init__(self, primary: EvidenceSink, *extras: EvidenceSink) -> None:
        self._primary = primary
        self._extras = extras

    def write(self, record: InvocationEvidenceRecord) -> None:
        self._primary.write(record)
        for sink in self._extras:
            try:
                sink.write(record)
            except Exception:
                _log.error(_LOG_EXTRA_FAILED)


def default_evidence_sink(environ: Mapping[str, str] | None = None) -> EvidenceSink:
    """The sink a Gateway uses when none is injected.

    With ``ZTAX_EVIDENCE_FILE_PATH`` unset or blank this is the plain logging sink: the
    same behaviour as before this module existed. With a path set, it is the logging sink
    plus a ``FileEvidenceSink`` (``ZTAX_EVIDENCE_FILE_BASE`` optionally names the folder
    inside which a missing folder may be created). A path that is set and not acceptable
    raises ``EvidenceFileConfigError`` (fixed text) at start-up rather than silently
    writing nothing.
    """
    env: Mapping[str, str] = os.environ if environ is None else environ
    raw = env.get(ENV_PATH, "").strip()
    if not raw:
        return LoggingEvidenceSink()
    base = env.get(ENV_BASE_DIR, "").strip() or None
    return FanOutEvidenceSink(LoggingEvidenceSink(), FileEvidenceSink(raw, base_dir=base))


# ---------------------------------------------------------------------------
# Reader
# ---------------------------------------------------------------------------


def read_evidence_file(path: str | os.PathLike[str]) -> list[dict[str, Any]]:
    """Return the records in an evidence file, oldest first.

    A last line with no newline (a write that was cut off) is skipped. A line elsewhere
    that is not a JSON object raises ``EvidenceFileError`` naming only its line number:
    the sink never writes one, so it means the file was altered or damaged. A missing file
    raises the ordinary ``OSError``.
    """
    data = Path(path).read_bytes()
    lines = data.split(b"\n")
    lines.pop()  # after a final newline this is empty; otherwise it is the torn last line
    records: list[dict[str, Any]] = []
    for number, line in enumerate(lines, start=1):
        try:
            value = json.loads(line)
        except (ValueError, RecursionError):
            value = None
        if not isinstance(value, dict):
            raise EvidenceFileError(f"{_ERR_CORRUPT} (line {number})")
        records.append(value)
    return records
