"""Keep the Python Gateway wire models and codecs aligned with the AI schemas."""

from __future__ import annotations

import base64
import dataclasses
import json
from pathlib import Path
from typing import Any, get_type_hints

import pytest

from test_ai_schemas import _Validator
from ztax_gateway.provenance import AuthorityOutcome, RiskTier
from ztax_gateway.wire import Call, Governance, Kind, Reply, WireError, decode_call, encode_reply

_REPO = Path(__file__).resolve().parents[2]
_SCHEMAS = _REPO / "contracts" / "schemas" / "ai"


def _load_schema(name: str) -> dict[str, Any]:
    with (_SCHEMAS / f"{name}.schema.json").open(encoding="utf-8") as file:
        result: dict[str, Any] = json.load(file)
    return result


_CALL: dict[str, Any] = {
    "kind": "SUGGESTION",
    "governance": {
        "tenant_id": "0190f3a2-1b2c-7d3e-8f40-5a6b7c8d9e0f",
        "use_case": "classification-review",
        "authority_outcome": "A1",
        "risk_tier": "T1",
        "region": "euc1-dev-01",
        "data_classes": ["P0", "P1"],
    },
    "subject_ref": "sku:PLAN-5G",
    "input_b64": base64.b64encode(b'{"sku":"PLAN-5G"}').decode("ascii"),
}


def test_kind_values_match_call_schema_exactly() -> None:
    schema_values = _load_schema("gateway-call")["properties"]["kind"]["enum"]
    wire_values = [kind.value for kind in Kind]
    assert wire_values == schema_values, (
        f"kind enum mismatch: schema={schema_values!r}; wire.Kind={wire_values!r}"
    )
    assert set(wire_values) == {
        "SUGGESTION",
        "EXTRACTION",
        "CLASSIFICATION_PROPOSAL",
    }


def test_call_and_governance_dataclass_fields_and_types_match_schema() -> None:
    schema = _load_schema("gateway-call")
    call_properties = schema["properties"]
    governance_schema = call_properties["governance"]

    # `input_b64` is a wire field. decode_call base64-decodes it into Call.input.
    call_mapping = {"input_b64": "input"}
    expected_call_types = {
        "kind": Kind,
        "governance": Governance,
        "subject_ref": str,
        "input_b64": bytes,
    }
    expected_governance_types = {
        "tenant_id": str,
        "use_case": str,
        "authority_outcome": AuthorityOutcome,
        "risk_tier": RiskTier,
        "region": str,
        "data_classes": tuple[str, ...],
    }
    mismatches: list[str] = []

    call_fields = {field.name for field in dataclasses.fields(Call)}
    mapped_call_fields = {
        call_mapping.get(name, name) for name in call_properties
    }
    if call_fields != mapped_call_fields:
        mismatches.append(
            f"Call fields: schema maps to {sorted(mapped_call_fields)!r}, "
            f"dataclass has {sorted(call_fields)!r}"
        )
    call_hints = get_type_hints(Call)
    for schema_name, expected_type in expected_call_types.items():
        python_name = call_mapping.get(schema_name, schema_name)
        if call_hints.get(python_name) != expected_type:
            mismatches.append(
                f"Call.{python_name}: schema field {schema_name!r} maps to "
                f"{expected_type!r}, annotation is {call_hints.get(python_name)!r}"
            )

    governance_fields = {field.name for field in dataclasses.fields(Governance)}
    schema_governance_fields = set(governance_schema["properties"])
    if governance_fields != schema_governance_fields:
        mismatches.append(
            f"Governance fields: schema has {sorted(schema_governance_fields)!r}, "
            f"dataclass has {sorted(governance_fields)!r}"
        )
    governance_hints = get_type_hints(Governance)
    for name, expected_type in expected_governance_types.items():
        if governance_hints.get(name) != expected_type:
            mismatches.append(
                f"Governance.{name}: schema representation maps to {expected_type!r}, "
                f"annotation is {governance_hints.get(name)!r}"
            )

    assert set(schema["required"]) == set(call_properties)
    assert set(governance_schema["required"]) == schema_governance_fields
    assert mismatches == [], "Contract mismatches:\n" + "\n".join(mismatches)


