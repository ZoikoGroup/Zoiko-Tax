"""The Python registries conform to the canonical AI governance schemas.

ADR-0006 §2.2 makes JSON Schema the schema authority across the boundary, and
``contracts/schemas/ai/`` holds the canonical form of the ZTAX-AIGOV-001 §3/§7
registry objects. The registries in this package were written first; this test
is what stops the two drifting. It serializes real registry objects -- built
through the same constructors production uses -- and validates them against the
schemas, and it asserts the closed vocabularies (A0-A5, T0-T4, refusal codes,
privacy classes) are the same lists on both sides.

The Go half is ``backend/internal/adapter/gateway/schema_vocabulary_test.go``.

Why a validator in this file rather than the ``jsonschema`` package: the
Gateway's dependency list is deliberately empty (see pyproject.toml), and a
development dependency added only to read eleven files is not worth the supply
chain it brings. The validator below implements the subset of draft 2020-12
the schemas use, and -- the part that makes it safe -- it fails on any keyword
it does not implement, so a schema can never rely on a rule this test silently
skips.
"""

from __future__ import annotations

import dataclasses
import json
import re
from collections.abc import Mapping
from datetime import UTC, datetime, timedelta
from enum import Enum
from pathlib import Path
from typing import Any

import pytest

from ztax_gateway.governance import Refusal, UseCase
from ztax_gateway.production_registries import (
    AgentMemoryPolicy,
    AgentProfile,
    DatasetProfile,
    EvaluationProfile,
    PromptProfile,
    build_manifest,
)
from ztax_gateway.provenance import AuthorityOutcome, RiskTier
from ztax_gateway.tool_broker import ActionClass, ToolProfile

_REPO = Path(__file__).resolve().parents[2]
_SCHEMAS = _REPO / "contracts" / "schemas" / "ai"
_PRIVACY_VOCABULARY = _REPO / "contracts" / "privacy" / "vocabulary.json"

# Refusal codes the Go policy gate raises and the Python Gateway does not yet.
# Listed explicitly so that a code appearing on one side only is a decision
# somebody wrote down, not drift: AI_DATA_CLASS_REFUSED needs a data-class
# allowlist on UseCase, which the Python registry does not carry yet.
_GO_ONLY_REFUSALS = frozenset({"AI_DATA_CLASS_REFUSED"})

_ALL_SCHEMAS = (
    "common",
    "authority-outcome",
    "risk-tier",
    "governance-refusal",
    "ai-use-case",
    "model-profile",
    "provider-profile",
    "prompt-profile",
    "agent-profile",
    "tool-profile",
    "dataset-profile",
    "evaluation-profile",
    "ai-release-manifest",
    "gateway-call",
    "gateway-reply",
)

# ---------------------------------------------------------------------------
# A minimal draft 2020-12 validator
# ---------------------------------------------------------------------------

# Keywords that carry no assertion. format is an annotation in 2020-12 unless a
# vocabulary turns it on, and none does here.
_ANNOTATIONS = frozenset(
    {"$schema", "$comment", "$defs", "title", "description", "format", "examples"}
)
_ASSERTIONS = frozenset(
    {
        "$ref",
        "type",
        "enum",
        "const",
        "allOf",
        "anyOf",
        "not",
        "required",
        "properties",
        "additionalProperties",
        "items",
        "minItems",
        "uniqueItems",
        "minLength",
        "maxLength",
        "pattern",
        "minimum",
        "maximum",
    }
)


def _load(name: str) -> dict[str, Any]:
    with (_SCHEMAS / f"{name}.schema.json").open(encoding="utf-8") as f:
        loaded: dict[str, Any] = json.load(f)
    return loaded


def _type_ok(value: object, t: str) -> bool:
    match t:
        case "object":
            return isinstance(value, dict)
        case "array":
            return isinstance(value, list)
        case "string":
            return isinstance(value, str)
        case "boolean":
            return isinstance(value, bool)
        case "integer":
            return isinstance(value, int) and not isinstance(value, bool)
        case "number":
            return isinstance(value, int | float) and not isinstance(value, bool)
        case "null":
            return value is None
    raise AssertionError(f"unknown type {t!r}")


