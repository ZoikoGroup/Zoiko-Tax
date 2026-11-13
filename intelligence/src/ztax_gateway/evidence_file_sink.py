"""A durable, append-only file destination for invocation evidence records.

``FileEvidenceSink`` implements the ``EvidenceSink`` protocol (``evidence_sink.py``).
It is OFF by default: nothing here runs unless ``ZTAX_EVIDENCE_FILE_PATH`` is set and the
Gateway is built through ``gateway_factory.build_gateway``, which then keeps the
structured-log sink as the primary destination and adds the file as a second one. A plain
``Gateway(...)`` never touches the file, whatever the environment says.

What the file holds
-------------------
One JSON object per line, UTF-8, ``\\n`` terminated, in the same shape and key order as
the line the logging sink emits (``record.as_dict()``, sorted keys). The sink does not
pick fields out of the record, so a field added to the record later (a tenant id, for
example) is written without a change here, and ``read_evidence_file`` returns whatever
keys a line carries.

One writer per file
-------------------
The sink takes an exclusive, non-blocking lock on the file when it opens it
(``fcntl.flock`` on POSIX; ``msvcrt.locking`` on a byte far beyond the end of the file on
Windows, so readers of the real bytes are unaffected). A second sink on the same file, in
this process or another, refuses to open with ``EvidenceFileConfigError``. The lock is
held until ``close()``, until the sink is garbage collected, or until the process exits.
Because there is one writer, truncating the file back after a failed write is safe.

Failure rules
-------------
* A write never raises. On any failure the sink counts it (``write_failures``) and logs
  one fixed line each time a failure follows a success (a new outage), and every 1000th
  failure after that. The log line, the counter and every error text are fixed: no record
  content, path or exception text is in them. The Gateway's behaviour for the logging sink
  (fail closed) is unchanged, because the logging sink stays primary.
* ``write`` waits at most ``_LOCK_WAIT_SECONDS`` for its turn. A record that cannot get
  its turn in time is dropped and counted as a failure, so a stalled disk delays other
  callers by two seconds at most.
* A record whose line is longer than ``max_record_bytes`` is dropped and counted
  (``records_dropped_oversize``).
* A failed or short write is rolled back (the file is truncated to its size before the
  write), so a failure does not leave a torn line in the middle of the file. If that
  truncation itself fails, the next write repairs the tail first.
* At open, the last line is checked. Bytes after the last newline that start with ``{`` but
  are not a whole JSON object (the cut-off end of a crashed write) are removed. A whole
  JSON object with no newline is kept and the newline is added. Anything else (plain text,
  for example) is not ours: the file is left unchanged and opening is refused.
"""

from __future__ import annotations

import contextlib
import json
import logging
import os
import stat
import sys
import threading
import weakref
from collections.abc import Mapping
from pathlib import Path
from typing import Any, Final

from .evidence_sink import EvidenceSink, LoggingEvidenceSink
from .invocation_evidence import InvocationEvidenceRecord

if sys.platform == "win32":
    import msvcrt
else:
    import fcntl

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

# How long write() waits for its turn before it drops the record and counts a failure.
_LOCK_WAIT_SECONDS = 2.0

# After a recovery, the first failure is logged; during an outage every this-many-th again.
_LOG_EVERY: Final = 1000

# A line longer than this is not a record this sink wrote; the reader refuses it.
_MAX_READ_LINE: Final = 16 * 1024 * 1024

# Windows: the byte the lock is taken on, far past any real end of file.
_WIN_LOCK_OFFSET: Final = 2**40

# ---------------------------------------------------------------------------
# Fixed texts. None of them carries a path, a record or an exception message.
# ---------------------------------------------------------------------------

_ERR_PATH_TYPE: Final = "evidence file sink: the path must be a text path"
_ERR_PATH_EMPTY: Final = "evidence file sink: the path is empty"
_ERR_PATH_NUL: Final = "evidence file sink: the path contains a null byte"
_ERR_PATH_TRAILING_SEP: Final = "evidence file sink: the path ends with a path separator"
_ERR_PATH_NO_NAME: Final = "evidence file sink: the path does not end in a file name"
_ERR_PATH_COLON: Final = "evidence file sink: the path has a colon outside a drive prefix"
_ERR_PATH_NAME_END: Final = "evidence file sink: the file name ends with a dot or a space"
_ERR_PATH_DIR: Final = "evidence file sink: the path is a directory, not a file"
_ERR_NOT_REGULAR: Final = "evidence file sink: the path is not a regular file"
_ERR_PARENT_NOT_DIR: Final = "evidence file sink: the path's parent is not a directory"
_ERR_PARENT_MISSING: Final = (
    "evidence file sink: the path's folder does not exist and no base folder was given"
)
_ERR_PARENT_OUTSIDE: Final = "evidence file sink: the path's folder is outside the base folder"
_ERR_PARENT_CREATE: Final = "evidence file sink: the path's folder could not be created"
_ERR_BASE_TYPE: Final = "evidence file sink: the base folder must be a text path"
_ERR_CAP: Final = "evidence file sink: max_record_bytes must be a positive whole number"
_ERR_OPEN: Final = "evidence file sink: the file could not be opened for appending"
_ERR_LOCKED: Final = "evidence file sink: the file is in use by another evidence sink"
_ERR_REPAIR: Final = "evidence file sink: the file's tail could not be checked"
_ERR_FOREIGN: Final = (
    "evidence file sink: the file does not end like an evidence file; it was left unchanged"
)
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
# Thin wrappers over the system calls, so a test can simulate a full disk.
# ---------------------------------------------------------------------------


