"""Strict loader for a prompt-profile file.

The file is a JSON object ``{"profiles": [ ... ]}``. Each profile carries the
fields of :class:`~ztax_gateway.production_registries.PromptProfile`. The
content hash is computed from the prompt text by ``PromptProfile.content_hash``
and is never read from the file: a ``content_hash`` key is accepted only so an
exported file still loads, and its value is ignored.

Errors name fields and profile positions only. They never echo prompt text,
file content or key names from the file. Every error is raised OUTSIDE the
``except`` block that caught the parser's or the enum's exception (a flag is
set inside it), so ``__cause__`` and ``__context__`` are both ``None``:
``from None`` alone would leave the parser exception, which can quote the
whole file, reachable as ``__context__``.
"""

from __future__ import annotations

import contextlib
import json
from pathlib import Path
from typing import Any, Final

from .production_gates import PromptProfileRegistry
from .production_registries import PromptProfile, PromptProfileError
from .provenance import AuthorityOutcome, RiskTier

__all__: list[str] = ["LoadedPromptProfiles", "PromptProfileFileError", "load_prompt_profiles"]


class PromptProfileFileError(ValueError):
    """The prompt-profile file is missing or invalid. The message never quotes the file."""


class LoadedPromptProfiles(PromptProfileRegistry):
    """A :class:`PromptProfileRegistry` that can also list the ids it holds."""

    def profile_ids(self) -> frozenset[str]:
        return frozenset(self._profiles)


_TOP_KEYS: Final[frozenset[str]] = frozenset({"profiles"})
_REQUIRED: Final[frozenset[str]] = frozenset(
    {
        "profile_id",
        "owner",
        "version",
        "system_prompt",
        "max_risk_tier",
        "authority_ceiling",
        "safety_rules",
    }
)
# content_hash: accepted and ignored (the hash is always recomputed).
_OPTIONAL: Final[frozenset[str]] = frozenset(
    {"variable_schema", "output_schema", "deprecated", "content_hash"}
)


def _fail(message: str) -> PromptProfileFileError:
    return PromptProfileFileError(f"prompt profile file: {message}")


def _text(entry: dict[str, Any], key: str, where: str) -> str:
    value = entry[key]
    if not isinstance(value, str) or not value.strip():
        raise _fail(f"{where}: field {key!r} must be a non-empty string")
    return value


def _object(entry: dict[str, Any], key: str, where: str) -> dict[str, object]:
    value = entry.get(key, {})
    if not isinstance(value, dict):
        raise _fail(f"{where}: field {key!r} must be an object")
    return value


def _parse_profile(entry: object, index: int) -> PromptProfile:
    where = f"profile {index}"
    if not isinstance(entry, dict):
        raise _fail(f"{where} is not an object")
    unknown = set(entry) - _REQUIRED - _OPTIONAL
    if unknown:
        raise _fail(f"{where} has {len(unknown)} unknown key(s)")
    missing = _REQUIRED - set(entry)
    if missing:
        raise _fail(f"{where} lacks field(s) {sorted(missing)}")

    rules = entry["safety_rules"]
    if not isinstance(rules, list) or not all(isinstance(r, str) for r in rules):
        raise _fail(f"{where}: field 'safety_rules' must be a list of strings")
    deprecated = entry.get("deprecated", False)
    if not isinstance(deprecated, bool):
        raise _fail(f"{where}: field 'deprecated' must be a boolean")
    profile_id = _text(entry, "profile_id", where)
    owner = _text(entry, "owner", where)
    version = _text(entry, "version", where)
    system_prompt = _text(entry, "system_prompt", where)
    variable_schema = _object(entry, "variable_schema", where)
    output_schema = _object(entry, "output_schema", where)
    tier_text = _text(entry, "max_risk_tier", where)
    authority_text = _text(entry, "authority_ceiling", where)

    # The exception is swallowed here and the raise happens after the block,
    # so the new error carries no __context__ (the enum's text quotes the value).
    risk: RiskTier | None = None
    authority: AuthorityOutcome | None = None
    with contextlib.suppress(ValueError):
        risk = RiskTier(tier_text)
    with contextlib.suppress(ValueError):
        authority = AuthorityOutcome(authority_text)
    if risk is None or authority is None:
        raise _fail(f"{where}: field 'max_risk_tier' or 'authority_ceiling' is not valid")

    profile: PromptProfile | None = None
    # Its message names the profile id; the file's errors name positions only.
    with contextlib.suppress(PromptProfileError):
        profile = PromptProfile(
            profile_id=profile_id,
            owner=owner,
            version=version,
            system_prompt=system_prompt,
            variable_schema=variable_schema,
            output_schema=output_schema,
            max_risk_tier=risk,
            authority_ceiling=authority,
            safety_rules=tuple(rules),
            deprecated=deprecated,
        )
    if profile is None:
        raise _fail(f"{where} was refused by the prompt profile rules")
    return profile


def load_prompt_profiles(path: Path) -> LoadedPromptProfiles:
    """Load *path* into a :class:`PromptProfileRegistry`, or raise.

    Rejects: a missing or unreadable file, invalid JSON, unknown keys, a
    missing field, a duplicate ``profile_id`` and an empty prompt.
    """
    raw: bytes | None = None
    with contextlib.suppress(OSError):
        raw = path.read_bytes()
    if raw is None:
        raise _fail("the file is missing or cannot be read")
    doc: Any = None
    parsed = False
    with contextlib.suppress(ValueError, RecursionError):
        doc = json.loads(raw)
        parsed = True
    if not parsed:
        raise _fail("the file is not valid JSON")
    if not isinstance(doc, dict):
        raise _fail("the top level is not an object")
    unknown = set(doc) - _TOP_KEYS
    if unknown:
        raise _fail(f"the top level has {len(unknown)} unknown key(s)")
    profiles = doc.get("profiles")
    if not isinstance(profiles, list) or not profiles:
        raise _fail("field 'profiles' must be a non-empty list")

    registry = LoadedPromptProfiles()
    seen: set[str] = set()
    for index, entry in enumerate(profiles):
        profile = _parse_profile(entry, index)
        if profile.profile_id in seen:
            raise _fail(f"profile {index} repeats a profile_id")
        seen.add(profile.profile_id)
        registry.register(profile)
    return registry