class _Validator:
    def __init__(self) -> None:
        self._docs: dict[str, dict[str, Any]] = {n: _load(n) for n in _ALL_SCHEMAS}

    def _resolve(self, ref: str, base: str) -> tuple[dict[str, Any], str]:
        file_part, _, pointer = ref.partition("#")
        doc_name = file_part.removesuffix(".schema.json") if file_part else base
        node: Any = self._docs[doc_name]
        for token in [p for p in pointer.split("/") if p]:
            node = node[token]
        assert isinstance(node, dict), f"$ref {ref!r} does not resolve to a schema"
        return node, doc_name

    def errors(self, schema: Mapping[str, Any], value: object, base: str, path: str) -> list[str]:
        out: list[str] = []
        for kw in schema:
            if kw.startswith("x-") or kw in _ANNOTATIONS:
                continue
            assert kw in _ASSERTIONS, f"{base}: keyword {kw!r} is not implemented by this validator"
        if "$ref" in schema:
            target, doc = self._resolve(schema["$ref"], base)
            out += self.errors(target, value, doc, path)
        if "type" in schema:
            types = schema["type"] if isinstance(schema["type"], list) else [schema["type"]]
            if not any(_type_ok(value, t) for t in types):
                return [*out, f"{path}: {value!r} is not of type {types}"]
        if "enum" in schema and value not in schema["enum"]:
            out.append(f"{path}: {value!r} is not one of {schema['enum']}")
        if "const" in schema and value != schema["const"]:
            out.append(f"{path}: {value!r} is not {schema['const']!r}")
        for sub in schema.get("allOf", []):
            out += self.errors(sub, value, base, path)
        if "anyOf" in schema and all(self.errors(s, value, base, path) for s in schema["anyOf"]):
            out.append(f"{path}: {value!r} matches no anyOf branch")
        if "not" in schema and not self.errors(schema["not"], value, base, path):
            out.append(f"{path}: {value!r} matches a forbidden schema")
        if isinstance(value, dict):
            out += self._object(schema, value, base, path)
        if isinstance(value, list):
            out += self._array(schema, value, base, path)
        if isinstance(value, str):
            if len(value) < schema.get("minLength", 0):
                out.append(f"{path}: {value!r} is shorter than {schema['minLength']}")
            if "maxLength" in schema and len(value) > schema["maxLength"]:
                out.append(f"{path}: {value!r} is longer than {schema['maxLength']}")
            if "pattern" in schema and not re.search(schema["pattern"], value):
                out.append(f"{path}: {value!r} does not match {schema['pattern']!r}")
        if _type_ok(value, "number"):
            assert isinstance(value, int | float)
            if "minimum" in schema and value < schema["minimum"]:
                out.append(f"{path}: {value!r} is below {schema['minimum']}")
            if "maximum" in schema and value > schema["maximum"]:
                out.append(f"{path}: {value!r} is above {schema['maximum']}")
        return out

    def _object(
        self, schema: Mapping[str, Any], value: dict[str, Any], base: str, path: str
    ) -> list[str]:
        out = [f"{path}: missing {k!r}" for k in schema.get("required", []) if k not in value]
        props: dict[str, Any] = schema.get("properties", {})
        addl = schema.get("additionalProperties", True)
        for k, v in value.items():
            if k in props:
                out += self.errors(props[k], v, base, f"{path}.{k}")
            elif addl is False:
                out.append(f"{path}: undeclared field {k!r}")
            elif isinstance(addl, dict):
                out += self.errors(addl, v, base, f"{path}.{k}")
        return out

    def _array(
        self, schema: Mapping[str, Any], value: list[Any], base: str, path: str
    ) -> list[str]:
        out: list[str] = []
        if len(value) < schema.get("minItems", 0):
            out.append(f"{path}: fewer than {schema['minItems']} items")
        distinct = {json.dumps(v, sort_keys=True) for v in value}
        if schema.get("uniqueItems") and len(distinct) != len(value):
            out.append(f"{path}: items are not unique")
        if "items" in schema:
            for i, v in enumerate(value):
                out += self.errors(schema["items"], v, base, f"{path}[{i}]")
        return out

    def validate(self, name: str, value: object) -> list[str]:
        return self.errors(self._docs[name], value, name, name)


@pytest.fixture(scope="module")
def validator() -> _Validator:
    return _Validator()


# ---------------------------------------------------------------------------
# Serialization: the full object, not the log-safe as_dict projection
# ---------------------------------------------------------------------------


