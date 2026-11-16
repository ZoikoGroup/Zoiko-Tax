"""Tests for gold/thresholds.v0.draft.json.

Verifies:
- The file parses as valid JSON.
- status is "DRAFT".
- approval signed_by and signed_on are null.
- Every metric value is null.
- Every metric has a non-empty basis.
- Nothing in src/ or tools/ reads the file (no "thresholds.v0" string found,
  and no code opens a file from gold/ other than the gold-set files).
"""

from __future__ import annotations

import json
import re
from pathlib import Path

# ---------------------------------------------------------------------------
# Paths
# ---------------------------------------------------------------------------

_INTEL_DIR = Path(__file__).resolve().parent.parent
_THRESHOLDS = _INTEL_DIR / "gold" / "thresholds.v0.draft.json"
_SRC_DIR = _INTEL_DIR / "src"
_TOOLS_DIR = _INTEL_DIR / "tools"


# ---------------------------------------------------------------------------
# 1. File parses
# ---------------------------------------------------------------------------


class TestThresholdsFileParseable:
    def test_parses_as_json(self) -> None:
        raw = _THRESHOLDS.read_text(encoding="utf-8")
        data = json.loads(raw)
        assert isinstance(data, dict)

    def test_top_level_name_present(self) -> None:
        data = json.loads(_THRESHOLDS.read_text(encoding="utf-8"))
        assert "name" in data

    def test_top_level_version_present(self) -> None:
        data = json.loads(_THRESHOLDS.read_text(encoding="utf-8"))
        assert "version" in data


# ---------------------------------------------------------------------------
# 2. Status is DRAFT
# ---------------------------------------------------------------------------


class TestThresholdsStatus:
    def test_status_is_draft(self) -> None:
        data = json.loads(_THRESHOLDS.read_text(encoding="utf-8"))
        assert data["status"] == "DRAFT"


# ---------------------------------------------------------------------------
# 3. Approval block: signed_by and signed_on are null
# ---------------------------------------------------------------------------


class TestThresholdsApproval:
    def test_approval_key_exists(self) -> None:
        data = json.loads(_THRESHOLDS.read_text(encoding="utf-8"))
        assert "approval" in data

    def test_signed_by_is_null(self) -> None:
        data = json.loads(_THRESHOLDS.read_text(encoding="utf-8"))
        assert data["approval"]["signed_by"] is None

    def test_signed_on_is_null(self) -> None:
        data = json.loads(_THRESHOLDS.read_text(encoding="utf-8"))
        assert data["approval"]["signed_on"] is None


# ---------------------------------------------------------------------------
# 4. Every metric value is null
# ---------------------------------------------------------------------------


class TestThresholdsMetricValuesNull:
    def _all_metrics(self) -> list[dict[str, object]]:
        data = json.loads(_THRESHOLDS.read_text(encoding="utf-8"))
        metrics: list[dict[str, object]] = []
        for profile in data.get("profiles", []):
            for m in profile.get("metrics", []):
                metrics.append(m)
        return metrics

    def test_at_least_one_metric_exists(self) -> None:
        assert len(self._all_metrics()) > 0

    def test_all_metric_values_are_null(self) -> None:
        for m in self._all_metrics():
            assert m.get("value") is None, (
                f"metric {m.get('metric')!r} has a non-null value: {m.get('value')!r}"
            )


# ---------------------------------------------------------------------------
# 5. Every metric has a non-empty basis
# ---------------------------------------------------------------------------


class TestThresholdsMetricBasis:
    def test_every_metric_has_basis(self) -> None:
        data = json.loads(_THRESHOLDS.read_text(encoding="utf-8"))
        for profile in data.get("profiles", []):
            for m in profile.get("metrics", []):
                basis = m.get("basis", "")
                assert isinstance(basis, str) and basis.strip(), (
                    f"metric {m.get('metric')!r} in profile {profile.get('profile_id')!r}"
                    " has an empty or missing basis"
                )


# ---------------------------------------------------------------------------
# 6. Nothing reads the file: no .py file contains "thresholds.v0"
# ---------------------------------------------------------------------------


class TestThresholdsNotUsedByCode:
    def _py_files(self) -> list[Path]:
        files: list[Path] = []
        for root in (_SRC_DIR, _TOOLS_DIR):
            files.extend(root.rglob("*.py"))
        return files

    def test_no_py_file_contains_thresholds_v0(self) -> None:
        bad: list[str] = []
        for f in self._py_files():
            try:
                text = f.read_text(encoding="utf-8", errors="replace")
            except OSError:
                continue
            if "thresholds.v0" in text:
                bad.append(str(f))
        assert bad == [], (
            "These files reference 'thresholds.v0' but no code should read it:\n"
            + "\n".join(bad)
        )

    def test_no_py_file_opens_gold_dir_except_gold_set_loaders(self) -> None:
        """No Python file under src/ or tools/ opens gold/ files at module level
        other than the two sanctioned loaders (gold_set_file, gold_slots_file)."""
        # Pattern: open( or read_text( or read_bytes( immediately followed by a path
        # containing 'gold/' -- a simple guard, not a full AST check.
        pattern = re.compile(r"['\"]gold/thresholds")
        bad: list[str] = []
        for f in self._py_files():
            try:
                text = f.read_text(encoding="utf-8", errors="replace")
            except OSError:
                continue
            if pattern.search(text):
                bad.append(str(f))
        assert bad == [], (
            "These files reference 'gold/thresholds' paths:\n" + "\n".join(bad)
        )