def _write_fd(fd: int, data: memoryview) -> int:
    return os.write(fd, data)


def _fsync_fd(fd: int) -> None:
    os.fsync(fd)


def _lock_fd(fd: int) -> bool:
    """Take an exclusive, non-blocking lock on the open file. False if someone holds it."""
    try:
        if sys.platform == "win32":
            os.lseek(fd, _WIN_LOCK_OFFSET, os.SEEK_SET)
            try:
                msvcrt.locking(fd, msvcrt.LK_NBLCK, 1)
            finally:
                os.lseek(fd, 0, os.SEEK_SET)
        else:
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except OSError:
        return False
    return True


# ---------------------------------------------------------------------------
# Path checks
# ---------------------------------------------------------------------------


def _checked_path(path: str | os.PathLike[str], base_dir: str | os.PathLike[str] | None) -> Path:
    """Return the absolute file path, or raise EvidenceFileConfigError.

    Refuses an empty path, a null byte, a trailing separator, a path with no file name, a
    colon outside a drive prefix, a name ending in a dot or space, a directory, and a
    folder that does not exist unless it lies inside *base_dir* (then it is created).
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
        raise EvidenceFileConfigError(_ERR_PATH_TRAILING_SEP)
    candidate = Path(raw)
    if candidate.name in ("", ".", ".."):
        raise EvidenceFileConfigError(_ERR_PATH_NO_NAME)
    if ":" in str(candidate)[len(candidate.drive) :]:
        raise EvidenceFileConfigError(_ERR_PATH_COLON)
    if candidate.name.endswith((".", " ")):
        raise EvidenceFileConfigError(_ERR_PATH_NAME_END)
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
        try:
            base_raw = os.fspath(base_dir)
        except TypeError:
            raise EvidenceFileConfigError(_ERR_BASE_TYPE) from None
        if not isinstance(base_raw, str):
            raise EvidenceFileConfigError(_ERR_BASE_TYPE)
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


def _is_json_object(data: bytes) -> bool:
    try:
        return isinstance(json.loads(data), dict)
    except (ValueError, RecursionError):
        return False


# ---------------------------------------------------------------------------
# FileEvidenceSink
# ---------------------------------------------------------------------------


class _Handle:
    """Holds the descriptor so a finalizer can close it without referring to the sink."""

    __slots__ = ("fd",)

    def __init__(self) -> None:
        self.fd: int | None = None


def _close_handle(handle: _Handle) -> None:
    fd, handle.fd = handle.fd, None
    if fd is not None:
        with contextlib.suppress(OSError):
            os.close(fd)  # closing the descriptor also releases the lock


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
        self._io = threading.Lock()  # one writer at a time; guards the descriptor and the file
        self._stats = threading.Lock()  # guards the counters; never held while waiting for _io
        self._handle = _Handle()
        self._finalizer = weakref.finalize(self, _close_handle, self._handle)
        self._dirty = False  # a failed write could not be rolled back; repair before the next
        self._last_ok = True
        self._written = 0
        self._failures = 0
        self._oversize = 0
        self._repaired_bytes = 0
        with self._io:
            self._open_locked()

    # -- counters ----------------------------------------------------------

    @property
    def path(self) -> Path:
        return self._path

    @property
    def records_written(self) -> int:
        with self._stats:
            return self._written

    @property
    def write_failures(self) -> int:
        with self._stats:
            return self._failures

    @property
    def records_dropped_oversize(self) -> int:
        with self._stats:
            return self._oversize

    @property
    def repaired_tail_bytes(self) -> int:
        """Bytes of an unfinished last line removed from the file by this sink."""
        with self._stats:
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
        if not self._io.acquire(timeout=_LOCK_WAIT_SECONDS):
            self._note_failure()  # could not get its turn in time: drop, count, return
            return
        try:
            try:
                self._append_locked(line)
            except Exception:
                self._note_failure()
            else:
                self._note_success()
        finally:
            self._io.release()

    def close(self) -> None:
        """Close the file and release its lock. A later write reopens (and relocks) it."""
        with self._io:
            _close_handle(self._handle)

    # -- internals -----------------------------------------------------------

    def _note_success(self) -> None:
        with self._stats:
            self._written += 1
            self._last_ok = True

    def _note_failure(self) -> None:
        with self._stats:
            self._failures += 1
            if self._last_ok or self._failures % _LOG_EVERY == 0:
                _log.error(_LOG_WRITE_FAILED, extra={"write_failures": self._failures})
            self._last_ok = False

    def _note_oversize(self) -> None:
        with self._stats:
            self._oversize += 1
            if self._oversize == 1 or self._oversize % _LOG_EVERY == 0:
                _log.error(_LOG_OVERSIZE, extra={"records_dropped_oversize": self._oversize})

    def _open_locked(self) -> None:
        # A FIFO or device is refused before it is opened (opening a FIFO can block).
        try:
            existing = os.stat(self._path)
        except FileNotFoundError:
            existing = None
        except OSError:
            raise EvidenceFileConfigError(_ERR_OPEN) from None
        if existing is not None and not stat.S_ISREG(existing.st_mode):
            raise EvidenceFileConfigError(_ERR_NOT_REGULAR)
        flags = (
            os.O_WRONLY
            | os.O_APPEND
            | os.O_CREAT
            | getattr(os, "O_CLOEXEC", 0)
            | getattr(os, "O_BINARY", 0)
            | getattr(os, "O_NOFOLLOW", 0)
        )
        try:
            fd = os.open(self._path, flags, 0o600)
        except OSError:
            raise EvidenceFileConfigError(_ERR_OPEN) from None
        try:
            self._claim(fd)
        except BaseException:
            os.close(fd)
            raise
        self._handle.fd = fd
        self._dirty = False

    def _claim(self, fd: int) -> None:
        """Check the opened file is regular, lock it, then check its last line."""
        try:
            regular = stat.S_ISREG(os.fstat(fd).st_mode)
        except OSError:
            raise EvidenceFileConfigError(_ERR_OPEN) from None
        if not regular:
            raise EvidenceFileConfigError(_ERR_NOT_REGULAR)
        if not _lock_fd(fd):
            raise EvidenceFileConfigError(_ERR_LOCKED)
        self._repair_tail(fd)

    def _repair_tail(self, fd: int) -> None:
        """Make the file end in a newline, without ever removing a whole record.

        * ends in a newline (or is empty): nothing to do.
        * the bytes after the last newline are one whole JSON object: add the newline.
        * they start with ``{`` and are not a whole object (a cut-off write): remove them.
        * anything else is not ours: refuse, and leave the file unchanged.
        """
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
                raise EvidenceFileConfigError(_ERR_FOREIGN)  # a last "line" too long to be ours
            fragment = tail[cut + 1 :]
            if _is_json_object(fragment):
                os.write(fd, b"\n")
                return
            if not fragment.startswith(b"{"):
                raise EvidenceFileConfigError(_ERR_FOREIGN)
            os.ftruncate(fd, size - len(fragment))
        except EvidenceFileConfigError:
            raise
        except OSError:
            raise EvidenceFileConfigError(_ERR_REPAIR) from None
        with self._stats:
            self._repaired_bytes += len(fragment)
        _log.warning(_LOG_REPAIRED, extra={"bytes_removed": len(fragment)})

    def _append_locked(self, line: bytes) -> None:
        """Write one whole line, or leave the file as it was and raise."""
        if self._handle.fd is None:
            self._open_locked()
        fd = self._handle.fd
        assert fd is not None
        if self._dirty:
            self._repair_tail(fd)
            self._dirty = False
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
            # Roll back a short write so no torn line stays in the file. The descriptor and
            # its lock stay: a second sink must not be able to slip in after a failure.
            try:
                os.ftruncate(fd, before)
            except OSError:
                self._dirty = True
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
    """The sink ``build_gateway`` gives the Gateway.

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
    """Return the records in an evidence file, oldest first, reading it line by line.

    Empty lines are skipped, and a BOM or CRLF line ending is tolerated. A last line with
    no newline is kept if it is a whole JSON object and skipped if it is cut off. Any other
    line that is not a JSON object raises ``EvidenceFileError`` naming only its line
    number: the sink never writes one, so it means the file was altered or damaged. A
    missing file raises the ordinary ``OSError``.
    """
    records: list[dict[str, Any]] = []
    with open(path, "rb") as fh:
        number = 0
        while True:
            line = fh.readline(_MAX_READ_LINE + 1)
            if not line:
                break
            number += 1
            if len(line) > _MAX_READ_LINE:
                raise EvidenceFileError(f"{_ERR_CORRUPT} (line {number})")
            body = line.strip()
            if not body:
                continue
            try:
                value = json.loads(body)
            except (ValueError, RecursionError):
                value = None
            if isinstance(value, dict):
                records.append(value)
            elif line.endswith(b"\n"):
                raise EvidenceFileError(f"{_ERR_CORRUPT} (line {number})")
            # else: the last line was cut off mid-write; skip it
    return records
