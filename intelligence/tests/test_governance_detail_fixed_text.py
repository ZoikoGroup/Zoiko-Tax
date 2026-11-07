"""Governance refusals never copy caller-sent text.

A refusal detail used to repeat the use-case id (or region, or a validation message built
from the request) the caller sent. That text reached the caller's error, the INFO log and
the evidence record. Now the detail is fixed text, the refusal context and the record name
an id the registry does not know as ``unregistered``, and the record of a call from another
cell names this cell's own region.

A sentinel is put in every caller field that used to be echoed. It must be absent from the
caller error (detail, str, repr and the chained refusal), from the debug log and from every
evidence record, through ``invoke``, ``invoke_with_id`` and the real gRPC handler. The
refusal codes and status codes are unchanged.
"""

from __future__ import annotations

import base64
import json
import logging
from collections.abc import Callable, Iterator
from dataclasses import dataclass, field, replace
from typing import Any

import grpc
import pytest

from ztax_gateway.evidence_sink import InMemoryEvidenceSink
from ztax_gateway.governance import (
    DETAIL_KILL_SWITCH_ENGAGED,
    DETAIL_MALFORMED_CONTEXT,
    DETAIL_REGION_NOT_PERMITTED,
    DETAIL_UNKNOWN_USE_CASE,
    DETAIL_USE_CASE_SUSPENDED,
    UNREGISTERED_USE_CASE,
    GovernanceRefusedError,
    Refusal,
    UseCase,
    UseCaseRegistry,
    authorise,
)
from ztax_gateway.provenance import AuthorityOutcome, Provenance, RiskTier
from ztax_gateway.runtime import Result, Route, RoutingTable
from ztax_gateway.server import FULL_METHOD, REASON_KEY, Gateway, GatewayError, handler
from ztax_gateway.wire import Call

SENTINEL = "SENTINEL-GOVDETAIL-6e2b90"
_REGION = "eu-west-1"
_UC = "classification-review"
_SUSPENDED_UC = "suspended-review"
_TENANT = "0190f3a2-1b2c-7d3e-8f40-5a6b7c8d9e0f"
_ROUTE = Route("model:fake@1", "provider:fake", "prompt:classification-review-v1")

_ENTRY_POINTS: list[tuple[str, Callable[[Gateway, bytes], Any]]] = [
    ("invoke", lambda gw, data: gw.invoke(data)),
    ("invoke_with_id", lambda gw, data: gw.invoke_with_id(data)),
]


class _Runtime:
    def __init__(self) -> None:
        self.calls = 0

    def run(self, route: Route, call: Call) -> Result:
        self.calls += 1
        return Result(text="ok", confidence="0.9")


def _use_case(use_case_id: str, *, suspended: bool = False) -> UseCase:
    return UseCase(
        use_case_id=use_case_id,
        owner="team-ai",
        description="test use case",
        max_risk_tier=RiskTier.T2,
        max_authority=AuthorityOutcome.A2,
        permitted_regions=frozenset([_REGION]),
        permitted_data_classes=frozenset({"P0", "P1"}),
        suspended=suspended,
    )


def _rig(*, global_kill: bool = False) -> tuple[Gateway, InMemoryEvidenceSink, _Runtime]:
    registry = UseCaseRegistry()
    registry.register(_use_case(_UC))
    registry.register(_use_case(_SUSPENDED_UC, suspended=True))
    if global_kill:
        registry.engage_global_kill()
    sink = InMemoryEvidenceSink()
    runtime = _Runtime()
    routes = RoutingTable({_UC: _ROUTE, _SUSPENDED_UC: _ROUTE})
    gateway = Gateway(registry, routes, runtime, _REGION, "0.7.0", evidence_sink=sink)
    return gateway, sink, runtime


def _call(*, use_case: str = _UC, region: str = _REGION) -> bytes:
    doc = {
        "kind": "SUGGESTION",
        "governance": {
            "tenant_id": _TENANT,
            "use_case": use_case,
            "authority_outcome": "A1",
            "risk_tier": "T1",
            "region": region,
            "data_classes": ["P1"],
        },
        "subject_ref": "sku:GOVDETAIL-001",
        "input_b64": base64.b64encode(b'{"note": "x"}').decode("ascii"),
    }
    return json.dumps(doc).encode("utf-8")