def _wire(value: object) -> object:
    """The JSON form of a registry field, as the schemas define it."""
    if isinstance(value, Enum):
        return value.value
    if isinstance(value, frozenset | set):
        return sorted(str(_wire(v)) for v in value)
    if isinstance(value, tuple | list):
        return [_wire(v) for v in value]
    if isinstance(value, timedelta):
        return value.total_seconds()
    if isinstance(value, datetime):
        return value.isoformat()
    if isinstance(value, dict):
        return {k: _wire(v) for k, v in value.items()}
    return value


def _serialize(obj: object) -> dict[str, object]:
    assert dataclasses.is_dataclass(obj) and not isinstance(obj, type)
    return {f.name: _wire(getattr(obj, f.name)) for f in dataclasses.fields(obj)}


_USE_CASE = UseCase(
    use_case_id="classification-review",
    owner="lane-l",
    description="Propose a product classification for human review.",
    max_risk_tier=RiskTier.T2,
    max_authority=AuthorityOutcome.A1,
    permitted_regions=frozenset({"eu-west-1", "eu-central-1"}),
)

_PROMPT = PromptProfile(
    profile_id="prompt:classification-review",
    owner="lane-l",
    version="1.0.0",
    system_prompt="Propose one ontology node and cite the source.",
    variable_schema={"type": "object", "properties": {"sku": {"type": "string"}}},
    output_schema={"type": "object", "required": ["node"]},
    max_risk_tier=RiskTier.T2,
    authority_ceiling=AuthorityOutcome.A1,
    safety_rules=("never emit a monetary amount", "abstain when sources conflict"),
)

_AGENT = AgentProfile(
    profile_id="agent:recon-investigator",
    owner="lane-l",
    version="1.0.0",
    allowed_tool_ids=frozenset({"ledger-read", "invoice-extractor"}),
    memory_policy=AgentMemoryPolicy.SESSION,
    max_steps=20,
    max_token_budget=50_000,
)

_TOOL = ToolProfile(
    tool_id="invoice-extractor",
    owner="lane-l",
    description="Extract line items from an invoice.",
    action_class=ActionClass.MUTATE,
    required_scopes=frozenset({"write", "extract"}),
    idempotent=True,
    max_steps=200,
    max_duration=timedelta(seconds=30),
    max_token_cost=4096,
    max_monetary_cost=0.10,
)

_DATASET = DatasetProfile(
    dataset_id="gold:classification-v1",
    owner="lane-l",
    version="1.0.0",
    description="Labelled SKU classification cases.",
    data_class="INTERNAL",
    case_count=250,
)

_EVALUATION = EvaluationProfile(
    profile_id="eval:classification-review",
    owner="lane-l",
    version="1.0.0",
    dataset_id="gold:classification-v1",
    min_accuracy=0.92,
    min_f1=None,
    max_calibration_error=0.05,
)

_MANIFEST = build_manifest(
    ai_use_case_id="classification-review",
    release_version="1.0.0",
    authority_level=AuthorityOutcome.A1,
    risk_tier=RiskTier.T2,
    model_profile_id="model:classifier",
    resolved_model_id="classifier-2026-09-01",
    prompt_profile_id="prompt:classification-review",
    evaluation_profile_id="eval:classification-review",
    policy_bundle_id="policy:2026.09",
    allowed_regions=frozenset({"eu-west-1"}),
    approved_at=datetime(2026, 9, 22, 12, 0, tzinfo=UTC),
    manifest_id="manifest-0001",
)


# ---------------------------------------------------------------------------
# Tests
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(
    ("schema", "obj"),
    [
        ("ai-use-case", _USE_CASE),
        ("prompt-profile", _PROMPT),
        ("agent-profile", _AGENT),
        ("tool-profile", _TOOL),
        ("dataset-profile", _DATASET),
        ("evaluation-profile", _EVALUATION),
        ("ai-release-manifest", _MANIFEST),
    ],
)
def test_registry_object_conforms(validator: _Validator, schema: str, obj: object) -> None:
    assert validator.validate(schema, _serialize(obj)) == []


