"""The system prompt the Groq demo runtime sends hashes to the route's profile.

For each of the four live-demo routes, and each wire kind the route can
receive WITH a prompt registry in force:

* the three explain routes accept every kind and send one prompt, whose hash is
  the ``content_hash`` of the profile named by ``route.prompt_profile``;
* ``classification-review`` accepts ``CLASSIFICATION_PROPOSAL`` (hash matches
  its profile) and refuses the other two kinds before any request is built or
  sent.

No network, no key: ``urllib.request.urlopen`` is replaced and the key is a
placeholder.
"""

from __future__ import annotations

import base64
import hashlib
import importlib.util
import json
from pathlib import Path
from types import ModuleType
from typing import Any

import pytest

from ztax_gateway.evidence_sink import InMemoryEvidenceSink
from ztax_gateway.invocation_check_event import InMemoryCheckEventSink
from ztax_gateway.invocation_evidence import InvocationOutcome
from ztax_gateway.prompt_profile_file import load_prompt_profiles
from ztax_gateway.runtime import RuntimeUnavailableError
from ztax_gateway.server import REASON_UNAVAILABLE, Gateway, GatewayError, load_config
from ztax_gateway.wire import Call, Governance, Kind

_ROOT = Path(__file__).resolve().parent.parent
_REGISTRY = _ROOT / "config" / "registry.live-demo.json"
_PROFILES = _ROOT / "config" / "prompt_profiles.live-demo.json"
_SENTINEL_INPUT = "PARITY-INPUT-2e8b60"
_CLASSIFICATION = "classification-review"
_EXPLAIN = ("explain-report-figure", "explain-obligation-row", "explain-shadow-variance")

_spec = importlib.util.spec_from_file_location(
    "groq_demo_runtime_for_parity", _ROOT / "tools" / "groq_demo_runtime.py"
)
assert _spec is not None and _spec.loader is not None
groq: ModuleType = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(groq)


