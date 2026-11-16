"""Tests for self-hosted model/provider profile draft files (S6-10 / Prompt B).

Verifies that the draft files in config/ follow the required schema corresponding
to ModelProfile and ProviderProfile dataclasses, remain strictly in DRAFT status,
have no real values, and are not referenced by production or runtime code.
"""

from __future__ import annotations

import dataclasses
import json
from pathlib import Path

from ztax_gateway.production_registries import ModelProfile, ProviderProfile

_INTELLIGENCE_ROOT = Path(__file__).resolve().parent.parent
_CONFIG_DIR = _INTELLIGENCE_ROOT / "config"
_PROVIDER_DRAFT_PATH = _CONFIG_DIR / "provider_profile.self-hosted.draft.json"
_MODEL_DRAFT_PATH = _CONFIG_DIR / "model_profile.self-hosted.draft.json"
_LICENCE_NOTE_PATH = _CONFIG_DIR / "SELF_HOSTED_LICENCE_NOTE.draft.md"


def test_provider_profile_draft_structure() -> None:
    """Verify provider draft JSON matches ProviderProfile dataclass fields + metadata."""
    assert _PROVIDER_DRAFT_PATH.is_file(), f"Missing {_PROVIDER_DRAFT_PATH}"
    data = json.loads(_PROVIDER_DRAFT_PATH.read_text(encoding="utf-8"))

    # Draft metadata
    assert data["status"] == "DRAFT"
    assert isinstance(data.get("purpose"), str) and len(data["purpose"]) > 0
    assert data["suspended"] is True
    assert data["provider_profile_id"] == "provider:self-hosted-draft"

    # Dataclass fields parity
    expected_dataclass_fields = {f.name for f in dataclasses.fields(ProviderProfile)}
    expected_all_keys = expected_dataclass_fields | {"status", "purpose", "draft_extras"}
    assert set(data.keys()) == expected_all_keys

    # draft_extras structure
    extras = data.get("draft_extras")
    assert isinstance(extras, dict)
    assert set(extras.keys()) == {
        "weights_sha256",
        "weights_scan_result",
        "licence_name",
        "licence_url",
    }
    assert extras["weights_sha256"] is None
    assert extras["weights_scan_result"] is None
    assert extras["licence_name"] is None
    assert extras["licence_url"] is None

    # All unknown dataclass fields must be null (None)
    for field_name in expected_dataclass_fields - {"provider_profile_id", "suspended"}:
        assert data[field_name] is None, f"Field {field_name} must be null in draft"


def test_model_profile_draft_structure() -> None:
    """Verify model draft JSON matches ModelProfile dataclass fields + metadata."""
    assert _MODEL_DRAFT_PATH.is_file(), f"Missing {_MODEL_DRAFT_PATH}"
    data = json.loads(_MODEL_DRAFT_PATH.read_text(encoding="utf-8"))

    # Draft metadata
    assert data["status"] == "DRAFT"
    assert isinstance(data.get("purpose"), str) and len(data["purpose"]) > 0
    assert data["suspended"] is True
    assert data["model_profile_id"] == "model:self-hosted-draft"

    # Dataclass fields parity
    expected_dataclass_fields = {f.name for f in dataclasses.fields(ModelProfile)}
    expected_all_keys = expected_dataclass_fields | {"status", "purpose", "draft_extras"}
    assert set(data.keys()) == expected_all_keys

    # draft_extras structure
    extras = data.get("draft_extras")
    assert isinstance(extras, dict)
    assert set(extras.keys()) == {
        "weights_sha256",
        "weights_scan_result",
        "licence_name",
        "licence_url",
    }
    assert extras["weights_sha256"] is None
    assert extras["weights_scan_result"] is None
    assert extras["licence_name"] is None
    assert extras["licence_url"] is None

    # All unknown dataclass fields must be null (None)
    for field_name in expected_dataclass_fields - {"model_profile_id", "suspended"}:
        assert data[field_name] is None, f"Field {field_name} must be null in draft"


def test_no_real_values_in_drafts() -> None:
    """Ensure no real-looking model name, hash, URL, or region appears in drafts."""
    provider_data = json.loads(_PROVIDER_DRAFT_PATH.read_text(encoding="utf-8"))
    model_data = json.loads(_MODEL_DRAFT_PATH.read_text(encoding="utf-8"))

    # Provider draft checks
    assert provider_data["legal_entity"] is None
    assert provider_data["permitted_regions"] is None
    assert provider_data["api_hosts"] is None
    assert provider_data["dpa_ref"] is None
    assert provider_data["draft_extras"]["licence_url"] is None
    assert provider_data["draft_extras"]["weights_sha256"] is None

    # Model draft checks
    assert model_data["model_id"] is None
    assert model_data["model_version"] is None
    assert model_data["permitted_regions"] is None
    assert model_data["terms_ref"] is None
    assert model_data["draft_extras"]["licence_url"] is None
    assert model_data["draft_extras"]["weights_sha256"] is None


def test_no_code_references_or_uses_draft_profiles() -> None:
    """Ensure no python code in src/ or tools/ references self-hosted.draft, nor do registries."""
    src_dir = _INTELLIGENCE_ROOT / "src"
    tools_dir = _INTELLIGENCE_ROOT / "tools"

    checked_py_files: list[Path] = []
    for directory in (src_dir, tools_dir):
        if directory.is_dir():
            checked_py_files.extend(directory.rglob("*.py"))

    assert len(checked_py_files) > 0, "No python files found to inspect"

    for py_file in checked_py_files:
        content = py_file.read_text(encoding="utf-8", errors="replace")
        assert "self-hosted.draft" not in content, (
            f"Found forbidden reference 'self-hosted.draft' in {py_file}"
        )

    # Check registry JSON files
    for reg_name in ("registry.live-demo.json", "registry.dev.json"):
        reg_path = _CONFIG_DIR / reg_name
        if reg_path.is_file():
            reg_content = reg_path.read_text(encoding="utf-8")
            assert "self-hosted.draft" not in reg_content, (
                f"Found forbidden reference 'self-hosted.draft' in {reg_path}"
            )


def test_licence_note_contents() -> None:
    """Verify SELF_HOSTED_LICENCE_NOTE.draft.md contains required notices and disclaimers."""
    assert _LICENCE_NOTE_PATH.is_file(), f"Missing {_LICENCE_NOTE_PATH}"
    text = _LICENCE_NOTE_PATH.read_text(encoding="utf-8")

    assert "TO CONFIRM" in text
    assert "not an approval" in text
    assert "Kaushal" in text
    assert "legal advice" in text