def _log_blob(caplog: pytest.LogCaptureFixture) -> str:
    fmt = logging.Formatter()
    parts: list[str] = []
    for rec in caplog.records:
        parts.append(fmt.format(rec))
        parts.append(repr(vars(rec)))
    return "\n".join(parts)


def _stored_blob(sink: InMemoryEvidenceSink) -> str:
    return json.dumps([r.as_dict() for r in sink.records], default=str, sort_keys=True)


@pytest.fixture()
def debug_logs(caplog: pytest.LogCaptureFixture) -> Iterator[pytest.LogCaptureFixture]:
    with caplog.at_level(logging.DEBUG):
        yield caplog


class _Ctx:
    def __init__(self) -> None:
        self.code: grpc.StatusCode | None = None
        self.detail: str | None = None
        self.metadata: dict[str, str] = {}

    def set_trailing_metadata(self, md: Any) -> None:
        self.metadata = dict(md)

    def abort(self, code: grpc.StatusCode, detail: str) -> None:
        self.code, self.detail = code, detail
        raise RuntimeError("aborted")


@dataclass
class _Case:
    name: str
    data: bytes
    code: grpc.StatusCode
    reason: str
    detail: str
    global_kill: bool = False
    record_use_case: str = field(default=_UC)


def _cases() -> list[_Case]:
    denied = grpc.StatusCode.PERMISSION_DENIED
    return [
        _Case(
            "unknown-use-case",
            _call(use_case=f"uc-{SENTINEL}"),
            denied,
            Refusal.UNKNOWN_USE_CASE.value,
            DETAIL_UNKNOWN_USE_CASE,
            record_use_case=UNREGISTERED_USE_CASE,
        ),
        _Case(
            "kill-switch-on-an-unknown-id",
            _call(use_case=f"uc-{SENTINEL}"),
            denied,
            Refusal.KILL_SWITCH_ENGAGED.value,
            DETAIL_KILL_SWITCH_ENGAGED,
            global_kill=True,
            record_use_case=UNREGISTERED_USE_CASE,
        ),
        _Case(
            "kill-switch-on-a-registered-id",
            _call(),
            denied,
            Refusal.KILL_SWITCH_ENGAGED.value,
            DETAIL_KILL_SWITCH_ENGAGED,
            global_kill=True,
        ),
        _Case(
            "suspended",
            _call(use_case=_SUSPENDED_UC),
            denied,
            Refusal.USE_CASE_SUSPENDED.value,
            DETAIL_USE_CASE_SUSPENDED,
            record_use_case=_SUSPENDED_UC,
        ),
        _Case(
            "another-cells-region",
            _call(region=f"ap-{SENTINEL}"),
            denied,
            Refusal.RESIDENCY_REFUSED.value,
            "call is from a region this Gateway does not serve",
        ),
        _Case(
            "unknown-use-case-and-another-cells-region",
            _call(use_case=f"uc-{SENTINEL}", region=f"ap-{SENTINEL}"),
            denied,
            Refusal.RESIDENCY_REFUSED.value,
            "call is from a region this Gateway does not serve",
            record_use_case=UNREGISTERED_USE_CASE,
        ),
    ]


def _assert_clean(label: str, text: str) -> None:
    assert SENTINEL not in text, f"{label} leaks the sentinel"


@pytest.mark.parametrize("case", _cases(), ids=lambda c: c.name)
@pytest.mark.parametrize(("entry", "invoke"), _ENTRY_POINTS, ids=[e[0] for e in _ENTRY_POINTS])
def test_a_governance_refusal_copies_no_caller_text(
    case: _Case,
    entry: str,
    invoke: Callable[[Gateway, bytes], Any],
    debug_logs: pytest.LogCaptureFixture,
) -> None:
    gateway, sink, runtime = _rig(global_kill=case.global_kill)
    with pytest.raises(GatewayError) as info:
        invoke(gateway, case.data)
    err = info.value
    # Codes unchanged, detail fixed.
    assert err.code == case.code
    assert err.reason == case.reason
    assert err.detail == case.detail
    # No caller text in the error, nor in what it chains to.
    for label, text in (("detail", err.detail), ("str", str(err)), ("repr", repr(err))):
        _assert_clean(f"GatewayError.{label}", text)
    cause = err.__cause__
    if cause is not None:
        _assert_clean("cause text", f"{cause!s} {cause!r}")
        if isinstance(cause, GovernanceRefusedError):
            _assert_clean("cause context", json.dumps(cause.context, default=str))
    # One BLOCK record, free of caller text, the model never called.
    assert runtime.calls == 0
    assert len(sink.records) == 1
    record = sink.records[0]
    assert record.invocation_outcome.value == "BLOCK"
    assert record.caller_security_context_ref == case.record_use_case
    assert record.region == _REGION
    _assert_clean("evidence records", _stored_blob(sink))
    _assert_clean("debug log", _log_blob(debug_logs))