@pytest.mark.parametrize(
    ("schema", "cls"),
    [
        ("ai-use-case", UseCase),
        ("prompt-profile", PromptProfile),
        ("agent-profile", AgentProfile),
        ("tool-profile", ToolProfile),
        ("dataset-profile", DatasetProfile),
        ("evaluation-profile", EvaluationProfile),
    ],
)
def test_schema_requires_exactly_the_python_fields(schema: str, cls: Any) -> None:
    # Required is the Python field set, so a field added to a dataclass without
    # a schema change -- or the reverse -- fails here rather than at the
    # boundary. Optional schema fields are the AIGOV-001 remainder.
    python_fields = {f.name for f in dataclasses.fields(cls)}
    assert set(_load(schema)["required"]) == python_fields


def test_manifest_schema_is_the_as_dict_shape() -> None:
    assert set(_load("ai-release-manifest")["required"]) == set(_MANIFEST.as_dict())


def test_manifest_as_dict_conforms(validator: _Validator) -> None:
    assert validator.validate("ai-release-manifest", _MANIFEST.as_dict()) == []


@pytest.mark.parametrize(
    ("schema", "obj", "field", "bad"),
    [
        # A5 is refused as a ceiling everywhere a ceiling is declared.
        ("ai-use-case", _USE_CASE, "max_authority", "A5"),
        ("prompt-profile", _PROMPT, "authority_ceiling", "A5"),
        ("ai-release-manifest", _MANIFEST, "authority_level", "A5"),
        ("tool-profile", _TOOL, "action_class", "PRIVILEGED"),
        ("ai-use-case", _USE_CASE, "max_risk_tier", "T5"),
        ("ai-release-manifest", _MANIFEST, "allowed_regions", []),
        ("ai-release-manifest", _MANIFEST, "manifest_hash", "not-a-digest"),
        ("dataset-profile", _DATASET, "case_count", 1),
        ("evaluation-profile", _EVALUATION, "min_accuracy", 1.5),
        ("agent-profile", _AGENT, "max_steps", 0),
        ("agent-profile", _AGENT, "undeclared", True),
    ],
)
def test_schema_refuses(
    validator: _Validator, schema: str, obj: object, field: str, bad: object
) -> None:
    doc = _serialize(obj)
    doc[field] = bad
    assert validator.validate(schema, doc) != []


def test_model_and_provider_profiles_validate(validator: _Validator) -> None:
    # No Python registry for these yet; this pins the schemas themselves to a
    # representative object so a schema edit that makes them unsatisfiable fails.
    model: dict[str, object] = {
        "model_profile_id": "model:classifier",
        "owner": "lane-l",
        "version": "1.0.0",
        "provider_profile_id": "provider:eu-hosted",
        "model_id": "classifier",
        "model_version": "2026-09-01",
        "hosting": "PRIVATE_CLOUD",
        "context_window_tokens": 128_000,
        "max_output_tokens": 4096,
        "modalities": ["TEXT"],
        "tool_capable": False,
        "permitted_regions": ["eu-west-1"],
        "permitted_data_classes": ["P0", "P1"],
        "terms_ref": "contract:2026-001",
        "known_limitations": ["no tabular reasoning"],
        "deprecation_date": None,
        "suspended": False,
    }
    assert validator.validate("model-profile", model) == []
    assert validator.validate("model-profile", {**model, "model_version": "latest"}) != []

    provider: dict[str, object] = {
        "provider_profile_id": "provider:eu-hosted",
        "owner": "lane-l",
        "version": "1.0.0",
        "legal_entity": "Example Hosting B.V.",
        "dpa_ref": "dpa:2026-001",
        "security_assurance": ["ISO27001"],
        "permitted_regions": ["eu-west-1"],
        "subprocessors": [],
        "retention_days": 0,
        "zero_data_retention": True,
        "trains_on_customer_data": False,
        "deprecation_notice_days": 90,
        "outage_terms_ref": "sla:2026-001",
        "audit_assurance_ref": "soc2:2026",
        "api_hosts": ["inference.eu.example.net"],
        "suspended": False,
    }
    assert validator.validate("provider-profile", provider) == []
    assert validator.validate("provider-profile", {**provider, "api_hosts": []}) != []


def test_authority_and_risk_vocabularies_match() -> None:
    assert _load("authority-outcome")["enum"] == [a.value for a in AuthorityOutcome]
    assert _load("risk-tier")["enum"] == [t.value for t in RiskTier]
    assert _load("authority-outcome")["x-ztax-prohibited"] == [AuthorityOutcome.A5.value]


