"""The wire form of a Gateway call and its reply.

The messages are ``contracts/schemas/ai/gateway-call.schema.json`` and
``gateway-reply.schema.json`` — the single schema authority ADR-0006 §2.2
requires — carried as JSON over gRPC (intelligence/README.md, "The transport").
``tests/test_server.py`` validates what this module produces and accepts
against those schemas, and the Go half
(``backend/internal/adapter/gateway/grpc.go``) is tested against the same
files.

Two rules make the decoder strict rather than forgiving:

* **No JSON numbers.** ``json.loads`` turns a number into a float, silently,
  which is the failure ``decimal_wire`` exists to stop (ADR-0006 §2.3). Every
  value on this wire is a string, so a number is refused at parse time rather
  than tolerated.
* **No unknown fields.** A field this build does not know was written against
  a different contract, and acting on a message half-understood is how a
  governance field gets ignored.
"""

from __future__ import annotations

import base64
import binascii
import json
import re
from dataclasses import dataclass, field
from enum import StrEnum
from typing import Any

from .provenance import AuthorityOutcome, RiskTier


class WireError(ValueError):
    """A message that is not a valid call."""


class Kind(StrEnum):
    """Which advisory record a call produces (Go: gateway.Kind)."""

    SUGGESTION = "SUGGESTION"
    EXTRACTION = "EXTRACTION"
    CLASSIFICATION_PROPOSAL = "CLASSIFICATION_PROPOSAL"


_PRIVACY_CLASSES = frozenset(f"P{i}" for i in range(8))
_UUID = re.compile(r"^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$")
_CONFIDENCE = re.compile(r"^$|^(0(\.[0-9]+)?|1(\.0+)?)$")


@dataclass(frozen=True)
class Governance:
    """The governance context the caller sent."""

    tenant_id: str
    use_case: str
    authority_outcome: AuthorityOutcome
    risk_tier: RiskTier
    region: str
    data_classes: tuple[str, ...]

    def highest_data_class(self) -> str:
        """The most sensitive class present, as the Go side records it."""
        return max(self.data_classes, key=lambda c: int(c[1:]))


@dataclass(frozen=True)
class Call:
    """One decoded call."""

    kind: Kind
    governance: Governance
    subject_ref: str
    input: bytes


@dataclass(frozen=True)
class Reply:
    """One reply. The routing fields are required; the content fields are
    those of the call's kind."""

    model_profile: str
    provider_profile: str
    prompt_profile: str
    ai_train_version: str
    text: str = ""
    payload: str = ""
    fields: dict[str, str] = field(default_factory=dict)
    proposed_code: str = ""
    confidence: str = ""


def _refuse_number(value: str) -> Any:
    raise WireError(f"wire: a JSON number ({value}) where a string is required")


def _object(
    value: Any, where: str, required: set[str], optional: frozenset[str] = frozenset()
) -> dict[str, Any]:
    if not isinstance(value, dict):
        raise WireError(f"wire: {where} is not an object")
    unknown = set(value) - required - optional
    if unknown:
        raise WireError(f"wire: {where} carries unknown field(s) {sorted(unknown)}")
    missing = required - set(value)
    if missing:
        raise WireError(f"wire: {where} lacks {sorted(missing)}")
    return value


def _string(value: Any, where: str) -> str:
    if not isinstance(value, str):
        raise WireError(f"wire: {where} is not a string")
    return value


def decode_call(data: bytes) -> Call:
    """Decode and validate one call, or raise ``WireError``."""
    try:
        doc = json.loads(
            data,
            parse_float=_refuse_number,
            parse_int=_refuse_number,
            parse_constant=_refuse_number,
        )
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise WireError(f"wire: not JSON: {exc}") from exc
    top = _object(doc, "call", {"kind", "governance", "subject_ref", "input_b64"})
    try:
        kind = Kind(_string(top["kind"], "kind"))
    except ValueError as exc:
        raise WireError(f"wire: kind {top['kind']!r}") from exc

    g = _object(
        top["governance"],
        "governance",
        {"tenant_id", "use_case", "authority_outcome", "risk_tier", "region", "data_classes"},
    )
    tenant = _string(g["tenant_id"], "governance.tenant_id")
    if not _UUID.match(tenant):
        raise WireError("wire: governance.tenant_id is not a canonical UUID")
    try:
        authority = AuthorityOutcome(
            _string(g["authority_outcome"], "governance.authority_outcome")
        )
        risk = RiskTier(_string(g["risk_tier"], "governance.risk_tier"))
    except ValueError as exc:
        raise WireError(f"wire: {exc}") from exc
    classes = g["data_classes"]
    if not isinstance(classes, list) or not classes:
        raise WireError("wire: governance.data_classes must name at least one class")
    for c in classes:
        if c not in _PRIVACY_CLASSES:
            raise WireError(f"wire: data class {c!r} is not a PRIV-001 class")
    if len(set(classes)) != len(classes):
        raise WireError("wire: governance.data_classes repeats a class")

    subject = _string(top["subject_ref"], "subject_ref")
    if len(subject) > 512:
        raise WireError("wire: subject_ref is longer than 512")
    try:
        raw = base64.b64decode(_string(top["input_b64"], "input_b64"), validate=True)
    except binascii.Error as exc:
        raise WireError("wire: input_b64 is not base64") from exc

    return Call(
        kind=kind,
        governance=Governance(
            tenant_id=tenant,
            use_case=_string(g["use_case"], "governance.use_case"),
            authority_outcome=authority,
            risk_tier=risk,
            region=_string(g["region"], "governance.region"),
            data_classes=tuple(classes),
        ),
        subject_ref=subject,
        input=raw,
    )


def encode_reply(reply: Reply) -> bytes:
    """Encode one reply. Refuses a reply that could not be evidenced."""
    for name in ("model_profile", "provider_profile", "prompt_profile", "ai_train_version"):
        if not getattr(reply, name):
            raise WireError(f"wire: reply has no {name}; an unevidenced reply is never sent")
    if not _CONFIDENCE.match(reply.confidence):
        raise WireError(
            f"wire: confidence {reply.confidence!r} is not a canonical decimal in [0, 1]"
        )
    doc: dict[str, Any] = {
        "model_profile": reply.model_profile,
        "provider_profile": reply.provider_profile,
        "prompt_profile": reply.prompt_profile,
        "ai_train_version": reply.ai_train_version,
    }
    if reply.text:
        doc["text"] = reply.text
    if reply.payload:
        doc["payload"] = reply.payload
    if reply.fields:
        doc["fields"] = dict(sorted(reply.fields.items()))
    if reply.proposed_code:
        doc["proposed_code"] = reply.proposed_code
    if reply.confidence:
        doc["confidence"] = reply.confidence
    return json.dumps(doc, sort_keys=True, separators=(",", ":")).encode("utf-8")
