"""The defects gate's own suite.

Same reasoning as test_check_quarantine.py: every rule in check_defects.py
has a case here that must fail, built by breaking exactly one thing in an
otherwise green fixture repository. A rule with no failing case may never
fire.

    python -m unittest discover -s docs/tools/tests
"""

from __future__ import annotations

import datetime as dt
import shutil
import sys
import tempfile
import unittest
from pathlib import Path

import yaml

HERE = Path(__file__).resolve().parent
REPO = HERE.parents[2]
sys.path.insert(0, str(HERE.parent))

import check_defects  # noqa: E402

TODAY = dt.date(2026, 10, 12)
NOW = dt.datetime(2026, 10, 12, 12, 0, tzinfo=dt.UTC)


def base_waiver(waiver_id: str = "ZTAX-WVR-2026-0001", requirement_id: str = "ZTAX-QA-REQ-0100",
                 expires_at: str = "2026-12-31T23:59:59Z") -> dict:
    return {
        "waiver_id": waiver_id, "requirement_id": requirement_id,
        "scope": "s", "reason": "r", "risk": "k", "risk_owner": "o",
        "compensating_control": "c", "expires_at": expires_at,
        "approvers": ["A", "B"], "approved_at": "2026-10-01",
    }


def base_defect(defect_id: str = "ZTAX-DEF-2026-0001", severity: str = "QA-SEV-2",
                 status: str = "OPEN", waiver_id: str | None = None) -> dict:
    d = {
        "defect_id": defect_id, "severity": severity, "status": status,
        "title": "t", "owner": "o", "opened_at": "2026-10-01",
    }
    if status == "RESOLVED":
        d["resolved_at"] = "2026-10-05"
    if waiver_id is not None:
        d["waiver_id"] = waiver_id
    return d


class Fixture:
    """A throwaway repository: the real schemas plus registers we can break."""

    def __init__(self) -> None:
        self.dir = Path(tempfile.mkdtemp(prefix="ztax-defects-"))
        (self.dir / "docs" / "schema").mkdir(parents=True)
        for name in ("defects.schema.json", "waivers.schema.json"):
            shutil.copy(REPO / "docs" / "schema" / name, self.dir / "docs" / "schema" / name)
        self.defects: dict = {"defects": []}
        self.waivers: dict = {"waivers": []}

    def write(self, path: str, text: str) -> None:
        p = self.dir / path
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text(text, encoding="utf-8", newline="\n")

    def run(self) -> list[str]:
        self.write("docs/defects.yaml", yaml.safe_dump(self.defects, sort_keys=False, allow_unicode=True))
        self.write("docs/waivers.yaml", yaml.safe_dump(self.waivers, sort_keys=False, allow_unicode=True))
        return check_defects.run(self.dir).errors

    def blockers(self, now: dt.datetime = NOW) -> list[dict]:
        self.write("docs/defects.yaml", yaml.safe_dump(self.defects, sort_keys=False, allow_unicode=True))
        self.write("docs/waivers.yaml", yaml.safe_dump(self.waivers, sort_keys=False, allow_unicode=True))
        return check_defects.blocking_defects(self.dir, now)

    def close(self) -> None:
        shutil.rmtree(self.dir, ignore_errors=True)


