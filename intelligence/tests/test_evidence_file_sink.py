"""Tests for the durable evidence file sink (evidence_file_sink.py).

Synthetic data only. Every file lives under pytest's tmp_path. A full disk and a
failing write are simulated by replacing the module's thin ``_write_fd`` and ``_fsync_fd``
wrappers; nothing touches a real disk limit. Sentinel strings stand in for record content.
"""

from __future__ import annotations

import base64
import errno
import json
import logging
import os
import sys
import threading
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


class _Rec:
    """A stand-in with the one method the sink uses."""

    def __init__(self, n: int, filler: str = "") -> None:
        self.n = n
        self.filler = filler

    def as_dict(self) -> dict[str, Any]:
        return {"ai_invocation_id": f"id-{self.n:06d}", "n": self.n, "filler": self.filler}


def _w(sink: FileEvidenceSink, rec: object) -> None:
    sink.write(rec)  # type: ignore[arg-type]


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
    assert [r["n"] for r in read_evidence_file(path)] == [0, 1, 2]
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
    assert [r["n"] for r in read_evidence_file(path)] == list(range(8))


def test_a_torn_last_line_is_skipped_by_the_reader(tmp_path: Path) -> None:
    path = tmp_path / "ev.jsonl"
    sink = FileEvidenceSink(path)
    for n in range(3):
        _w(sink, _Rec(n))
    sink.close()
    with path.open("ab") as fh:
        fh.write(b'{"ai_invocation_id": "id-000003", "n": 3, "fil')  # cut off mid-write
    assert [r["n"] for r in read_evidence_file(path)] == [0, 1, 2]


def test_reopening_removes_a_torn_tail_so_new_records_start_cleanly(
    tmp_path: Path, caplog: pytest.LogCaptureFixture
) -> None:
    caplog.set_level(logging.DEBUG)
    path = tmp_path / "ev.jsonl"
    sink = FileEvidenceSink(path)
    _w(sink, _Rec(0))
    sink.close()
    torn = b'{"ai_invocation_id": "id-000001", "n": 1, "fil' + SENTINEL.encode()
    with path.open("ab") as fh:
        fh.write(torn)
    again = FileEvidenceSink(path)
    assert again.repaired_tail_bytes == len(torn)
    _w(again, _Rec(2))
    again.close()
    assert [r["n"] for r in read_evidence_file(path)] == [0, 2]
    assert SENTINEL not in "\n".join(r.getMessage() + repr(vars(r)) for r in caplog.records)


def test_a_file_with_only_a_torn_line_is_emptied_on_open(tmp_path: Path) -> None:
    path = tmp_path / "ev.jsonl"
    path.write_bytes(b'{"n": 1')
    sink = FileEvidenceSink(path)
    _w(sink, _Rec(7))
    sink.close()
    assert [r["n"] for r in read_evidence_file(path)] == [7]


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
        (lambda tmp: "", "is empty"),
        (lambda tmp: "   ", "is empty"),
        (lambda tmp: str(tmp / "a\x00b.jsonl"), "null byte"),
        (lambda tmp: str(tmp), "directory"),
        (lambda tmp: str(tmp) + os.sep, "directory"),
        (lambda tmp: str(tmp / "dir-style") + "/", "directory"),
        (lambda tmp: ".", "directory"),
        (lambda tmp: "..", "directory"),
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
        "missing-folder-no-base",
        "bytes",
        "int",
    ],
)
def test_a_bad_path_is_refused_with_fixed_text(
    tmp_path: Path, make_path: Any, text: str
) -> None:
    bad = make_path(tmp_path)
    with pytest.raises(EvidenceFileConfigError, match=text) as exc:
        FileEvidenceSink(bad)
    assert str(tmp_path) not in str(exc.value)
    assert exc.value.__cause__ is None


def test_a_missing_folder_is_created_only_inside_the_base(tmp_path: Path) -> None:
    base = tmp_path / "base"
    inside = base / "a" / "b" / "ev.jsonl"
    sink = FileEvidenceSink(inside, base_dir=base)
    _w(sink, _Rec(1))
    sink.close()
    assert [r["n"] for r in read_evidence_file(inside)] == [1]

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


@pytest.mark.parametrize("bad_cap", [0, -1, True, 1.5, "100", None])
def test_a_bad_size_cap_is_refused(tmp_path: Path, bad_cap: Any) -> None:
    with pytest.raises(EvidenceFileConfigError, match="max_record_bytes"):
        FileEvidenceSink(tmp_path / "ev.jsonl", max_record_bytes=bad_cap)


