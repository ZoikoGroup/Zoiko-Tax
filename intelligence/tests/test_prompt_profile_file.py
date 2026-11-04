"""The strict prompt-profile loader: every rejection, and no prompt text in errors."""

from __future__ import annotations

import copy
import hashlib
import json
import logging
import traceback
from pathlib import Path
from typing import Any

import pytest

from ztax_gateway.prompt_profile_file import PromptProfileFileError, load_prompt_profiles

_MARKER = "PROMPT-MARKER-4c9e1f"
_PROFILE: dict[str, Any] = {
    "profile_id": "prompt:test-v1",
    "owner": "lane-l",
    "version": "1.0.0",
    "system_prompt": f"You are a test assistant. {_MARKER}",
    "max_risk_tier": "T1",
    "authority_ceiling": "A1",
    "safety_rules": ["Demo only."],
}


def _write(tmp_path: Path, doc: Any) -> Path:
    path = tmp_path / "profiles.json"
    path.write_text(doc if isinstance(doc, str) else json.dumps(doc), encoding="utf-8")
    return path


def _profile(**overrides: Any) -> dict[str, Any]:
    p = copy.deepcopy(_PROFILE)
    p.update(overrides)
    return p


def test_a_valid_file_loads_and_the_hash_is_computed_from_the_text(tmp_path: Path) -> None:
    registry = load_prompt_profiles(_write(tmp_path, {"profiles": [_profile()]}))
    profile = registry.get("prompt:test-v1")
    assert profile is not None
    assert profile.content_hash == hashlib.sha256(_PROFILE["system_prompt"].encode()).hexdigest()
    assert registry.profile_ids() == frozenset({"prompt:test-v1"})


def test_a_hash_in_the_file_is_ignored(tmp_path: Path) -> None:
    forged = "0" * 64
    registry = load_prompt_profiles(_write(tmp_path, {"profiles": [_profile(content_hash=forged)]}))
    profile = registry.get("prompt:test-v1")
    assert profile is not None
    assert profile.content_hash != forged
    assert profile.content_hash == hashlib.sha256(_PROFILE["system_prompt"].encode()).hexdigest()


def test_the_shipped_live_demo_file_loads() -> None:
    path = Path(__file__).resolve().parent.parent / "config" / "prompt_profiles.live-demo.json"
    assert len(load_prompt_profiles(path).profile_ids()) == 4


def _rejections() -> list[tuple[str, Any]]:
    return [
        ("invalid-json", "{not json " + _MARKER),
        ("not-an-object", "[]"),
        ("unknown-top-key", {"profiles": [_profile()], "extra": 1}),
        ("no-profiles", {"profiles": []}),
        ("profiles-not-a-list", {"profiles": {}}),
        ("profile-not-an-object", {"profiles": ["x"]}),
        ("unknown-profile-key", {"profiles": [_profile(surprise=_MARKER)]}),
        ("marker-in-unknown-profile-key", {"profiles": [_profile(**{_MARKER: 1})]}),
        ("marker-in-unknown-top-level-key", {"profiles": [_profile()], _MARKER: 1}),
        ("marker-in-tier-value", {"profiles": [_profile(max_risk_tier=_MARKER)]}),
        ("marker-in-authority-value", {"profiles": [_profile(authority_ceiling=_MARKER)]}),
        ("missing-field", {"profiles": [{k: v for k, v in _PROFILE.items() if k != "owner"}]}),
        ("duplicate-id", {"profiles": [_profile(), _profile()]}),
        ("empty-prompt", {"profiles": [_profile(system_prompt="")]}),
        ("blank-prompt", {"profiles": [_profile(system_prompt="   \n ")]}),
        ("prompt-not-a-string", {"profiles": [_profile(system_prompt=5)]}),
        ("bad-tier", {"profiles": [_profile(max_risk_tier="T9")]}),
        ("a5-authority", {"profiles": [_profile(authority_ceiling="A5")]}),
        ("rules-not-a-list", {"profiles": [_profile(safety_rules="x")]}),
        ("empty-owner", {"profiles": [_profile(owner="")]}),
    ]


_CASES = _rejections()
_IDS = [name for name, _ in _CASES]


@pytest.mark.parametrize(("name", "doc"), _CASES, ids=_IDS)
def test_every_bad_file_is_rejected(tmp_path: Path, name: str, doc: Any) -> None:
    with pytest.raises(PromptProfileFileError):
        load_prompt_profiles(_write(tmp_path, doc))


def test_a_missing_file_is_rejected(tmp_path: Path) -> None:
    with pytest.raises(PromptProfileFileError):
        load_prompt_profiles(tmp_path / "absent.json")


@pytest.mark.parametrize(("name", "doc"), _CASES, ids=_IDS)
def test_a_marker_in_a_bad_file_never_reaches_the_error_or_the_logs(
    tmp_path: Path, caplog: pytest.LogCaptureFixture, name: str, doc: Any
) -> None:
    # The marker sits inside a prompt (or in the raw text) of every rejected document.
    with caplog.at_level(logging.DEBUG), pytest.raises(PromptProfileFileError) as info:
        load_prompt_profiles(_write(tmp_path, doc))
    err = info.value
    shown = "".join(traceback.format_exception(err))
    assert _MARKER not in str(err)
    assert _MARKER not in repr(err)
    assert _MARKER not in shown
    assert _MARKER not in repr(err.args)
    # "from None" only suppresses the display: the parser's or the enum's
    # exception (which quotes the file or the value) must not be reachable at all.
    assert err.__cause__ is None
    assert err.__context__ is None
    assert _MARKER not in caplog.text