def test_refusal_vocabulary_matches() -> None:
    schema = set(_load("governance-refusal")["enum"])
    python = {r.value for r in Refusal}
    assert python | _GO_ONLY_REFUSALS == schema
    assert python.isdisjoint(_GO_ONLY_REFUSALS), "a Go-only refusal is now raised in Python"


def test_privacy_classes_match_the_shared_vocabulary() -> None:
    with _PRIVACY_VOCABULARY.open(encoding="utf-8") as f:
        classes = sorted(json.load(f)["classes"])
    assert _load("common")["$defs"]["privacyClass"]["enum"] == classes

# ---------------------------------------------------------------------------
# Gateway call and reply schemas
# ---------------------------------------------------------------------------
# test_server.py validates gateway-call and gateway-reply via a live gRPC
# channel; the tests here exercise the schemas directly with plain dicts so
# that every schema file in contracts/schemas/ai/ is covered by this file.
# ---------------------------------------------------------------------------

import base64  # noqa: E402

from ztax_gateway.wire import Call, Governance, Reply  # noqa: E402

_TENANT = "0190f3a2-1b2c-7d3e-8f40-5a6b7c8d9e0f"
_REGION = "euc1-dev-01"
_INPUT_B64 = base64.b64encode(b'{"sku":"PLAN-5G"}').decode()

_CALL_MIN: dict[str, Any] = {
    "kind": "SUGGESTION",
    "governance": {
        "tenant_id": _TENANT,
        "use_case": "classification-review",
        "authority_outcome": "A1",
        "risk_tier": "T1",
        "region": _REGION,
        "data_classes": ["P0"],
    },
    "subject_ref": "sku:PLAN-5G",
    "input_b64": _INPUT_B64,
}

_REPLY_MIN: dict[str, Any] = {
    "model_profile": "model:cls-2027.03",
    "provider_profile": "provider:eu-hosted",
    "prompt_profile": "prompt:cls-v4",
    "ai_train_version": "ai-2027.03.1",
}

_REPLY_FULL: dict[str, Any] = {
    **_REPLY_MIN,
    "text": "Consider the reduced rate.",
    "payload": '{"detail":"none"}',
    "fields": {"invoiceNumber": "INV-001"},
    "proposed_code": "ontology:telecom/voice/mobile",
    "confidence": "0.97",
}


# --- gateway-call positive --------------------------------------------------


def test_gateway_call_minimal_conforms(validator: _Validator) -> None:
    assert validator.validate("gateway-call", _CALL_MIN) == []


@pytest.mark.parametrize("kind", ["SUGGESTION", "EXTRACTION", "CLASSIFICATION_PROPOSAL"])
def test_gateway_call_all_kinds_conform(validator: _Validator, kind: str) -> None:
    doc = {**_CALL_MIN, "kind": kind}
    assert validator.validate("gateway-call", doc) == []


@pytest.mark.parametrize("outcome", ["A0", "A1", "A2", "A3", "A4", "A5"])
def test_gateway_call_all_authority_outcomes_conform(
    validator: _Validator, outcome: str
) -> None:
    gov = {**_CALL_MIN["governance"], "authority_outcome": outcome}
    assert validator.validate("gateway-call", {**_CALL_MIN, "governance": gov}) == []


@pytest.mark.parametrize("tier", ["T0", "T1", "T2", "T3", "T4"])
def test_gateway_call_all_risk_tiers_conform(validator: _Validator, tier: str) -> None:
    gov = {**_CALL_MIN["governance"], "risk_tier": tier}
    assert validator.validate("gateway-call", {**_CALL_MIN, "governance": gov}) == []


@pytest.mark.parametrize("cls", ["P0", "P1", "P2", "P3", "P4", "P5", "P6", "P7"])
def test_gateway_call_every_privacy_class_is_valid(validator: _Validator, cls: str) -> None:
    gov = {**_CALL_MIN["governance"], "data_classes": [cls]}
    assert validator.validate("gateway-call", {**_CALL_MIN, "governance": gov}) == []


def test_gateway_call_multiple_privacy_classes_conform(validator: _Validator) -> None:
    gov = {**_CALL_MIN["governance"], "data_classes": ["P0", "P3", "P5"]}
    assert validator.validate("gateway-call", {**_CALL_MIN, "governance": gov}) == []


