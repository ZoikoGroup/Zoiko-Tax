"""Tests for the durable evidence file sink (evidence_file_sink.py).

Synthetic data only. Every file lives under pytest's tmp_path. A full disk and a failing
write are simulated by replacing the module's thin ``_write_fd`` and ``_fsync_fd`` wrappers;
nothing touches a real disk limit. Sentinel strings stand in for record content.
"""

from __future__ import annotations

import base64
import errno
import gc
import json
import logging
import os
import stat
import subprocess
import sys
import threading
import time
from pathlib import Path
from typing import Any

import pytest

from ztax_gateway import evidence_file_sink as efs
from ztax_gateway.evidence_file_sink import (
    DEFAULT_MAX_RECORD_BYTES,
    ENV_BASE_DIR,
    ENV_PATH,
    EvidenceFileConfigError,
    EvidenceFileError,
    FanOutEvidenceSink,
    FileEvidenceSink,
    default_evidence_sink,
    read_evidence_file,
)
from ztax_gateway.evidence_sink import InMemoryEvidenceSink, LoggingEvidenceSink
from ztax_gateway.fake_runtime import FakeRuntime
from ztax_gateway.gateway_factory import ENV_PROMPT_PROFILES, GatewayFactoryError, build_gateway
from ztax_gateway.governance import UseCase, UseCaseRegistry
from ztax_gateway.invocation_evidence import InvocationEvidenceRecord
from ztax_gateway.provenance import AuthorityOutcome, RiskTier
from ztax_gateway.runtime import Result, Route, RoutingTable
from ztax_gateway.server import Gateway, GatewayError
from ztax_gateway.wire import Call

SENTINEL = "SENT-EVIDENCE-FILE-4b7d19"
_REGION = "eu-west-1"
_UC = "classification-review"
_TENANT = "0190f3a2-1b2c-7d3e-8f40-5a6b7c8d9e0f"
_ROUTE = Route("model:fake@1", "provider:fake", "prompt:classification-review-v1")
_CONFIG_DIR = Path(__file__).resolve().parent.parent / "config"


class _Rec:
    """A stand-in with the one method the sink uses."""

    def __init__(self, n: int, filler: str = "") -> None:
        self.n = n
        self.filler = filler

    def as_dict(self) -> dict[str, Any]:
        return {"ai_invocation_id": f"id-{self.n:06d}", "n": self.n, "filler": self.filler}


def _w(sink: FileEvidenceSink, rec: object) -> None:
    sink.write(rec)  # type: ignore[arg-type]


def _ns(path: Path) -> list[int]:
    return [r["n"] for r in read_evidence_file(path)]


def _log_blob(caplog: pytest.LogCaptureFixture) -> str:
    return "\n".join(r.getMessage() + repr(vars(r)) for r in caplog.records)


def _error_lines(caplog: pytest.LogCaptureFixture) -> list[logging.LogRecord]:
    return [r for r in caplog.records if r.levelno >= logging.ERROR]


