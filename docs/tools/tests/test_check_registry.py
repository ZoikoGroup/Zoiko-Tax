"""The register gate's own suite.

A gate nobody has tested is a gate nobody should trust — so every rule in
check_registry.py has a case here that must fail, built by breaking exactly one
thing in an otherwise green fixture repository. A rule with no failing case is
a rule that may never fire.

Standard library unittest, so the suite needs nothing the gate does not.

    python -m unittest discover -s docs/tools/tests
"""

from __future__ import annotations

import copy
import datetime as dt
import hashlib
import shutil
import sys
import tempfile
import unittest
from pathlib import Path

import yaml

HERE = Path(__file__).resolve().parent
REPO = HERE.parents[2]
sys.path.insert(0, str(HERE.parent))

import check_registry  # noqa: E402

TODAY = dt.date(2026, 9, 25)
NOW = dt.datetime(2026, 9, 25, tzinfo=dt.UTC)

SPEC = "# ZTAX-TST-001\n\nA specification.\n"


def sha(text: str) -> str:
    return "sha256:" + hashlib.sha256(text.encode("utf-8")).hexdigest()


def base_register() -> dict:
    return {
        "documents": [
            {
                "document_id": "ZTAX-TST-001", "title": "Test spec", "class": "S", "tranche": "F1",
                "version": "1.0", "status": "DRAFT", "owner": "Head of Testing", "approvers": [],
                "effective_date": None, "supersedes": None, "dependencies": ["ZTAX-EXT-001"],
                "source_path": "docs/spec.md", "publication_hash": sha(SPEC), "review_due": "2026-12-01",
                "applicable_release": None, "retention_policy": "10Y_AFTER_SUPERSESSION",
            },
            {
                "document_id": "ZTAX-EXT-001", "title": "External baseline", "class": "C", "tranche": "EXISTING",
                "version": "2.0", "status": "EFFECTIVE", "owner": "CTO", "approvers": ["CTO"],
                "effective_date": "2026-01-01", "supersedes": None, "dependencies": [],
                "source_path": None, "publication_hash": None, "review_due": "2027-01-01",
                "applicable_release": None, "retention_policy": "PERMANENT",
            },
            {
                # The real schemas the fixture copies cite GOV-001, so the
                # fixture registers it like the real register does.
                "document_id": "ZTAX-GOV-001", "title": "Governance", "class": "C", "tranche": "F0",
                "version": None, "status": "PROPOSED", "owner": "CTO", "approvers": [],
                "effective_date": None, "supersedes": None, "dependencies": [],
                "source_path": None, "publication_hash": None, "review_due": None,
                "applicable_release": None, "retention_policy": "PERMANENT",
            },
        ],
        "gaps": [
            {"document_id": "ZTAX-EXT-001", "kind": "VERIFY", "issue": "hash not held here",
             "owner": "CTO", "due": "2026-10-09"},
        ],
    }


def base_requirements() -> dict:
    return {
        "requirements": [
            {
                "id": "ZTAX-TST-REQ-0001", "document_id": "ZTAX-TST-001", "version": "1.0",
                "statement": "The thing MUST hold.", "rationale": "Because.", "source_authority": "ZTAX-TST-001",
                "owner": "Head of Testing", "applies_to": ["testing"], "verification_method": "TEST",
                "verification_ref": None, "status": "PROPOSED", "effective_from": None, "effective_to": None,
            },
        ],
    }


class Fixture:
    """A throwaway repository: the real schemas plus a register we can break."""

    def __init__(self) -> None:
        self.dir = Path(tempfile.mkdtemp(prefix="ztax-register-"))
        (self.dir / "docs" / "schema").mkdir(parents=True)
        for name in ("register.schema.json", "requirements.schema.json", "waivers.schema.json"):
            shutil.copy(REPO / "docs" / "schema" / name, self.dir / "docs" / "schema" / name)
        (self.dir / "docs" / "spec.md").write_text(SPEC, encoding="utf-8", newline="\n")
        self.register = base_register()
        self.requirements = base_requirements()
        self.waivers: dict = {"waivers": []}

    def write(self, path: str, text: str) -> None:
        p = self.dir / path
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text(text, encoding="utf-8", newline="\n")

    def run(self, today: dt.date = TODAY, now: dt.datetime = NOW) -> list[str]:
        for name, data in (("register", self.register), ("requirements", self.requirements),
                           ("waivers", self.waivers)):
            self.write(f"docs/{name}.yaml", yaml.safe_dump(data, sort_keys=False, allow_unicode=True))
        # Generate the index first, so a case fails for its own reason and not
        # for a stale index.
        check_registry.run(self.dir, today, now, write_index=True)
        return check_registry.run(self.dir, today, now, write_index=False).errors

    def close(self) -> None:
        shutil.rmtree(self.dir, ignore_errors=True)


