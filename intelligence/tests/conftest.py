"""Shared test settings.

The optional evidence file (``ZTAX_EVIDENCE_FILE_PATH``) must never be written by a test
that did not ask for it. ``build_gateway`` reads that setting, so a developer or CI shell
that happens to export it would otherwise make every test that builds a Gateway through the
factory append to the file (and fail when a second Gateway finds it locked). Each test
starts without the setting; the evidence-file tests set it themselves where they need it.
"""

from __future__ import annotations

import pytest

from ztax_gateway.evidence_file_sink import ENV_BASE_DIR, ENV_PATH


@pytest.fixture(autouse=True)
def _evidence_file_setting_is_off(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.delenv(ENV_PATH, raising=False)
    monkeypatch.delenv(ENV_BASE_DIR, raising=False)