def test_gateway_call_subject_ref_at_max_length_conforms(validator: _Validator) -> None:
    doc = {**_CALL_MIN, "subject_ref": "x" * 512}
    assert validator.validate("gateway-call", doc) == []


# --- gateway-call negative --------------------------------------------------


@pytest.mark.parametrize("field", ["kind", "governance", "subject_ref", "input_b64"])
def test_gateway_call_refuses_when_top_field_missing(
    validator: _Validator, field: str
) -> None:
    doc = {k: v for k, v in _CALL_MIN.items() if k != field}
    assert validator.validate("gateway-call", doc) != []


@pytest.mark.parametrize(
    "gov_field",
    ["tenant_id", "use_case", "authority_outcome", "risk_tier", "region", "data_classes"],
)
def test_gateway_call_refuses_missing_governance_field(
    validator: _Validator, gov_field: str
) -> None:
    gov = {k: v for k, v in _CALL_MIN["governance"].items() if k != gov_field}
    assert validator.validate("gateway-call", {**_CALL_MIN, "governance": gov}) != []


def test_gateway_call_refuses_unknown_kind(validator: _Validator) -> None:
    assert validator.validate("gateway-call", {**_CALL_MIN, "kind": "AUTONOMOUS_DECISION"}) != []


def test_gateway_call_refuses_extra_top_level_field(validator: _Validator) -> None:
    assert validator.validate("gateway-call", {**_CALL_MIN, "extra": "surprise"}) != []


def test_gateway_call_refuses_extra_governance_field(validator: _Validator) -> None:
    gov = {**_CALL_MIN["governance"], "internal_note": "bypass"}
    assert validator.validate("gateway-call", {**_CALL_MIN, "governance": gov}) != []


def test_gateway_call_refuses_malformed_tenant_uuid(validator: _Validator) -> None:
    gov = {**_CALL_MIN["governance"], "tenant_id": "not-a-uuid"}
    assert validator.validate("gateway-call", {**_CALL_MIN, "governance": gov}) != []


def test_gateway_call_refuses_empty_data_classes(validator: _Validator) -> None:
    gov = {**_CALL_MIN["governance"], "data_classes": []}
    assert validator.validate("gateway-call", {**_CALL_MIN, "governance": gov}) != []


def test_gateway_call_refuses_unknown_privacy_class(validator: _Validator) -> None:
    gov = {**_CALL_MIN["governance"], "data_classes": ["UNRESTRICTED"]}
    assert validator.validate("gateway-call", {**_CALL_MIN, "governance": gov}) != []


def test_gateway_call_refuses_duplicate_privacy_classes(validator: _Validator) -> None:
    gov = {**_CALL_MIN["governance"], "data_classes": ["P0", "P0"]}
    assert validator.validate("gateway-call", {**_CALL_MIN, "governance": gov}) != []


def test_gateway_call_refuses_subject_ref_too_long(validator: _Validator) -> None:
    assert validator.validate("gateway-call", {**_CALL_MIN, "subject_ref": "x" * 513}) != []


def test_gateway_call_refuses_unknown_authority_outcome(validator: _Validator) -> None:
    gov = {**_CALL_MIN["governance"], "authority_outcome": "A9"}
    assert validator.validate("gateway-call", {**_CALL_MIN, "governance": gov}) != []


def test_gateway_call_refuses_unknown_risk_tier(validator: _Validator) -> None:
    gov = {**_CALL_MIN["governance"], "risk_tier": "T9"}
    assert validator.validate("gateway-call", {**_CALL_MIN, "governance": gov}) != []


# --- gateway-call Python type parity ----------------------------------------


def test_gateway_call_schema_required_matches_call_wire_fields() -> None:
    python_fields = {f.name for f in dataclasses.fields(Call)}
    mapped = (python_fields - {"input"}) | {"input_b64"}
    schema_required = set(_load("gateway-call")["required"])
    assert schema_required == mapped


def test_gateway_governance_schema_required_matches_governance_fields() -> None:
    python_fields = {f.name for f in dataclasses.fields(Governance)}
    schema_required = set(_load("gateway-call")["properties"]["governance"]["required"])
    assert schema_required == python_fields


# --- gateway-reply positive --------------------------------------------------