def test_input_b64_is_a_wire_to_internal_mapping_not_a_name_mismatch() -> None:
    schema = _load_schema("gateway-call")
    call = decode_call(json.dumps(_CALL).encode("utf-8"))

    assert "input_b64" in schema["required"]
    assert "input_b64" not in {field.name for field in dataclasses.fields(Call)}
    assert "input" in {field.name for field in dataclasses.fields(Call)}
    assert call.input == b'{"sku":"PLAN-5G"}'

    wrong_name = {key: value for key, value in _CALL.items() if key != "input_b64"}
    wrong_name["input"] = _CALL["input_b64"]
    with pytest.raises(WireError):
        decode_call(json.dumps(wrong_name).encode("utf-8"))


def test_decode_call_accepts_schema_valid_types_and_returns_typed_models() -> None:
    validator = _Validator()
    assert validator.validate("gateway-call", _CALL) == []

    call = decode_call(json.dumps(_CALL).encode("utf-8"))
    assert isinstance(call, Call)
    assert isinstance(call.kind, Kind)
    assert isinstance(call.governance, Governance)
    assert isinstance(call.subject_ref, str)
    assert isinstance(call.input, bytes)
    assert isinstance(call.governance.tenant_id, str)
    assert isinstance(call.governance.use_case, str)
    assert isinstance(call.governance.authority_outcome, AuthorityOutcome)
    assert isinstance(call.governance.risk_tier, RiskTier)
    assert isinstance(call.governance.region, str)
    assert isinstance(call.governance.data_classes, tuple)
    assert all(isinstance(item, str) for item in call.governance.data_classes)


def test_decode_call_requires_exact_schema_fields_and_field_types() -> None:
    schema = _load_schema("gateway-call")
    governance_required = schema["properties"]["governance"]["required"]
    mismatches: list[str] = []

    for name in schema["required"]:
        invalid = dict(_CALL)
        del invalid[name]
        try:
            decode_call(json.dumps(invalid).encode("utf-8"))
        except WireError:
            pass
        else:
            mismatches.append(f"decode_call accepted call missing required field {name!r}")

    for name in governance_required:
        invalid_governance = dict(_CALL["governance"])
        del invalid_governance[name]
        invalid = {**_CALL, "governance": invalid_governance}
        try:
            decode_call(json.dumps(invalid).encode("utf-8"))
        except WireError:
            pass
        else:
            mismatches.append(
                f"decode_call accepted governance missing required field {name!r}"
            )

    invalid_types: tuple[tuple[str, object], ...] = (
        ("kind", 1),
        ("governance", []),
        ("subject_ref", 1),
        ("input_b64", 1),
    )
    for name, value in invalid_types:
        invalid = {**_CALL, name: value}
        try:
            decode_call(json.dumps(invalid).encode("utf-8"))
        except WireError:
            pass
        else:
            mismatches.append(
                f"decode_call accepted wrong type for {name!r}: {type(value).__name__}"
            )

    invalid = {**_CALL, "extra": "not in the schema"}
    try:
        decode_call(json.dumps(invalid).encode("utf-8"))
    except WireError:
        pass
    else:
        mismatches.append("decode_call accepted an unknown top-level field")

    assert mismatches == [], "Codec mismatches:\n" + "\n".join(mismatches)