@pytest.fixture(autouse=True)
def _no_env(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.delenv(ENV_PATH, raising=False)
    monkeypatch.delenv(ENV_BASE_DIR, raising=False)


# ---------------------------------------------------------------------------
# Writing and reading
# ---------------------------------------------------------------------------


def test_one_json_line_per_record_in_sorted_key_order(tmp_path: Path) -> None:
    path = tmp_path / "ev.jsonl"
    sink = FileEvidenceSink(path)
    for n in range(3):
        _w(sink, _Rec(n))
    sink.close()
    raw = path.read_bytes()
    assert raw.endswith(b"\n")
    lines = raw.split(b"\n")[:-1]
    assert len(lines) == 3
    assert lines[1] == json.dumps(_Rec(1).as_dict(), sort_keys=True).encode()
    assert _ns(path) == [0, 1, 2]
    assert sink.records_written == 3
    assert sink.write_failures == 0


def test_restart_keeps_records_and_appends(tmp_path: Path) -> None:
    path = tmp_path / "ev.jsonl"
    first = FileEvidenceSink(path)
    for n in range(5):
        _w(first, _Rec(n))
    first.close()
    second = FileEvidenceSink(path)  # a restart: a new sink on the same file
    for n in range(5, 8):
        _w(second, _Rec(n))
    second.close()
    assert _ns(path) == list(range(8))


def test_each_write_is_flushed_with_fsync_unless_turned_off(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    calls: list[int] = []
    monkeypatch.setattr(efs, "_fsync_fd", lambda fd: calls.append(fd))
    durable = FileEvidenceSink(tmp_path / "a.jsonl")
    _w(durable, _Rec(1))
    _w(durable, _Rec(2))
    assert len(calls) == 2
    fast = FileEvidenceSink(tmp_path / "b.jsonl", fsync=False)
    _w(fast, _Rec(1))
    assert len(calls) == 2
    durable.close()
    fast.close()


@pytest.mark.skipif(sys.platform == "win32", reason="POSIX file modes")
def test_a_new_file_is_private(tmp_path: Path) -> None:
    path = tmp_path / "ev.jsonl"
    FileEvidenceSink(path).close()
    assert (path.stat().st_mode & 0o777) == 0o600


# ---------------------------------------------------------------------------
# The tail of the file: repair at open
# ---------------------------------------------------------------------------


def test_a_torn_last_line_is_skipped_by_the_reader(tmp_path: Path) -> None:
    path = tmp_path / "ev.jsonl"
    path.write_bytes(b'{"n": 0}\n{"n": 1}\n{"ai_invocation_id": "id-3", "n": 3, "fil')
    assert _ns(path) == [0, 1]


def test_reopening_removes_a_torn_tail_so_new_records_start_cleanly(
    tmp_path: Path, caplog: pytest.LogCaptureFixture
) -> None:
    caplog.set_level(logging.DEBUG)
    path = tmp_path / "ev.jsonl"
    torn = b'{"ai_invocation_id": "id-000001", "n": 1, "fil' + SENTINEL.encode()
    path.write_bytes(b'{"n": 0}\n' + torn)
    sink = FileEvidenceSink(path)
    assert sink.repaired_tail_bytes == len(torn)
    assert path.read_bytes() == b'{"n": 0}\n'
    _w(sink, _Rec(2))
    sink.close()
    assert _ns(path) == [0, 2]
    assert SENTINEL not in _log_blob(caplog)


def test_a_file_with_only_a_torn_line_is_emptied_on_open(tmp_path: Path) -> None:
    path = tmp_path / "ev.jsonl"
    path.write_bytes(b'{"n": 1')
    sink = FileEvidenceSink(path)
    assert path.read_bytes() == b""
    _w(sink, _Rec(7))
    sink.close()
    assert _ns(path) == [7]


def test_a_whole_last_record_with_no_newline_is_kept_and_the_newline_added(
    tmp_path: Path,
) -> None:
    path = tmp_path / "ev.jsonl"
    path.write_bytes(b'{"n": 1}\n{"n": 2}')
    assert _ns(path) == [1, 2]  # the reader keeps it
    sink = FileEvidenceSink(path)
    assert path.read_bytes() == b'{"n": 1}\n{"n": 2}\n'
    assert sink.repaired_tail_bytes == 0
    _w(sink, _Rec(3))
    sink.close()
    assert _ns(path) == [1, 2, 3]


def test_a_file_that_is_not_an_evidence_file_is_refused_and_left_unchanged(
    tmp_path: Path,
) -> None:
    path = tmp_path / "notes.txt"
    original = b"important notes without a newline"
    path.write_bytes(original)
    with pytest.raises(EvidenceFileConfigError, match="does not end like an evidence file"):
        FileEvidenceSink(path)
    assert path.read_bytes() == original
    # The refused open did not leave the file locked.
    other = tmp_path / "other.jsonl"
    FileEvidenceSink(other).close()


def test_a_last_line_too_long_to_be_ours_is_refused_and_left_unchanged(tmp_path: Path) -> None:
    path = tmp_path / "ev.jsonl"
    # The last 101 bytes (the repair window for a cap of 100) start with "{", so only the
    # "too long to be ours" refusal keeps the repair from cutting into this line.
    original = b'{"n": 0}\n' + b"y" * 400 + b"{" + b"z" * 100
    path.write_bytes(original)
    with pytest.raises(EvidenceFileConfigError, match="does not end like an evidence file"):
        FileEvidenceSink(path, max_record_bytes=100)
    assert path.read_bytes() == original


# ---------------------------------------------------------------------------
# The reader
# ---------------------------------------------------------------------------


def test_reader_edge_cases(tmp_path: Path) -> None:
    empty = tmp_path / "empty.jsonl"
    empty.write_bytes(b"")
    assert read_evidence_file(empty) == []
    bad_mid = tmp_path / "bad.jsonl"
    bad_mid.write_bytes(b'{"n": 1}\nnot json\n{"n": 3}\n')
    with pytest.raises(EvidenceFileError, match="line 2"):
        read_evidence_file(bad_mid)
    not_object = tmp_path / "arr.jsonl"
    not_object.write_bytes(b"[1, 2]\n")
    with pytest.raises(EvidenceFileError):
        read_evidence_file(not_object)
    nested = tmp_path / "deep.jsonl"
    nested.write_bytes(b"[" * 100_000 + b"\n")
    with pytest.raises(EvidenceFileError):
        read_evidence_file(nested)
    with pytest.raises(OSError):
        read_evidence_file(tmp_path / "missing.jsonl")


def test_reader_skips_empty_lines_and_accepts_bom_and_crlf(tmp_path: Path) -> None:
    blank = tmp_path / "blank.jsonl"
    blank.write_bytes(b'{"n": 1}\n\n   \n{"n": 2}\n')
    assert _ns(blank) == [1, 2]
    bom = tmp_path / "bom.jsonl"
    bom.write_bytes(b'\xef\xbb\xbf{"n": 1}\n')
    assert _ns(bom) == [1]
    crlf = tmp_path / "crlf.jsonl"
    crlf.write_bytes(b'{"n": 1}\r\n{"n": 2}\r\n')
    assert _ns(crlf) == [1, 2]


def test_reader_streams_the_file_instead_of_reading_it_whole(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    path = tmp_path / "ev.jsonl"
    path.write_bytes(b'{"n": 1}\n{"n": 2}\n')

    def forbidden(self: Path) -> bytes:
        raise AssertionError("the reader must not read the whole file at once")

    monkeypatch.setattr(Path, "read_bytes", forbidden)
    assert _ns(path) == [1, 2]


def test_reader_refuses_a_single_line_longer_than_its_limit(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    path = tmp_path / "ev.jsonl"
    path.write_bytes(b'{"n": "' + b"x" * 300 + b'"}\n')
    monkeypatch.setattr(efs, "_MAX_READ_LINE", 100)
    with pytest.raises(EvidenceFileError, match="line 1"):
        read_evidence_file(path)


# ---------------------------------------------------------------------------
# Bad paths and settings
# ---------------------------------------------------------------------------


def test_an_unwritable_path_is_refused_at_construction(tmp_path: Path) -> None:
    if hasattr(os, "geteuid") and os.geteuid() == 0:
        pytest.skip("root can write to a read-only file")
    path = tmp_path / "readonly.jsonl"
    path.write_text("")
    path.chmod(0o444)
    try:
        with pytest.raises(EvidenceFileConfigError, match="could not be opened"):
            FileEvidenceSink(path)
    finally:
        path.chmod(0o644)


def test_a_path_whose_parent_is_a_file_is_refused(tmp_path: Path) -> None:
    blocker = tmp_path / "blocker"
    blocker.write_text("x")
    with pytest.raises(EvidenceFileConfigError, match="parent is not a directory"):
        FileEvidenceSink(blocker / "ev.jsonl")


@pytest.mark.parametrize(
    ("make_path", "text"),
    [
        (lambda tmp: "", "path is empty"),
        (lambda tmp: "   ", "path is empty"),
        (lambda tmp: str(tmp / "a\x00b.jsonl"), "null byte"),
        (lambda tmp: str(tmp), "is a directory, not a file"),
        (lambda tmp: str(tmp) + os.sep, "ends with a path separator"),
        (lambda tmp: str(tmp / "dir-style") + "/", "ends with a path separator"),
        (lambda tmp: ".", "does not end in a file name"),
        (lambda tmp: "..", "does not end in a file name"),
        (lambda tmp: str(tmp) + os.sep + "a:b.jsonl", "colon outside a drive prefix"),
        (lambda tmp: str(tmp / "ev.jsonl:stream"), "colon outside a drive prefix"),
        (lambda tmp: str(tmp / "ev.jsonl."), "ends with a dot or a space"),
        (lambda tmp: str(tmp / "ev.jsonl "), "ends with a dot or a space"),
        (lambda tmp: str(tmp / "missing-folder" / "ev.jsonl"), "no base folder"),
        (lambda tmp: b"bytes-path", "text path"),
        (lambda tmp: 12345, "text path"),
    ],
    ids=[
        "empty",
        "blank",
        "null-byte",
        "directory",
        "trailing-separator",
        "trailing-slash",
        "dot",
        "dotdot",
        "colon-in-name",
        "alternate-stream",
        "trailing-dot",
        "trailing-space",
        "missing-folder-no-base",
        "bytes",
        "int",
    ],
)
def test_a_bad_path_is_refused_by_the_check_that_names_it(
    tmp_path: Path, make_path: Any, text: str
) -> None:
    bad = make_path(tmp_path)
    with pytest.raises(EvidenceFileConfigError, match=text) as exc:
        FileEvidenceSink(bad)
    assert str(tmp_path) not in str(exc.value)
    assert exc.value.__cause__ is None
    assert not (tmp_path / "missing-folder").exists()


def test_a_missing_folder_is_created_only_inside_the_base(tmp_path: Path) -> None:
    base = tmp_path / "base"
    inside = base / "a" / "b" / "ev.jsonl"
    sink = FileEvidenceSink(inside, base_dir=base)
    _w(sink, _Rec(1))
    sink.close()
    assert _ns(inside) == [1]

    outside = tmp_path / "elsewhere" / "ev.jsonl"
    with pytest.raises(EvidenceFileConfigError, match="outside the base folder"):
        FileEvidenceSink(outside, base_dir=base)
    assert not (tmp_path / "elsewhere").exists()

    escape = base / ".." / "escaped" / "ev.jsonl"
    with pytest.raises(EvidenceFileConfigError, match="outside the base folder"):
        FileEvidenceSink(escape, base_dir=base)
    assert not (tmp_path / "escaped").exists()


def test_a_sibling_folder_with_the_same_prefix_is_not_inside_the_base(tmp_path: Path) -> None:
    base = tmp_path / "base"
    base.mkdir()
    with pytest.raises(EvidenceFileConfigError, match="outside the base folder"):
        FileEvidenceSink(tmp_path / "base-evil" / "ev.jsonl", base_dir=base)
    assert not (tmp_path / "base-evil").exists()


@pytest.mark.parametrize("bad_base", [b"bytes-base", 123, object()])
def test_a_base_folder_of_the_wrong_type_is_a_config_error_not_a_type_error(
    tmp_path: Path, bad_base: Any
) -> None:
    with pytest.raises(EvidenceFileConfigError, match="base folder must be a text path"):
        FileEvidenceSink(tmp_path / "new" / "ev.jsonl", base_dir=bad_base)
    assert not (tmp_path / "new").exists()


@pytest.mark.parametrize("bad_cap", [0, -1, True, 1.5, "100", None])
def test_a_bad_size_cap_is_refused(tmp_path: Path, bad_cap: Any) -> None:
    with pytest.raises(EvidenceFileConfigError, match="max_record_bytes"):
        FileEvidenceSink(tmp_path / "ev.jsonl", max_record_bytes=bad_cap)


def _device_stat() -> os.stat_result:
    return os.stat_result((stat.S_IFCHR | 0o600, 0, 0, 1, 0, 0, 0, 0, 0, 0))


def test_a_device_found_by_stat_is_refused_before_it_is_opened(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    path = tmp_path / "device.jsonl"
    real_stat = os.stat

    def stat_says_device(p: Any, *args: Any, **kwargs: Any) -> os.stat_result:
        if Path(p) == path:
            return _device_stat()
        return real_stat(p, *args, **kwargs)

    def must_not_open(*args: Any, **kwargs: Any) -> int:
        raise AssertionError("a non-regular file must be refused before it is opened")

    monkeypatch.setattr(os, "stat", stat_says_device)
    monkeypatch.setattr(os, "open", must_not_open)
    with pytest.raises(EvidenceFileConfigError, match="not a regular file"):
        FileEvidenceSink(path)


def test_a_device_found_after_opening_is_refused_and_not_locked(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    path = tmp_path / "ev.jsonl"
    real_fstat = os.fstat
    monkeypatch.setattr(os, "fstat", lambda fd: _device_stat())
    with pytest.raises(EvidenceFileConfigError, match="not a regular file"):
        FileEvidenceSink(path)
    monkeypatch.setattr(os, "fstat", real_fstat)
    FileEvidenceSink(path).close()  # the refused open left neither a lock nor a descriptor


@pytest.mark.skipif(sys.platform == "win32", reason="/dev/null is a POSIX device")
def test_a_device_is_refused_as_not_a_regular_file() -> None:
    with pytest.raises(EvidenceFileConfigError, match="not a regular file"):
        FileEvidenceSink("/dev/null")


@pytest.mark.skipif(sys.platform != "win32", reason="NUL is a Windows device name")
def test_the_windows_nul_device_is_refused(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.chdir(tmp_path)
    with pytest.raises(EvidenceFileConfigError):
        FileEvidenceSink("NUL")


@pytest.mark.skipif(not hasattr(os, "O_NOFOLLOW"), reason="no O_NOFOLLOW on this platform")
def test_a_symlinked_final_name_is_not_followed(tmp_path: Path) -> None:
    target = tmp_path / "target.jsonl"
    target.write_text("")
    link = tmp_path / "link.jsonl"
    try:
        link.symlink_to(target)
    except OSError:
        pytest.skip("cannot create symlinks here")
    with pytest.raises(EvidenceFileConfigError, match="could not be opened"):
        FileEvidenceSink(link)
    assert target.read_bytes() == b""


# ---------------------------------------------------------------------------
# One writer per file
# ---------------------------------------------------------------------------


def test_a_second_sink_on_the_same_file_is_refused_and_the_first_keeps_working(
    tmp_path: Path,
) -> None:
    path = tmp_path / "ev.jsonl"
    first = FileEvidenceSink(path)
    _w(first, _Rec(1))
    with pytest.raises(EvidenceFileConfigError, match="in use by another evidence sink"):
        FileEvidenceSink(path)
    _w(first, _Rec(2))
    first.close()
    assert _ns(path) == [1, 2]
    assert first.write_failures == 0


def test_a_refused_second_sink_does_not_touch_the_file(tmp_path: Path) -> None:
    path = tmp_path / "ev.jsonl"
    first = FileEvidenceSink(path)
    _w(first, _Rec(1))
    with path.open("ab") as raw:  # bytes that a repair would remove if the lock came after it
        raw.write(b'{"torn": ')
    before = path.read_bytes()
    with pytest.raises(EvidenceFileConfigError, match="in use by another evidence sink"):
        FileEvidenceSink(path)
    assert path.read_bytes() == before
    first.close()


def test_a_second_process_is_refused_while_the_first_sink_holds_the_file(tmp_path: Path) -> None:
    path = tmp_path / "ev.jsonl"
    src = Path(efs.__file__).resolve().parent.parent
    code = (
        "import sys\n"
        f"sys.path.insert(0, {str(src)!r})\n"
        "from ztax_gateway.evidence_file_sink import EvidenceFileConfigError, FileEvidenceSink\n"
        "try:\n"
        "    FileEvidenceSink(sys.argv[1])\n"
        "except EvidenceFileConfigError as exc:\n"
        "    print('REFUSED:' + str(exc))\n"
        "    sys.exit(3)\n"
        "print('OPENED')\n"
    )

    def other_process() -> subprocess.CompletedProcess[str]:
        return subprocess.run(
            [sys.executable, "-c", code, str(path)],
            capture_output=True,
            text=True,
            timeout=60,
            check=False,
        )

    first = FileEvidenceSink(path)
    _w(first, _Rec(1))
    refused = other_process()
    assert refused.returncode == 3, refused.stderr
    assert "in use by another evidence sink" in refused.stdout
    _w(first, _Rec(2))  # the first writer is unaffected
    first.close()
    assert _ns(path) == [1, 2]
    allowed = other_process()  # released on close()
    assert allowed.returncode == 0, allowed.stderr
    assert "OPENED" in allowed.stdout


def test_close_releases_the_lock_and_a_later_write_takes_it_again(tmp_path: Path) -> None:
    path = tmp_path / "ev.jsonl"
    first = FileEvidenceSink(path)
    first.close()
    second = FileEvidenceSink(path)  # the lock was released by close()
    second.close()
    _w(first, _Rec(1))  # a write after close() reopens and relocks the file
    assert first.write_failures == 0
    with pytest.raises(EvidenceFileConfigError, match="in use by another evidence sink"):
        FileEvidenceSink(path)
    first.close()
    assert _ns(path) == [1]


def test_a_sink_that_is_garbage_collected_releases_its_lock(tmp_path: Path) -> None:
    path = tmp_path / "ev.jsonl"
    sink = FileEvidenceSink(path)
    _w(sink, _Rec(1))
    del sink
    gc.collect()
    again = FileEvidenceSink(path)  # would be refused if the descriptor had leaked
    again.close()
    assert _ns(path) == [1]


# ---------------------------------------------------------------------------
# Failures never reach the caller
# ---------------------------------------------------------------------------


def test_a_full_disk_is_counted_and_never_raised_and_leaves_no_content_in_logs(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch, caplog: pytest.LogCaptureFixture
) -> None:
    caplog.set_level(logging.DEBUG)
    path = tmp_path / "ev.jsonl"
    sink = FileEvidenceSink(path)
    _w(sink, _Rec(0))

    def full_disk(fd: int, data: memoryview) -> int:
        raise OSError(errno.ENOSPC, f"No space left on device {SENTINEL}")

    monkeypatch.setattr(efs, "_write_fd", full_disk)
    _w(sink, _Rec(1, filler=SENTINEL))  # must not raise
    _w(sink, _Rec(2, filler=SENTINEL))
    assert sink.write_failures == 2
    assert sink.records_written == 1
    blob = _log_blob(caplog)
    assert SENTINEL not in blob
    assert str(tmp_path) not in blob
    errors = _error_lines(caplog)
    assert len(errors) == 1  # one fixed line for the start of the outage, not one per record
    assert "AUDIT GAP" in errors[0].getMessage()

    monkeypatch.undo()  # the disk is freed: the next write succeeds
    _w(sink, _Rec(3))
    sink.close()
    assert _ns(path) == [0, 3]
    assert sink.write_failures == 2


def test_each_new_outage_after_a_recovery_logs_a_new_error_line(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch, caplog: pytest.LogCaptureFixture
) -> None:
    sink = FileEvidenceSink(tmp_path / "ev.jsonl")
    real = efs._write_fd

    def full_disk(fd: int, data: memoryview) -> int:
        raise OSError(errno.ENOSPC, "No space left on device")

    for outage in range(3):
        monkeypatch.setattr(efs, "_write_fd", full_disk)
        _w(sink, _Rec(outage))
        _w(sink, _Rec(outage))  # same outage: no second line
        monkeypatch.setattr(efs, "_write_fd", real)
        _w(sink, _Rec(100 + outage))  # recovery
    assert sink.write_failures == 6
    assert len(_error_lines(caplog)) == 3
    sink.close()


def test_a_long_outage_logs_again_every_thousandth_failure(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch, caplog: pytest.LogCaptureFixture
) -> None:
    sink = FileEvidenceSink(tmp_path / "ev.jsonl")

    def full_disk(fd: int, data: memoryview) -> int:
        raise OSError(errno.ENOSPC, "No space left on device")

    monkeypatch.setattr(efs, "_write_fd", full_disk)
    for n in range(2001):
        _w(sink, _Rec(n))
    assert sink.write_failures == 2001
    assert len(_error_lines(caplog)) == 3  # failures 1, 1000 and 2000
    sink.close()


def test_a_short_write_is_rolled_back_at_once(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    path = tmp_path / "ev.jsonl"
    sink = FileEvidenceSink(path)
    _w(sink, _Rec(0))
    before = path.read_bytes()
    real = efs._write_fd

    def half_then_fail(fd: int, data: memoryview) -> int:
        real(fd, data[: len(data) // 2])
        raise OSError(errno.EIO, "I/O error")

    monkeypatch.setattr(efs, "_write_fd", half_then_fail)
    _w(sink, _Rec(1))
    assert sink.write_failures == 1
    # Checked before any further write or reopen: no torn half line is left behind.
    assert path.read_bytes() == before
    monkeypatch.undo()
    _w(sink, _Rec(2))
    sink.close()
    assert _ns(path) == [0, 2]


def test_a_rollback_that_fails_is_repaired_before_the_next_write(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    path = tmp_path / "ev.jsonl"
    sink = FileEvidenceSink(path)
    _w(sink, _Rec(0))
    real_write = efs._write_fd

    def half_then_fail(fd: int, data: memoryview) -> int:
        real_write(fd, data[: len(data) // 2])
        raise OSError(errno.EIO, "I/O error")

    def no_truncate(fd: int, length: int) -> None:
        raise OSError(errno.EIO, "I/O error")

    monkeypatch.setattr(efs, "_write_fd", half_then_fail)
    monkeypatch.setattr(os, "ftruncate", no_truncate)
    _w(sink, _Rec(1))  # the half line cannot be rolled back
    assert path.read_bytes().count(b"\n") == 1  # one whole record, then a torn tail
    monkeypatch.undo()
    _w(sink, _Rec(2))  # repairs the torn tail first, so it is not glued to record 2
    sink.close()
    assert _ns(path) == [0, 2]


def test_an_fsync_failure_is_counted_and_not_raised(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    sink = FileEvidenceSink(tmp_path / "ev.jsonl")

    def bad_fsync(fd: int) -> None:
        raise OSError(errno.EIO, "I/O error")

    monkeypatch.setattr(efs, "_fsync_fd", bad_fsync)
    _w(sink, _Rec(1))
    assert sink.write_failures == 1
    assert sink.records_written == 0


def test_a_record_that_cannot_be_serialised_is_counted_and_not_raised(
    tmp_path: Path, caplog: pytest.LogCaptureFixture
) -> None:
    class Broken:
        def as_dict(self) -> dict[str, Any]:
            raise RuntimeError(SENTINEL)

    sink = FileEvidenceSink(tmp_path / "ev.jsonl")
    _w(sink, Broken())
    assert sink.write_failures == 1
    assert SENTINEL not in _log_blob(caplog)


def test_a_write_that_cannot_get_its_turn_in_time_is_dropped_and_counted(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    path = tmp_path / "ev.jsonl"
    sink = FileEvidenceSink(path)
    monkeypatch.setattr(efs, "_LOCK_WAIT_SECONDS", 0.2)
    finished = threading.Event()

    def attempt() -> None:
        _w(sink, _Rec(1))
        finished.set()

    sink._io.acquire()  # a thread stuck inside a slow disk call holds the turn
    try:
        worker = threading.Thread(target=attempt, daemon=True)
        started = time.monotonic()
        worker.start()
        assert finished.wait(timeout=5), "write() waited for the lock without a limit"
        assert time.monotonic() - started < 3
        assert sink.write_failures == 1
        assert sink.records_written == 0
    finally:
        sink._io.release()
    _w(sink, _Rec(2))
    sink.close()
    assert _ns(path) == [2]


def test_an_oversize_record_is_dropped_and_counted_and_later_records_still_write(
    tmp_path: Path, caplog: pytest.LogCaptureFixture
) -> None:
    caplog.set_level(logging.DEBUG)
    path = tmp_path / "ev.jsonl"
    sink = FileEvidenceSink(path, max_record_bytes=300)
    _w(sink, _Rec(1, filler=SENTINEL * 100))
    _w(sink, _Rec(2))
    _w(sink, _Rec(3, filler="x" * 400))
    assert sink.records_dropped_oversize == 2
    assert sink.write_failures == 0
    assert _ns(path) == [2]
    assert SENTINEL not in _log_blob(caplog)


def test_a_line_exactly_at_the_cap_is_written_and_one_byte_over_is_dropped(
    tmp_path: Path,
) -> None:
    base = len(json.dumps(_Rec(1).as_dict(), sort_keys=True)) + 1
    sink = FileEvidenceSink(tmp_path / "ev.jsonl", max_record_bytes=base + 10)
    _w(sink, _Rec(1, filler="a" * 10))
    _w(sink, _Rec(2, filler="a" * 11))
    assert sink.records_written == 1
    assert sink.records_dropped_oversize == 1


def test_the_default_cap_is_64_kib() -> None:
    assert DEFAULT_MAX_RECORD_BYTES == 65_536


# ---------------------------------------------------------------------------
# Concurrency
# ---------------------------------------------------------------------------


def test_fifty_threads_writing_lose_nothing_and_never_interleave(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    path = tmp_path / "ev.jsonl"
    sink = FileEvidenceSink(path, fsync=False)
    real = efs._write_fd

    def small_chunks(fd: int, data: memoryview) -> int:
        count = real(fd, data[:16])  # a line goes out in many small pieces...
        time.sleep(0)  # ...and each piece yields, so unlocked writers would interleave
        return count

    monkeypatch.setattr(efs, "_write_fd", small_chunks)
    threads_n, per_thread = 50, 10
    start = threading.Barrier(threads_n)

    def work(t: int) -> None:
        start.wait()
        for i in range(per_thread):
            _w(sink, _Rec(t * 1000 + i, filler="y" * 120))

    threads = [threading.Thread(target=work, args=(t,)) for t in range(threads_n)]
    for th in threads:
        th.start()
    for th in threads:
        th.join(timeout=120)
    assert not any(th.is_alive() for th in threads)
    sink.close()
    assert sink.write_failures == 0
    records = read_evidence_file(path)  # strict: an interleaved line would raise
    assert len(records) == threads_n * per_thread
    assert len({r["n"] for r in records}) == threads_n * per_thread


# ---------------------------------------------------------------------------
# The Gateway
# ---------------------------------------------------------------------------


class _Runtime:
    def run(self, route: Route, call: Call) -> Result:
        return Result(text="synthetic answer", confidence="0.9")


def _gateway(sink: Any = None) -> Gateway:
    registry = UseCaseRegistry()
    registry.register(
        UseCase(
            use_case_id=_UC,
            owner="team-ai",
            description="test use case",
            max_risk_tier=RiskTier.T2,
            max_authority=AuthorityOutcome.A2,
            permitted_regions=frozenset([_REGION]),
            permitted_data_classes=frozenset({"P0", "P1"}),
        )
    )
    return Gateway(
        registry, RoutingTable({_UC: _ROUTE}), _Runtime(), _REGION, "0.7.0", evidence_sink=sink
    )


def _call_bytes(
    use_case: str = _UC, region: str = _REGION, kind: str = "SUGGESTION"
) -> bytes:
    doc = {
        "kind": kind,
        "governance": {
            "tenant_id": _TENANT,
            "use_case": use_case,
            "authority_outcome": "A1",
            "risk_tier": "T1",
            "region": region,
            "data_classes": ["P0"],
        },
        "subject_ref": "sku:FILE-SINK-001",
        "input_b64": base64.b64encode(b'{"note": "x"}').decode("ascii"),
    }
    return json.dumps(doc).encode()


def _factory_gateway(monkeypatch: pytest.MonkeyPatch) -> Gateway:
    monkeypatch.setenv(ENV_PROMPT_PROFILES, str(_CONFIG_DIR / "prompt_profiles.live-demo.json"))
    return build_gateway(
        _CONFIG_DIR / "registry.live-demo.json",
        "local",
        "0.0.0-test",
        runtime_factory=lambda _: FakeRuntime(),
    )


def test_a_failing_file_sink_does_not_break_the_gateway_call(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    memory = InMemoryEvidenceSink()
    file_sink = FileEvidenceSink(tmp_path / "ev.jsonl")
    gateway = _gateway(FanOutEvidenceSink(memory, file_sink))

    def full_disk(fd: int, data: memoryview) -> int:
        raise OSError(errno.ENOSPC, "No space left on device")

    monkeypatch.setattr(efs, "_write_fd", full_disk)
    reply = gateway.invoke(_call_bytes())  # no exception: the call succeeds
    assert json.loads(reply)["text"] == "synthetic answer"
    assert len(memory.records) == 1
    assert file_sink.write_failures == 1


def test_real_records_reach_the_file_through_the_gateway(tmp_path: Path) -> None:
    path = tmp_path / "ev.jsonl"
    memory = InMemoryEvidenceSink()
    gateway = _gateway(FanOutEvidenceSink(memory, FileEvidenceSink(path)))
    gateway.invoke(_call_bytes())
    with pytest.raises(GatewayError):
        gateway.invoke(_call_bytes(use_case="no-such-use-case"))
    on_disk = read_evidence_file(path)
    assert [r["ai_invocation_id"] for r in on_disk] == [
        r.ai_invocation_id for r in memory.records
    ]
    assert len(on_disk) == 2
    assert isinstance(memory.records[0], InvocationEvidenceRecord)


def test_fan_out_keeps_the_primary_fail_closed_and_ignores_an_extra_that_raises() -> None:
    class Failing:
        def write(self, record: object) -> None:
            raise RuntimeError(SENTINEL)

    memory = InMemoryEvidenceSink()
    rec: Any = _Rec(1)
    FanOutEvidenceSink(memory, Failing()).write(rec)  # extra failure is swallowed
    assert len(memory.records) == 1
    with pytest.raises(RuntimeError):
        FanOutEvidenceSink(Failing(), memory).write(rec)  # primary failure still propagates
    assert len(memory.records) == 1  # extras are not reached after the primary fails


# ---------------------------------------------------------------------------
# Off by default; chosen only by build_gateway
# ---------------------------------------------------------------------------


@pytest.mark.parametrize("value", [None, "", "   "])
def test_the_default_is_the_logging_sink_alone(value: str | None) -> None:
    env = {} if value is None else {ENV_PATH: value}
    assert type(default_evidence_sink(env)) is LoggingEvidenceSink


def test_a_plain_gateway_never_writes_the_file_even_when_the_setting_is_present(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    path = tmp_path / "ev.jsonl"
    monkeypatch.setenv(ENV_PATH, str(path))
    gateway = _gateway()
    assert type(gateway._evidence_sink) is LoggingEvidenceSink
    gateway.invoke(_call_bytes())
    assert not path.exists()


def test_build_gateway_without_the_setting_uses_the_logging_sink_alone(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    gateway = _factory_gateway(monkeypatch)
    assert type(gateway._evidence_sink) is LoggingEvidenceSink


def test_build_gateway_with_the_setting_writes_every_record_to_the_file(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    path = tmp_path / "ev.jsonl"
    monkeypatch.setenv(ENV_PATH, str(path))
    gateway = _factory_gateway(monkeypatch)
    assert type(gateway._evidence_sink) is FanOutEvidenceSink
    call = _call_bytes(region="local", kind="CLASSIFICATION_PROPOSAL")
    gateway.invoke(call)
    gateway.invoke(call)
    records = read_evidence_file(path)
    assert len(records) == 2
    assert len({r["ai_invocation_id"] for r in records}) == 2


def test_build_gateway_turns_a_bad_path_into_a_factory_error_with_fixed_text(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    monkeypatch.setenv(ENV_PATH, str(tmp_path))  # a directory
    with pytest.raises(GatewayFactoryError, match="is a directory, not a file") as exc:
        _factory_gateway(monkeypatch)
    assert str(tmp_path) not in str(exc.value)
    assert exc.value.__cause__ is None


def test_the_base_setting_lets_a_missing_folder_be_created(tmp_path: Path) -> None:
    env = {ENV_PATH: str(tmp_path / "b" / "x" / "ev.jsonl"), ENV_BASE_DIR: str(tmp_path / "b")}
    sink = default_evidence_sink(env)
    assert type(sink) is FanOutEvidenceSink
    assert (tmp_path / "b" / "x").is_dir()


def test_a_bad_path_setting_stops_start_up_with_fixed_text(tmp_path: Path) -> None:
    with pytest.raises(EvidenceFileConfigError) as exc:
        default_evidence_sink({ENV_PATH: str(tmp_path)})  # a directory
    assert str(tmp_path) not in str(exc.value)
    with pytest.raises(EvidenceFileConfigError):
        default_evidence_sink({ENV_PATH: str(tmp_path / "nope" / "ev.jsonl")})
    assert not (tmp_path / "nope").exists()