@pytest.fixture()
def env(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("GROQ_API_KEY", "not-a-real-key-0000")
    monkeypatch.setenv("GROQ_MODEL", "demo-pinned-model-70b")


def _call(use_case: str, kind: Kind) -> Call:
    from ztax_gateway.provenance import AuthorityOutcome, RiskTier

    return Call(
        kind=kind,
        governance=Governance(
            tenant_id="00000000-0000-0000-0000-000000000001",
            use_case=use_case,
            authority_outcome=AuthorityOutcome.A1,
            risk_tier=RiskTier.T1,
            region="local",
            data_classes=("P0",),
        ),
        subject_ref="sku:parity",
        input=json.dumps({"note": _SENTINEL_INPUT}).encode(),
    )


def _raw(use_case: str, kind: Kind) -> bytes:
    doc = {
        "kind": kind.value,
        "governance": {
            "tenant_id": "0190f3a2-1b2c-7d3e-8f40-5a6b7c8d9e0f",
            "use_case": use_case,
            "authority_outcome": "A1",
            "risk_tier": "T1",
            "region": "local",
            "data_classes": ["P0"],
        },
        "subject_ref": "sku:parity",
        "input_b64": base64.b64encode(json.dumps({"note": _SENTINEL_INPUT}).encode()).decode(),
    }
    return json.dumps(doc).encode()


class _Response:
    def __init__(self, body: bytes) -> None:
        self._body = body

    def __enter__(self) -> _Response:
        return self

    def __exit__(self, *exc: object) -> None:
        return None

    def read(self) -> bytes:
        return self._body


def _completion(content: str) -> bytes:
    return json.dumps({"choices": [{"message": {"content": content}}]}).encode()


_EXPLAIN_REPLY = json.dumps(
    {
        "explanation": "Given rows restated.",
        "cited_ids": ["OBL-001"],
        "abstain": False,
        "reason": "",
    }
)


def _reply_for(use_case: str) -> str:
    return _EXPLAIN_REPLY if use_case in _EXPLAIN else "ontology:test/path"


class _Sent:
    """What the runtime handed to urlopen."""

    def __init__(self) -> None:
        self.systems: list[str] = []


def _stub_network(monkeypatch: pytest.MonkeyPatch, reply: str) -> _Sent:
    sent = _Sent()

    def fake_urlopen(request: Any, timeout: float) -> _Response:
        body = json.loads(request.data)
        sent.systems.append(body["messages"][0]["content"])
        return _Response(_completion(reply))

    monkeypatch.setattr(groq.urllib.request, "urlopen", fake_urlopen)
    return sent


def _hash(text: str) -> str:
    return hashlib.sha256(text.encode("utf-8")).hexdigest()


def _rig(
    monkeypatch: pytest.MonkeyPatch,
) -> tuple[Gateway, InMemoryEvidenceSink, InMemoryCheckEventSink, Any]:
    profiles = load_prompt_profiles(_PROFILES)
    registry, routes = load_config(_REGISTRY)
    runtime = groq.GroqDemoRuntime(prompt_registry=profiles)
    runtime.verify_prompts(registry, routes)
    evidence, checks = InMemoryEvidenceSink(), InMemoryCheckEventSink()
    gateway = Gateway(
        registry,
        routes,
        runtime,
        "local",
        "0.0.0-test",
        evidence_sink=evidence,
        prompt_profile_registry=profiles,
        check_event_sink=checks,
    )
    return gateway, evidence, checks, profiles


# The routes and kinds the runtime accepts with a registry in force.
_ACCEPTED = [(uc, k) for uc in _EXPLAIN for k in Kind] + [
    (_CLASSIFICATION, Kind.CLASSIFICATION_PROPOSAL)
]
_REFUSED = [(_CLASSIFICATION, Kind.SUGGESTION), (_CLASSIFICATION, Kind.EXTRACTION)]


def test_the_permitted_kinds_per_route_are_exactly_these() -> None:
    for uc in _EXPLAIN:
        assert set(groq.permitted_kinds(uc)) == set(Kind)
    assert groq.permitted_kinds(_CLASSIFICATION) == (Kind.CLASSIFICATION_PROPOSAL,)


@pytest.mark.parametrize(("use_case", "kind"), _ACCEPTED, ids=lambda v: getattr(v, "value", v))
def test_the_system_message_sent_hashes_to_the_profile_content_hash(
    env: None, use_case: str, kind: Kind
) -> None:
    profiles = load_prompt_profiles(_PROFILES)
    _, routes = load_config(_REGISTRY)
    label = routes.route(use_case).prompt_profile
    profile = profiles.get(label)
    assert profile is not None
    request = groq.build_request(
        "demo-pinned-model-70b", "not-a-real-key-0000", _call(use_case, kind)
    )
    system = json.loads(request.data)["messages"][0]["content"]
    assert _hash(system) == profile.content_hash


# End to end, the reply shape must fit the kind (runtime.check_result): the explain
# routes return fields, so they complete only as EXTRACTION.
_COMPLETING = [(uc, Kind.EXTRACTION) for uc in _EXPLAIN] + [
    (_CLASSIFICATION, Kind.CLASSIFICATION_PROPOSAL)
]
_PROMPT_OK_REPLY_REFUSED = [(uc, k) for uc in _EXPLAIN for k in Kind if k is not Kind.EXTRACTION]


@pytest.mark.parametrize(("use_case", "kind"), _COMPLETING, ids=lambda v: getattr(v, "value", v))
def test_end_to_end_the_record_hash_equals_the_hash_of_the_text_actually_sent(
    env: None, monkeypatch: pytest.MonkeyPatch, use_case: str, kind: Kind
) -> None:
    gateway, evidence, _, _ = _rig(monkeypatch)
    sent = _stub_network(monkeypatch, _reply_for(use_case))
    gateway.invoke(_raw(use_case, kind))
    assert len(sent.systems) == 1
    record = evidence.last
    assert record.invocation_outcome is not InvocationOutcome.BLOCK
    assert record.prompt_content_hash == _hash(sent.systems[0])


@pytest.mark.parametrize(("use_case", "kind"), _REFUSED, ids=lambda v: getattr(v, "value", v))
def test_other_kinds_on_classification_review_are_refused_before_any_request(
    env: None, monkeypatch: pytest.MonkeyPatch, use_case: str, kind: Kind
) -> None:
    profiles = load_prompt_profiles(_PROFILES)
    runtime = groq.GroqDemoRuntime(prompt_registry=profiles)
    built: list[int] = []
    opened: list[int] = []
    monkeypatch.setattr(groq, "build_request", lambda *a, **k: built.append(1))
    monkeypatch.setattr(groq.urllib.request, "urlopen", lambda *a, **k: opened.append(1))
    _, routes = load_config(_REGISTRY)
    with pytest.raises(RuntimeUnavailableError) as info:
        runtime.run(routes.route(use_case), _call(use_case, kind))
    assert built == [] and opened == []
    message = str(info.value)
    assert message == "groq demo runtime: this route does not accept that kind"
    assert _SENTINEL_INPUT not in message


@pytest.mark.parametrize(("use_case", "kind"), _REFUSED, ids=lambda v: getattr(v, "value", v))
def test_a_refused_kind_leaves_a_block_record_with_no_hash_and_no_check_event(
    env: None, monkeypatch: pytest.MonkeyPatch, use_case: str, kind: Kind
) -> None:
    gateway, evidence, checks, _ = _rig(monkeypatch)
    sent = _stub_network(monkeypatch, "ontology:test/path")
    with pytest.raises(GatewayError) as info:
        gateway.invoke_with_id(_raw(use_case, kind))
    assert info.value.reason == REASON_UNAVAILABLE
    assert sent.systems == []
    record = evidence.last
    assert record.invocation_outcome is InvocationOutcome.BLOCK
    assert record.prompt_content_hash is None
    assert checks.events == ()


def test_a_route_with_no_profile_is_refused_before_any_request(
    env: None, monkeypatch: pytest.MonkeyPatch
) -> None:
    from ztax_gateway.production_gates import PromptProfileRegistry

    runtime = groq.GroqDemoRuntime(prompt_registry=PromptProfileRegistry())
    built: list[int] = []
    monkeypatch.setattr(groq, "build_request", lambda *a, **k: built.append(1))
    _, routes = load_config(_REGISTRY)
    with pytest.raises(RuntimeUnavailableError) as info:
        runtime.run(
            routes.route(_CLASSIFICATION), _call(_CLASSIFICATION, Kind.CLASSIFICATION_PROPOSAL)
        )
    assert built == []
    assert str(info.value) == "groq demo runtime: no prompt profile for this route"


def test_without_a_registry_every_kind_still_works_as_before(
    env: None, monkeypatch: pytest.MonkeyPatch
) -> None:
    runtime = groq.GroqDemoRuntime()
    _stub_network(monkeypatch, "a brief suggestion")
    _, routes = load_config(_REGISTRY)
    result = runtime.run(routes.route(_CLASSIFICATION), _call(_CLASSIFICATION, Kind.SUGGESTION))
    assert result.text == "a brief suggestion"


def test_the_shipped_files_pass_the_startup_check(env: None) -> None:
    profiles = load_prompt_profiles(_PROFILES)
    registry, routes = load_config(_REGISTRY)
    groq.GroqDemoRuntime(prompt_registry=profiles).verify_prompts(registry, routes)


@pytest.mark.parametrize(
    ("use_case", "kind"), _PROMPT_OK_REPLY_REFUSED, ids=lambda v: getattr(v, "value", v)
)
def test_explain_routes_send_the_same_prompt_for_a_kind_whose_reply_shape_does_not_fit(
    env: None, monkeypatch: pytest.MonkeyPatch, use_case: str, kind: Kind
) -> None:
    # Pre-existing behaviour, pinned: the prompt sent is still the profile's, but the
    # Gateway refuses the reply shape, so the record is a BLOCK with no prompt hash.
    gateway, evidence, checks, profiles = _rig(monkeypatch)
    sent = _stub_network(monkeypatch, _reply_for(use_case))
    with pytest.raises(GatewayError):
        gateway.invoke_with_id(_raw(use_case, kind))
    label = load_config(_REGISTRY)[1].route(use_case).prompt_profile
    profile = profiles.get(label)
    assert profile is not None
    assert [_hash(text) for text in sent.systems] == [profile.content_hash]
    assert evidence.last.invocation_outcome is InvocationOutcome.BLOCK
    assert evidence.last.prompt_content_hash is None
    assert checks.events == ()