def test_reply_dataclass_fields_and_types_match_schema() -> None:
    schema = _load_schema("gateway-reply")
    schema_properties = set(schema["properties"])
    reply_fields = {field.name: field for field in dataclasses.fields(Reply)}
    type_hints = get_type_hints(Reply)
    required_fields = {
        name
        for name, field in reply_fields.items()
        if field.default is dataclasses.MISSING
        and field.default_factory is dataclasses.MISSING
    }
    expected_types = {
        "model_profile": str,
        "provider_profile": str,
        "prompt_profile": str,
        "ai_train_version": str,
        "text": str,
        "payload": str,
        "fields": dict[str, str],
        "proposed_code": str,
        "confidence": str,
    }
    mismatches: list[str] = []

    if set(reply_fields) != schema_properties:
        mismatches.append(
            f"Reply fields: schema has {sorted(schema_properties)!r}, "
            f"dataclass has {sorted(reply_fields)!r}"
        )
    if required_fields != set(schema["required"]):
        mismatches.append(
            f"Reply required fields: schema has {sorted(schema['required'])!r}, "
            f"dataclass requires {sorted(required_fields)!r}"
        )
    for name, expected_type in expected_types.items():
        if type_hints.get(name) != expected_type:
            mismatches.append(
                f"Reply.{name}: schema representation maps to {expected_type!r}, "
                f"annotation is {type_hints.get(name)!r}"
            )

    assert mismatches == [], "Contract mismatches:\n" + "\n".join(mismatches)


def test_encode_reply_produces_exact_schema_fields_and_types() -> None:
    validator = _Validator()
    reply = Reply(
        model_profile="model:cls-2027.03",
        provider_profile="provider:eu-hosted",
        prompt_profile="prompt:cls-v4",
        ai_train_version="ai-2027.03.1",
        text="Suggested tax treatment.",
        payload='{"detail":"standard rate"}',
        fields={"invoice_no": "INV-100", "vat_rate": "0.20"},
        proposed_code="ontology:telecom/voice/mobile",
        confidence="0.95",
    )
    encoded = encode_reply(reply)
    document: dict[str, Any] = json.loads(encoded)
    schema = _load_schema("gateway-reply")
    mismatches: list[str] = []

    if set(document) != set(schema["properties"]):
        mismatches.append(
            f"encode_reply keys: schema properties are "
            f"{sorted(schema['properties'])!r}; encoder produced {sorted(document)!r}"
        )
    for name, value in document.items():
        if name == "fields":
            if not isinstance(value, dict) or not all(
                isinstance(key, str) and isinstance(item, str)
                for key, item in value.items()
            ):
                mismatches.append("encoded 'fields' is not an object of string values")
        elif not isinstance(value, str):
            mismatches.append(
                f"encoded {name!r} has type {type(value).__name__}, expected string"
            )
    schema_errors = validator.validate("gateway-reply", document)
    if schema_errors:
        mismatches.extend(f"schema validation: {error}" for error in schema_errors)

    assert mismatches == [], "Codec mismatches:\n" + "\n".join(mismatches)


def test_decode_call_rejects_dict_inside_data_classes() -> None:
    """A dict element in data_classes must raise WireError, not TypeError.

    Before the isinstance(c, str) guard in wire.py, passing a dict caused an
    unhashable-type crash that escaped the WireError catch-block in server.invoke().
    This test pins that the guard is present and surfaces the error correctly.
    """
    bad = {**_CALL, "governance": {**_CALL["governance"], "data_classes": [{"P0": True}]}}
    with pytest.raises(WireError, match="is not a string"):
        decode_call(json.dumps(bad).encode("utf-8"))


def test_decode_call_rejects_identifier_over_128_chars() -> None:
    """use_case and region must not exceed 128 chars (common.schema.json#/$defs/identifier).

    The canonical schema defines maxLength:128 for all identifiers. Go validates
    this via JSON Schema before it sends; Python must enforce the same limit on
    decode so that any client bypassing Go is also rejected at the wire boundary.
    """
    too_long = "x" * 129

    bad_use_case = {**_CALL, "governance": {**_CALL["governance"], "use_case": too_long}}
    with pytest.raises(WireError, match=r"governance\.use_case.*128"):
        decode_call(json.dumps(bad_use_case).encode("utf-8"))

    bad_region = {**_CALL, "governance": {**_CALL["governance"], "region": too_long}}
    with pytest.raises(WireError, match=r"governance\.region.*128"):
        decode_call(json.dumps(bad_region).encode("utf-8"))