class DefectsGate(unittest.TestCase):
    def setUp(self) -> None:
        self.fx = Fixture()

    def tearDown(self) -> None:
        self.fx.close()

    def assertFinding(self, needle: str) -> None:
        errors = self.fx.run()
        self.assertTrue(any(needle in e for e in errors), f"expected a finding containing {needle!r}, got {errors}")

    # ---- the baseline ----------------------------------------------------

    def test_empty_register_is_green(self) -> None:
        self.assertEqual(self.fx.run(), [])
        self.assertEqual(self.fx.blockers(), [])

    def test_the_real_register_is_green(self) -> None:
        errors = check_defects.run(REPO).errors
        self.assertEqual(errors, [])

    def test_valid_entry_passes(self) -> None:
        self.fx.defects["defects"].append(base_defect())
        self.fx.waivers["waivers"].append(base_waiver())
        self.assertEqual(self.fx.run(), [])

    # ---- rule a: schema / duplicate ids -----------------------------------

    def test_schema_invalid(self) -> None:
        d = base_defect()
        del d["owner"]
        self.fx.defects["defects"].append(d)
        self.assertFinding("schema:")

    def test_duplicate_defect_id(self) -> None:
        self.fx.defects["defects"].append(base_defect())
        self.fx.defects["defects"].append(base_defect())
        self.assertFinding("is registered twice")

    def test_resolved_requires_resolved_at(self) -> None:
        d = base_defect(status="RESOLVED")
        del d["resolved_at"]
        self.fx.defects["defects"].append(d)
        self.assertFinding("schema:")

    def test_open_forbids_resolved_at(self) -> None:
        d = base_defect(status="OPEN")
        d["resolved_at"] = "2026-10-05"
        self.fx.defects["defects"].append(d)
        self.assertFinding("schema:")

    # ---- rule b: waiver_id must exist -------------------------------------

    def test_waiver_id_not_found(self) -> None:
        self.fx.defects["defects"].append(base_defect(waiver_id="ZTAX-WVR-2026-9999"))
        self.assertFinding("is not in docs/waivers.yaml")

    # ---- rule c: QA-SEV-1 can never carry a waiver ------------------------

    def test_sev1_with_waiver_is_refused(self) -> None:
        self.fx.defects["defects"].append(base_defect(severity="QA-SEV-1", waiver_id="ZTAX-WVR-2026-0001"))
        self.fx.waivers["waivers"].append(base_waiver())
        self.assertFinding("QA-REQ-0099 has no exception")

    def test_sev1_without_waiver_passes(self) -> None:
        self.fx.defects["defects"].append(base_defect(severity="QA-SEV-1"))
        self.assertEqual(self.fx.run(), [])

    # ---- rule d: a SEV-2 waiver must be for QA-REQ-0100 -------------------

    def test_sev2_waiver_for_different_requirement_is_refused(self) -> None:
        self.fx.defects["defects"].append(base_defect(severity="QA-SEV-2", waiver_id="ZTAX-WVR-2026-0001"))
        self.fx.waivers["waivers"].append(base_waiver(requirement_id="ZTAX-QA-REQ-0101"))
        self.assertFinding("not ZTAX-QA-REQ-0100")

    def test_sev2_waiver_for_0100_passes(self) -> None:
        self.fx.defects["defects"].append(base_defect(severity="QA-SEV-2", waiver_id="ZTAX-WVR-2026-0001"))
        self.fx.waivers["waivers"].append(base_waiver(requirement_id="ZTAX-QA-REQ-0100"))
        self.assertEqual(self.fx.run(), [])

    # ---- rule e / blocking_defects: expiry is a blocker, not a gate error -

    def test_sev2_with_valid_0100_waiver_is_not_a_blocker(self) -> None:
        self.fx.defects["defects"].append(base_defect(severity="QA-SEV-2", waiver_id="ZTAX-WVR-2026-0001"))
        self.fx.waivers["waivers"].append(base_waiver(expires_at="2026-12-31T23:59:59Z"))
        self.assertEqual(self.fx.run(), [])
        self.assertEqual(self.fx.blockers(NOW), [])

    def test_sev2_with_expired_waiver_is_a_blocker_not_a_gate_error(self) -> None:
        self.fx.defects["defects"].append(base_defect(severity="QA-SEV-2", waiver_id="ZTAX-WVR-2026-0001"))
        self.fx.waivers["waivers"].append(base_waiver(expires_at="2026-01-01T00:00:00Z"))
        # The register itself is still valid -- expiry is not a schema/cross-file error.
        self.assertEqual(self.fx.run(), [])
        blockers = self.fx.blockers(NOW)
        self.assertEqual([b["defect_id"] for b in blockers], ["ZTAX-DEF-2026-0001"])

    def test_sev2_without_waiver_is_a_blocker(self) -> None:
        self.fx.defects["defects"].append(base_defect(severity="QA-SEV-2"))
        blockers = self.fx.blockers(NOW)
        self.assertEqual([b["defect_id"] for b in blockers], ["ZTAX-DEF-2026-0001"])

    def test_open_sev1_is_always_a_blocker(self) -> None:
        self.fx.defects["defects"].append(base_defect(severity="QA-SEV-1"))
        blockers = self.fx.blockers(NOW)
        self.assertEqual([b["defect_id"] for b in blockers], ["ZTAX-DEF-2026-0001"])

    def test_resolved_defects_never_block(self) -> None:
        self.fx.defects["defects"].append(base_defect(severity="QA-SEV-1", status="RESOLVED"))
        self.fx.defects["defects"].append(base_defect(defect_id="ZTAX-DEF-2026-0002",
                                                        severity="QA-SEV-2", status="RESOLVED"))
        self.assertEqual(self.fx.blockers(NOW), [])

    # ---- expiry is judged against a full instant, not a calendar date ----

    def test_waiver_expiring_same_day_is_a_blocker_once_the_instant_passes(self) -> None:
        """A waiver expiring at 08:00Z on 2026-10-12 must not be treated as
        covering the whole day -- at 12:00Z the same day it is already
        expired, and the defect it covered blocks again."""
        self.fx.defects["defects"].append(base_defect(severity="QA-SEV-2", waiver_id="ZTAX-WVR-2026-0001"))
        self.fx.waivers["waivers"].append(base_waiver(expires_at="2026-10-12T08:00:00Z"))
        after = dt.datetime(2026, 10, 12, 12, 0, tzinfo=dt.UTC)
        blockers = self.fx.blockers(after)
        self.assertEqual([b["defect_id"] for b in blockers], ["ZTAX-DEF-2026-0001"])

    def test_waiver_expiring_same_day_is_not_yet_a_blocker_before_the_instant(self) -> None:
        """The same waiver, one hour before its own expiry, still covers
        the defect -- the same calendar day, the opposite answer."""
        self.fx.defects["defects"].append(base_defect(severity="QA-SEV-2", waiver_id="ZTAX-WVR-2026-0001"))
        self.fx.waivers["waivers"].append(base_waiver(expires_at="2026-10-12T08:00:00Z"))
        before = dt.datetime(2026, 10, 12, 7, 0, tzinfo=dt.UTC)
        self.assertEqual(self.fx.blockers(before), [])