def test_gateway_reply_routing_only_conforms(validator: _Validator) -> None:
    assert validator.validate("gateway-reply", _REPLY_MIN) == []


def test_gateway_reply_full_conforms(validator: _Validator) -> None:
    assert validator.validate("gateway-reply", _REPLY_FULL) == []


def test_gateway_reply_text_suggestion_conforms(validator: _Validator) -> None:
    doc = {**_REPLY_MIN, "text": "Consider the reduced rate."}
    assert validator.validate("gateway-reply", doc) == []


def test_gateway_reply_extraction_conforms(validator: _Validator) -> None:
    doc = {**_REPLY_MIN, "fields": {"invoiceNumber": "INV-001", "amount": "99.00"}}
    assert validator.validate("gateway-reply", doc) == []


def test_gateway_reply_classification_conforms(validator: _Validator) -> None:
    doc = {**_REPLY_MIN, "proposed_code": "ontology:telecom/voice", "confidence": "0.97"}
    assert validator.validate("gateway-reply", doc) == []


@pytest.mark.parametrize(
    "confidence",
    ["", "0", "0.0", "0.5", "0.99", "1", "1.0", "0.000001", "0.999999"],
)
def test_gateway_reply_confidence_valid_values_conform(
    validator: _Validator, confidence: str
) -> None:
    doc = {**_REPLY_MIN, "confidence": confidence}
    assert validator.validate("gateway-reply", doc) == [], f"confidence {confidence!r} rejected"


# --- gateway-reply negative --------------------------------------------------


@pytest.mark.parametrize(
    "field", ["model_profile", "provider_profile", "prompt_profile", "ai_train_version"]
)
def test_gateway_reply_refuses_missing_routing_field(
    validator: _Validator, field: str
) -> None:
    doc = {k: v for k, v in _REPLY_MIN.items() if k != field}
    assert validator.validate("gateway-reply", doc) != []


def test_gateway_reply_refuses_extra_field(validator: _Validator) -> None:
    assert validator.validate("gateway-reply", {**_REPLY_MIN, "extra": "field"}) != []


@pytest.mark.parametrize(
    "confidence", ["1.1", "2.0", "-0.1", "0.5%", "high", "1.00001", ".", "0,5"]
)
def test_gateway_reply_refuses_invalid_confidence(
    validator: _Validator, confidence: str
) -> None:
    doc = {**_REPLY_MIN, "confidence": confidence}
    assert validator.validate("gateway-reply", doc) != [], f"bad confidence {confidence!r} accepted"


def test_gateway_reply_refuses_non_string_fields_value(validator: _Validator) -> None:
    doc = {**_REPLY_MIN, "fields": {"amount": 99}}
    assert validator.validate("gateway-reply", doc) != []


def test_gateway_reply_refuses_empty_model_profile(validator: _Validator) -> None:
    assert validator.validate("gateway-reply", {**_REPLY_MIN, "model_profile": ""}) != []


def test_gateway_reply_refuses_empty_ai_train_version(validator: _Validator) -> None:
    assert validator.validate("gateway-reply", {**_REPLY_MIN, "ai_train_version": ""}) != []


# --- gateway-reply Python type parity ---------------------------------------


def test_gateway_reply_schema_required_matches_reply_non_default_fields() -> None:
    non_default = {
        f.name
        for f in dataclasses.fields(Reply)
        if f.default is dataclasses.MISSING
        and f.default_factory is dataclasses.MISSING
    }
    schema_required = set(_load("gateway-reply")["required"])
    assert schema_required == non_default


def test_gateway_reply_schema_properties_match_reply_fields() -> None:
    python_fields = {f.name for f in dataclasses.fields(Reply)}
    schema_props = set(_load("gateway-reply")["properties"])
    assert python_fields == schema_props


# --- coverage sentinel -------------------------------------------------------


def test_every_ai_schema_file_is_in_all_schemas() -> None:
    on_disk = {p.stem.removesuffix(".schema") for p in _SCHEMAS.glob("*.schema.json")}
    assert on_disk == set(_ALL_SCHEMAS), (
        "schema files not in _ALL_SCHEMAS: "
        + str(on_disk - set(_ALL_SCHEMAS))
        + " | _ALL_SCHEMAS entries with no file: "
        + str(set(_ALL_SCHEMAS) - on_disk)
    )