# ---------------------------------------------------------------------------
# Failures never reach the caller
# ---------------------------------------------------------------------------


def _log_blob(caplog: pytest.LogCaptureFixture) -> str:
    return "\n".join(r.getMessage() + repr(vars(r)) for r in caplog.records)


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
    errors = [r for r in caplog.records if r.levelno >= logging.ERROR]
    assert len(errors) == 1  # one fixed line for the first failure, not one per record
    assert "AUDIT GAP" in errors[0].getMessage()

    monkeypatch.undo()  # the disk is freed: the next write reopens and succeeds
    _w(sink, _Rec(3))
    sink.close()
    assert [r["n"] for r in read_evidence_file(path)] == [0, 3]
    assert sink.write_failures == 2


def test_a_short_write_is_rolled_back_so_no_torn_line_is_left(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    path = tmp_path / "ev.jsonl"
    sink = FileEvidenceSink(path)
    _w(sink, _Rec(0))
    real = efs._write_fd

    def half_then_fail(fd: int, data: memoryview) -> int:
        real(fd, data[: len(data) // 2])
        raise OSError(errno.EIO, "I/O error")

    monkeypatch.setattr(efs, "_write_fd", half_then_fail)
    _w(sink, _Rec(1))
    assert sink.write_failures == 1
    monkeypatch.undo()
    _w(sink, _Rec(2))
    sink.close()
    # Strict read: a torn line in the middle would raise.
    assert [r["n"] for r in read_evidence_file(path)] == [0, 2]


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
    assert [r["n"] for r in read_evidence_file(path)] == [2]
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


def test_fifty_threads_writing_lose_nothing_and_never_interleave(tmp_path: Path) -> None:
    path = tmp_path / "ev.jsonl"
    sink = FileEvidenceSink(path, fsync=False)
    threads_n, per_thread = 50, 20
    start = threading.Barrier(threads_n)

    def work(t: int) -> None:
        start.wait()
        for i in range(per_thread):
            _w(sink, _Rec(t * 1000 + i, filler="y" * 200))

    threads = [threading.Thread(target=work, args=(t,)) for t in range(threads_n)]
    for th in threads:
        th.start()
    for th in threads:
        th.join(timeout=60)
    assert not any(th.is_alive() for th in threads)
    sink.close()
    records = read_evidence_file(path)  # strict: any interleaved line would raise
    assert len(records) == threads_n * per_thread
    assert len({r["n"] for r in records}) == threads_n * per_thread
    assert sink.records_written == threads_n * per_thread
    assert sink.write_failures == 0


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


def _call_bytes(use_case: str = _UC) -> bytes:
    doc = {
        "kind": "SUGGESTION",
        "governance": {
            "tenant_id": _TENANT,
            "use_case": use_case,
            "authority_outcome": "A1",
            "risk_tier": "T1",
            "region": _REGION,
            "data_classes": ["P1"],
        },
        "subject_ref": "sku:FILE-SINK-001",
        "input_b64": base64.b64encode(b'{"note": "x"}').decode("ascii"),
    }
    return json.dumps(doc).encode()


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
    refused = _call_bytes(use_case="no-such-use-case")
    with pytest.raises(GatewayError):
        gateway.invoke(refused)
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
# Off by default
# ---------------------------------------------------------------------------


@pytest.mark.parametrize("value", [None, "", "   "])
def test_the_default_is_the_logging_sink_alone(value: str | None) -> None:
    env = {} if value is None else {ENV_PATH: value}
    assert type(default_evidence_sink(env)) is LoggingEvidenceSink


def test_a_gateway_with_no_setting_uses_the_logging_sink_and_makes_no_file(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    monkeypatch.chdir(tmp_path)
    gateway = _gateway()
    assert type(gateway._evidence_sink) is LoggingEvidenceSink
    gateway.invoke(_call_bytes())
    assert list(tmp_path.iterdir()) == []


def test_a_path_setting_turns_the_file_on_next_to_the_logging_sink(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    path = tmp_path / "ev.jsonl"
    monkeypatch.setenv(ENV_PATH, str(path))
    gateway = _gateway()
    assert type(gateway._evidence_sink) is FanOutEvidenceSink
    gateway.invoke(_call_bytes())
    gateway.invoke(_call_bytes())
    records = read_evidence_file(path)
    assert len(records) == 2
    assert len({r["ai_invocation_id"] for r in records}) == 2


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