class ReleaseFlag(unittest.TestCase):
    """main()'s --release flag: blockers are informational without it,
    fatal with it; the register being valid is orthogonal to blocking."""

    def setUp(self) -> None:
        self.fx = Fixture()

    def tearDown(self) -> None:
        self.fx.close()

    def _write(self) -> None:
        self.fx.write("docs/defects.yaml", yaml.safe_dump(self.fx.defects, sort_keys=False, allow_unicode=True))
        self.fx.write("docs/waivers.yaml", yaml.safe_dump(self.fx.waivers, sort_keys=False, allow_unicode=True))

    def test_open_sev1_blocks_under_release(self) -> None:
        self.fx.defects["defects"].append(base_defect(severity="QA-SEV-1"))
        self._write()
        rc = check_defects.main(["--root", str(self.fx.dir), "--today", TODAY.isoformat(), "--release"])
        self.assertEqual(rc, 1)

    def test_open_sev1_does_not_fail_without_release(self) -> None:
        self.fx.defects["defects"].append(base_defect(severity="QA-SEV-1"))
        self._write()
        rc = check_defects.main(["--root", str(self.fx.dir), "--today", TODAY.isoformat()])
        self.assertEqual(rc, 0)

    def test_empty_register_passes_release_too(self) -> None:
        self._write()
        rc = check_defects.main(["--root", str(self.fx.dir), "--today", TODAY.isoformat(), "--release"])
        self.assertEqual(rc, 0)

    def test_invalid_register_fails_regardless_of_release(self) -> None:
        d = base_defect()
        del d["owner"]
        self.fx.defects["defects"].append(d)
        self._write()
        rc = check_defects.main(["--root", str(self.fx.dir), "--today", TODAY.isoformat()])
        self.assertEqual(rc, 1)


if __name__ == "__main__":
    unittest.main()