@pytest.mark.parametrize("case", _cases(), ids=lambda c: c.name)
def test_a_governance_refusal_copies_no_caller_text_through_the_real_handler(
    case: _Case, debug_logs: pytest.LogCaptureFixture
) -> None:
    gateway, sink, _ = _rig(global_kill=case.global_kill)
    method = handler(gateway)._method_handlers[FULL_METHOD].unary_unary
    ctx = _Ctx()
    with pytest.raises(RuntimeError, match="aborted"):
        method(case.data, ctx)
    assert ctx.code is case.code
    assert ctx.metadata[REASON_KEY] == case.reason
    assert ctx.detail == case.detail
    _assert_clean("gRPC detail and trailers", json.dumps([ctx.detail, ctx.metadata]))
    _assert_clean("evidence records", _stored_blob(sink))
    _assert_clean("debug log", _log_blob(debug_logs))


@pytest.mark.parametrize(
    ("case_name", "expected"),
    [("unknown-use-case", UNREGISTERED_USE_CASE), ("kill-switch-on-a-registered-id", _UC)],
)
def test_the_refusal_is_logged_at_info_with_a_safe_use_case(
    case_name: str, expected: str, debug_logs: pytest.LogCaptureFixture
) -> None:
    case = next(c for c in _cases() if c.name == case_name)
    gateway, _, _ = _rig(global_kill=case.global_kill)
    with pytest.raises(GatewayError):
        gateway.invoke(case.data)
    lines = [r for r in debug_logs.records if r.getMessage() == "ai crossing refused"]
    assert len(lines) == 1
    assert lines[0].__dict__["use_case"] == expected
    assert lines[0].__dict__["detail"] == case.detail
    assert lines[0].__dict__["refusal"] == case.reason


# ---------------------------------------------------------------------------
# The governance check on its own: fields the transport would have refused first
# ---------------------------------------------------------------------------


def _provenance(**kw: Any) -> Provenance:
    base = Provenance(
        use_case=_UC,
        model_profile="model:fake@1",
        provider_profile="provider:fake",
        prompt_profile="prompt:classification-review-v1",
        region=_REGION,
        data_class="P1",
        risk_tier=RiskTier.T1,
        authority_outcome=AuthorityOutcome.A1,
        ai_train_version="0.7.0",
        data_classes=("P1",),
    )
    return replace(base, **kw)


def _registry() -> UseCaseRegistry:
    registry = UseCaseRegistry()
    registry.register(_use_case(_UC))
    return registry


def test_a_region_the_use_case_does_not_permit_is_not_repeated() -> None:
    with pytest.raises(GovernanceRefusedError) as info:
        authorise(_registry(), _provenance(region=f"ap-{SENTINEL}"))
    err = info.value
    assert err.refusal is Refusal.RESIDENCY_REFUSED
    assert err.detail == DETAIL_REGION_NOT_PERMITTED
    _assert_clean("detail", f"{err.detail} {err}")


def test_a_malformed_context_does_not_quote_the_request_or_chain_to_the_error() -> None:
    bad = _provenance(risk_tier=SENTINEL)
    with pytest.raises(GovernanceRefusedError) as info:
        authorise(_registry(), bad)
    err = info.value
    assert err.refusal is Refusal.MALFORMED_CONTEXT
    assert err.detail == DETAIL_MALFORMED_CONTEXT
    _assert_clean("detail", f"{err.detail} {err}")
    assert err.__cause__ is None
    assert err.__context__ is None


def test_an_unknown_use_case_gets_a_safe_context_but_a_registered_one_keeps_its_id() -> None:
    with pytest.raises(GovernanceRefusedError) as unknown:
        authorise(_registry(), _provenance(use_case=f"uc-{SENTINEL}"))
    assert unknown.value.context["use_case"] == UNREGISTERED_USE_CASE
    _assert_clean("context", json.dumps(unknown.value.context))
    with pytest.raises(GovernanceRefusedError) as known:
        authorise(_registry(), _provenance(region="us-east-1"))
    assert known.value.context["use_case"] == _UC
