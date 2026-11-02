"""Live kill switch (AI-09): the running Gateway honours ``killed`` and
``global_kill`` from its registry file on the next call, with no restart.

A kill must take effect on the next call and must never reach the model. The
state is read from the registry file before every call and parsed only when its
bytes change. Anything unreadable fails closed.
"""

from __future__ import annotations

import base64
import json
from pathlib import Path
from typing import Any

import pytest

import ztax_gateway.server as server_mod
from ztax_gateway.evidence_sink import InMemoryEvidenceSink
from ztax_gateway.governance import Refusal
from ztax_gateway.invocation_evidence import InvocationOutcome
from ztax_gateway.runtime import Result, Route
from ztax_gateway.server import Gateway, GatewayError, load_config
from ztax_gateway.wire import Call

_REGION = "eu-west-1"
_UC_A = "classification-review"
_UC_B = "other-use-case"
_TENANT = "0190f3a2-1b2c-7d3e-8f40-5a6b7c8d9e0f"


class _CountingRuntime:
    """Returns a canned suggestion and counts how often it is reached."""

    def __init__(self) -> None:
        self.calls = 0

    def run(self, route: Route, call: Call) -> Result:
        self.calls += 1
        return Result(text="ok", confidence="0.5")


def _use_case(use_case_id: str, **extra: Any) -> dict[str, Any]:
    uc: dict[str, Any] = {
        "use_case_id": use_case_id,
        "owner": "lane-l",
        "description": "test use case",
        "max_risk_tier": "T2",
        "max_authority": "A1",
        "permitted_regions": [_REGION],
        "permitted_data_classes": ["P0", "P1"],
        "suspended": False,
        "route": {
            "model_profile": "model:fake-v1",
            "provider_profile": "provider:fake",
            "prompt_profile": f"prompt:{use_case_id}-v1",
        },
    }
    uc.update(extra)
    return uc


def _write(path: Path, *, killed: Any = None, global_kill: Any = False, **top: Any) -> None:
    doc: dict[str, Any] = {
        "use_cases": [_use_case(_UC_A), _use_case(_UC_B)],
        "killed": [] if killed is None else killed,
        "global_kill": global_kill,
    }
    doc.update(top)
    path.write_text(json.dumps(doc), encoding="utf-8")


def _call(use_case: str = _UC_A) -> bytes:
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
        "subject_ref": "sku:PLAN-5G",
        "input_b64": base64.b64encode(b'{"sku":"PLAN-5G"}').decode("ascii"),
    }
    return json.dumps(doc).encode("utf-8")


class _Rig:
    def __init__(self, path: Path) -> None:
        self.path = path
        self.runtime = _CountingRuntime()
        self.sink = InMemoryEvidenceSink()
        registry, routes = load_config(path)
        self.gateway = Gateway(
            registry,
            routes,
            self.runtime,
            _REGION,
            "0.7.0",
            evidence_sink=self.sink,
            kill_state_path=path,
        )

    def ok(self, use_case: str = _UC_A) -> None:
        self.gateway.invoke(_call(use_case))

    def refused_by_kill(self, use_case: str = _UC_A) -> None:
        before = self.runtime.calls
        with pytest.raises(GatewayError) as info:
            self.gateway.invoke(_call(use_case))
        assert info.value.reason == Refusal.KILL_SWITCH_ENGAGED.value
        assert self.runtime.calls == before, "a killed call reached the runtime"


@pytest.fixture()
def rig(tmp_path: Path) -> _Rig:
    path = tmp_path / "registry.json"
    _write(path)
    return _Rig(path)


# 1. global_kill flips while the server object is running.
def test_global_kill_takes_effect_on_the_next_call_and_never_reaches_the_runtime(
    rig: _Rig,
) -> None:
    rig.ok()
    assert rig.runtime.calls == 1
    _write(rig.path, global_kill=True)
    rig.refused_by_kill(_UC_A)
    rig.refused_by_kill(_UC_B)
    assert rig.runtime.calls == 1
    assert rig.sink.last.invocation_outcome is InvocationOutcome.BLOCK


# 2. A per-use-case kill refuses only that use case.
def test_killing_one_use_case_leaves_the_other_working(rig: _Rig) -> None:
    _write(rig.path, killed=[_UC_A])
    rig.refused_by_kill(_UC_A)
    before = rig.runtime.calls
    rig.ok(_UC_B)
    assert rig.runtime.calls == before + 1


# 3. Unreadable, missing or corrupt state fails closed and recovers.
def test_missing_registry_file_fails_closed_and_recovers(rig: _Rig) -> None:
    rig.ok()
    rig.path.unlink()
    rig.refused_by_kill(_UC_A)
    rig.refused_by_kill(_UC_B)
    _write(rig.path)
    rig.ok(_UC_A)
    rig.ok(_UC_B)


@pytest.mark.parametrize(
    "raw",
    [
        b"",
        b"{not json",
        b"\xff\xfe\x00garbage",
        b"[]",
        b'"just a string"',
        json.dumps({"use_cases": [], "killed": [], "global_kill": "yes"}).encode(),
        json.dumps({"use_cases": [], "killed": [], "global_kill": 1}).encode(),
        json.dumps(
            {"use_cases": [], "killed": "classification-review", "global_kill": False}
        ).encode(),
        json.dumps({"use_cases": [], "killed": [1], "global_kill": False}).encode(),
        json.dumps({"use_cases": [], "killed": [""], "global_kill": False}).encode(),
        json.dumps({"use_cases": [], "killed": None, "global_kill": False}).encode(),
    ],
)
def test_corrupt_or_wrong_typed_state_fails_closed_and_recovers(rig: _Rig, raw: bytes) -> None:
    rig.ok()
    rig.path.write_bytes(raw)
    rig.refused_by_kill(_UC_A)
    rig.refused_by_kill(_UC_B)
    # Still closed on the next call; the bad bytes do not become "no kill".
    rig.refused_by_kill(_UC_A)
    _write(rig.path)
    rig.ok(_UC_A)
    rig.ok(_UC_B)