class RegisterGate(unittest.TestCase):
    def setUp(self) -> None:
        self.fx = Fixture()

    def tearDown(self) -> None:
        self.fx.close()

    def assertFinding(self, needle: str) -> None:
        errors = self.fx.run()
        self.assertTrue(any(needle in e for e in errors), f"expected a finding containing {needle!r}, got {errors}")

    # ---- the baseline ------------------------------------------------------

    def test_fixture_is_green(self) -> None:
        self.assertEqual(self.fx.run(), [])

    def test_the_real_register_is_green(self) -> None:
        # The repository's own register, judged today. This is the case that
        # fails when somebody edits a spec without re-registering its hash.
        errors = check_registry.run(REPO, dt.datetime.now(dt.UTC).date(), dt.datetime.now(dt.UTC), False).errors
        self.assertEqual(errors, [])

    # ---- unregistered primary document IDs ---------------------------------

    def test_unregistered_document_id_in_a_file(self) -> None:
        self.fx.write("backend/thing.go", "// see ZTAX-NOPE-001 for why\n")
        self.assertFinding("backend/thing.go:1: ZTAX-NOPE-001 is not a registered document")

    def test_waiver_and_pack_ids_are_not_documents(self) -> None:
        self.fx.write("notes.md", "ZTAX-WVR-2026-0041 applies to ZTAX-CP-GBR-VAT-v1.2.\n")
        self.assertEqual(self.fx.run(), [])

    def test_unregistered_dependency(self) -> None:
        self.fx.register["documents"][0]["dependencies"] = ["ZTAX-GONE-001"]
        self.assertFinding("depends on ZTAX-GONE-001, which is not registered")

    def test_supersedes_a_live_document(self) -> None:
        self.fx.register["documents"][0]["supersedes"] = "ZTAX-EXT-001"
        self.assertFinding("supersedes ZTAX-EXT-001, which is still EFFECTIVE")

    # ---- broken requirement references -------------------------------------

    def test_unregistered_requirement_id_in_a_file(self) -> None:
        self.fx.write("docs/other.md", "Per ZTAX-TST-REQ-0099.\n")
        self.assertFinding("docs/other.md:1: ZTAX-TST-REQ-0099 is not a registered requirement")

    def test_requirement_of_an_unregistered_document(self) -> None:
        self.fx.requirements["requirements"][0]["document_id"] = "ZTAX-ABC-001"
        self.assertFinding("belongs to ZTAX-ABC-001, which is not registered")

    def test_requirement_filed_under_the_wrong_document(self) -> None:
        r = copy.deepcopy(self.fx.requirements["requirements"][0])
        r["id"] = "ZTAX-EXT-REQ-0001"
        r["version"] = "1.0"
        self.fx.requirements["requirements"].append(r)
        self.assertFinding("is filed under ZTAX-TST-001; the ID names domain EXT")

    def test_requirement_cites_a_different_version(self) -> None:
        self.fx.requirements["requirements"][0]["version"] = "0.9"
        self.assertFinding("cites ZTAX-TST-001 version 0.9")

    def test_verification_path_must_exist(self) -> None:
        self.fx.requirements["requirements"][0]["verification_ref"] = "backend/missing_test.go::TestX"
        self.assertFinding("verification_ref backend/missing_test.go does not exist")

    def test_waiver_of_an_unregistered_requirement(self) -> None:
        self.fx.waivers["waivers"].append(self.waiver(requirement_id="ZTAX-TST-REQ-0002"))
        self.assertFinding("waives ZTAX-TST-REQ-0002, which is not registered")

    # ---- missing owners ----------------------------------------------------

    def test_document_without_owner(self) -> None:
        self.fx.register["documents"][0]["owner"] = ""
        self.assertFinding("schema: documents/0/owner")

    def test_requirement_with_blank_owner(self) -> None:
        self.fx.requirements["requirements"][0]["owner"] = "   "
        self.assertFinding("ZTAX-TST-REQ-0001 has no owner")

    # ---- expired mandatory reviews -----------------------------------------

    def test_review_date_in_the_past(self) -> None:
        self.fx.register["documents"][0]["review_due"] = "2026-09-01"
        self.assertFinding("ZTAX-TST-001 review was due 2026-09-01")

    def test_live_document_without_review_date(self) -> None:
        self.fx.register["documents"][0]["review_due"] = None
        self.assertFinding("ZTAX-TST-001 is DRAFT and has no review date")

    def test_gap_past_its_due_date(self) -> None:
        self.fx.register["gaps"][0]["due"] = "2026-09-01"
        self.assertFinding("gap for ZTAX-EXT-001 was due 2026-09-01 and is still open")

    def test_expired_waiver(self) -> None:
        self.fx.waivers["waivers"].append(self.waiver(expires_at="2026-09-01T00:00:00Z"))
        self.assertFinding("expired at 2026-09-01T00:00:00Z")

    # ---- duplicate IDs -----------------------------------------------------

    def test_duplicate_document(self) -> None:
        self.fx.register["documents"].append(copy.deepcopy(self.fx.register["documents"][0]))
        self.assertFinding("ZTAX-TST-001 is registered twice")

    def test_duplicate_requirement(self) -> None:
        self.fx.requirements["requirements"].append(copy.deepcopy(self.fx.requirements["requirements"][0]))
        self.assertFinding("ZTAX-TST-REQ-0001 is registered twice")

    def test_duplicate_waiver(self) -> None:
        self.fx.waivers["waivers"] += [self.waiver(), self.waiver()]
        self.assertFinding("ZTAX-WVR-2026-0001 is registered twice")

    # ---- production requirements without a verification reference ----------

    def test_effective_requirement_without_verification(self) -> None:
        self.fx.requirements["requirements"][0]["status"] = "EFFECTIVE"
        self.assertFinding("ZTAX-TST-REQ-0001 is EFFECTIVE with no verification_ref")

    def test_effective_requirement_of_a_draft_document(self) -> None:
        r = self.fx.requirements["requirements"][0]
        r["status"] = "EFFECTIVE"
        r["verification_ref"] = "DET-CONF-0001"
        self.assertFinding("ZTAX-TST-REQ-0001 is EFFECTIVE but ZTAX-TST-001 is DRAFT")

    # ---- what EFFECTIVE and approval mean ------------------------------------

    def test_effective_document_without_hash_and_no_gap(self) -> None:
        self.fx.register["gaps"] = []
        self.assertFinding("ZTAX-EXT-001 is EFFECTIVE with no publication_hash")

    def test_approved_without_approvers(self) -> None:
        d = self.fx.register["documents"][0]
        d["status"] = "BASELINED-FOR-BUILD"
        self.assertFinding("ZTAX-TST-001 is BASELINED-FOR-BUILD with no approvers named")

    def test_self_approved_waiver(self) -> None:
        self.fx.waivers["waivers"].append(self.waiver(approvers=["Head of Testing"]))
        self.assertFinding("is approved only by its own risk owner")

    # ---- publication hash --------------------------------------------------

    def test_edited_source_fails_the_hash(self) -> None:
        self.fx.write("docs/spec.md", SPEC + "An edit nobody re-registered.\n")
        self.assertFinding("ZTAX-TST-001 publication_hash does not match docs/spec.md (run --rehash-drafts)")

    def test_rehash_drafts_restores_green_and_touches_only_the_hash(self) -> None:
        self.fx.run()
        self.fx.write("docs/spec.md", SPEC + "An edit.\n")
        before = (self.fx.dir / "docs" / "register.yaml").read_text(encoding="utf-8")
        self.assertEqual(check_registry.rehash_drafts(self.fx.dir), 1)
        after = (self.fx.dir / "docs" / "register.yaml").read_text(encoding="utf-8")
        changed = [(a, b) for a, b in zip(before.splitlines(), after.splitlines(), strict=True) if a != b]
        self.assertEqual(len(changed), 1)
        self.assertIn("publication_hash", changed[0][1])
        check_registry.run(self.fx.dir, TODAY, NOW, write_index=True)
        self.assertEqual(check_registry.run(self.fx.dir, TODAY, NOW, write_index=False).errors, [])

    def test_missing_source(self) -> None:
        self.fx.register["documents"][0]["source_path"] = "docs/absent.md"
        self.assertFinding("source_path docs/absent.md does not exist")

    # ---- the generated index ------------------------------------------------

    def test_hand_edited_index(self) -> None:
        self.fx.run()
        index = self.fx.dir / "docs" / "ESTATE.md"
        index.write_text(index.read_text(encoding="utf-8") + "\nA hand edit.\n", encoding="utf-8")
        errors = check_registry.run(self.fx.dir, TODAY, NOW, write_index=False).errors
        self.assertTrue(any("ESTATE.md" in e for e in errors), errors)

    # ---- helpers -----------------------------------------------------------

    @staticmethod
    def waiver(**overrides: object) -> dict:
        w = {
            "waiver_id": "ZTAX-WVR-2026-0001", "requirement_id": "ZTAX-TST-REQ-0001",
            "scope": "one adapter", "reason": "certificate rotation", "risk": "unconfirmed submission",
            "risk_owner": "Head of Testing", "compensating_control": "dual approval",
            "expires_at": "2026-10-05T23:59:59Z", "approvers": ["CTO", "Head of Testing"],
            "approved_at": "2026-09-24",
        }
        w.update(overrides)
        return w


if __name__ == "__main__":
    unittest.main()