def test_recovery_works_when_the_fixed_file_has_the_same_bytes_as_before(rig: _Rig) -> None:
    original = rig.path.read_bytes()
    rig.ok()
    rig.path.unlink()
    rig.refused_by_kill()
    rig.path.write_bytes(original)
    rig.ok()


# 4. Un-kill restores service without a restart.
def test_global_unkill_restores_service(rig: _Rig) -> None:
    _write(rig.path, global_kill=True)
    rig.refused_by_kill()
    _write(rig.path, global_kill=False)
    rig.ok(_UC_A)
    rig.ok(_UC_B)


def test_per_use_case_unkill_restores_service(rig: _Rig) -> None:
    _write(rig.path, killed=[_UC_A, _UC_B])
    rig.refused_by_kill(_UC_A)
    rig.refused_by_kill(_UC_B)
    _write(rig.path, killed=[_UC_B])
    rig.ok(_UC_A)
    rig.refused_by_kill(_UC_B)
    _write(rig.path, killed=[])
    rig.ok(_UC_B)


def test_killing_an_unregistered_id_is_accepted_like_the_registry_kill(rig: _Rig) -> None:
    _write(rig.path, killed=["not-registered"])
    rig.ok(_UC_A)
    rig.refused_by_kill("not-registered")


# 8. Only killed / global_kill are synced.
def test_an_edit_that_only_touches_suspended_does_not_change_kill_state(rig: _Rig) -> None:
    doc = json.loads(rig.path.read_text(encoding="utf-8"))
    doc["use_cases"][0]["suspended"] = True
    doc["use_cases"][0]["max_authority"] = "A4"
    doc["use_cases"][0]["permitted_data_classes"] = ["P0", "P1", "P2"]
    rig.path.write_text(json.dumps(doc), encoding="utf-8")
    # The running Gateway keeps what load_config gave it: not suspended, A1, P0/P1.
    rig.ok(_UC_A)
    registry = rig.gateway._registry
    uc = registry.get(_UC_A)
    assert uc is not None
    assert uc.suspended is False
    assert uc.max_authority.value == "A1"
    assert uc.permitted_data_classes == frozenset({"P0", "P1"})
    assert not registry.is_killed(_UC_A)


def test_a_mixed_edit_applies_the_kill_and_ignores_everything_else(rig: _Rig) -> None:
    doc = json.loads(rig.path.read_text(encoding="utf-8"))
    doc["use_cases"][1]["suspended"] = True
    doc["killed"] = [_UC_A]
    rig.path.write_text(json.dumps(doc), encoding="utf-8")
    rig.refused_by_kill(_UC_A)
    rig.ok(_UC_B)  # suspended in the file, but suspension is not live


def test_missing_kill_keys_mean_not_killed(rig: _Rig) -> None:
    doc = json.loads(rig.path.read_text(encoding="utf-8"))
    del doc["killed"], doc["global_kill"]
    rig.path.write_text(json.dumps(doc), encoding="utf-8")
    rig.ok(_UC_A)
    rig.ok(_UC_B)


# The file is parsed only when its bytes change.
def test_state_is_parsed_only_when_the_bytes_change(
    rig: _Rig, monkeypatch: pytest.MonkeyPatch
) -> None:
    parsed: list[int] = []
    real = server_mod._parse_kill_state

    def counting(raw: bytes) -> Any:
        parsed.append(1)
        return real(raw)

    monkeypatch.setattr(server_mod, "_parse_kill_state", counting)
    rig.ok()  # first call: the digest is new
    rig.ok()
    rig.ok()
    assert len(parsed) == 1
    _write(rig.path, killed=[_UC_B])
    rig.ok()
    assert len(parsed) == 2


# A kill set by other code is never released by the file, and vice versa.
def test_file_sync_does_not_release_a_kill_it_did_not_make(rig: _Rig) -> None:
    rig.gateway._registry.kill(_UC_A)
    rig.gateway._registry.engage_global_kill()
    _write(rig.path, global_kill=False, killed=[])
    rig.refused_by_kill(_UC_A)
    rig.gateway._registry.release_global_kill()
    rig.refused_by_kill(_UC_A)  # the per-use-case kill made in code still holds


def test_fail_closed_does_not_release_an_existing_global_kill_on_recovery(rig: _Rig) -> None:
    rig.gateway._registry.engage_global_kill()
    rig.path.unlink()
    rig.refused_by_kill()
    _write(rig.path)
    rig.refused_by_kill()  # engaged by code, not by the file: stays engaged


# Without a path the guard is off and nothing changes.
def test_without_a_kill_state_path_nothing_is_read(tmp_path: Path) -> None:
    path = tmp_path / "registry.json"
    _write(path)
    registry, routes = load_config(path)
    runtime = _CountingRuntime()
    gw = Gateway(registry, routes, runtime, _REGION, "0.7.0", evidence_sink=InMemoryEvidenceSink())
    _write(path, global_kill=True)
    gw.invoke(_call())
    assert runtime.calls == 1
