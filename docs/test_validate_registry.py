#!/usr/bin/env python3
"""
Self-tests for docs/validate_registry.py — docs/test_validate_registry.py

Uses stdlib unittest only (no pytest required, though pytest will discover it).
Each test builds a minimal in-memory YAML structure in a temp directory and
asserts that the validator does / does not emit a FAIL line.

Run with:
    python docs/test_validate_registry.py          # unittest runner
    python -m pytest docs/test_validate_registry.py -v   # via pytest

Coverage matrix (one negative + one positive per rule):
  ✓ Requirement status BASELINED accepted; "APPROVED" rejected (wrong status set)
  ✓ Past review_due fails; APPROVED doc with null review_due fails
  ✓ EFFECTIVE requirement with null ref fails
  ✓ EFFECTIVE requirement with non-existent ref fails
  ✓ BASELINED requirement on a DRAFT document fails
  ✓ Duplicate requirement ID fails
  ✓ EFFECTIVE requirement with good ref on EFFECTIVE doc passes
  ✓ Fully valid minimal registry passes (0 failures)
"""

import os
import sys
import tempfile
import textwrap
import unittest

# ---------------------------------------------------------------------------
# Make sure the validator module is importable from the same repo tree.
# ---------------------------------------------------------------------------
_DOCS_DIR = os.path.dirname(os.path.abspath(__file__))
if _DOCS_DIR not in sys.path:
    sys.path.insert(0, _DOCS_DIR)

import validate_registry as vr  # noqa: E402  (after path fixup)


# ---------------------------------------------------------------------------
# Helper
# ---------------------------------------------------------------------------

class _RegistryFixture:
    """
    Build a minimal temp-directory registry and invoke validate_registry.main()
    with captured stdout, returning (exit_code, output_lines).
    """

    # Minimal valid register entry (EFFECTIVE — has review_due in the future)
    EFFECTIVE_DOC = textwrap.dedent("""\
        documents:
          - document_id: ZTAX-TST-001
            title: "Test Document"
            class: S
            version: "1.0"
            status: EFFECTIVE
            owner: "Test Owner"
            approvers: ["CTO"]
            effective_date: "2026-01-01"
            supersedes: null
            dependencies: []
            source_path: null
            publication_hash: null
            review_due: "2099-01-01"
            applicable_release: ["all"]
            retention_policy: "PERMANENT"
    """)

    PROPOSED_DOC = textwrap.dedent("""\
        documents:
          - document_id: ZTAX-TST-001
            title: "Test Document"
            class: S
            version: "1.0"
            status: PROPOSED
            owner: "Test Owner"
            approvers: []
            effective_date: null
            supersedes: null
            dependencies: []
            source_path: null
            publication_hash: null
            review_due: null
            applicable_release: ["all"]
            retention_policy: "PERMANENT"
    """)

    DRAFT_DOC = textwrap.dedent("""\
        documents:
          - document_id: ZTAX-TST-001
            title: "Test Document"
            class: S
            version: "1.0"
            status: DRAFT
            owner: "Test Owner"
            approvers: []
            effective_date: null
            supersedes: null
            dependencies: []
            source_path: null
            publication_hash: null
            review_due: null
            applicable_release: ["all"]
            retention_policy: "PERMANENT"
    """)

    APPROVED_DOC_NO_DUE = textwrap.dedent("""\
        documents:
          - document_id: ZTAX-TST-001
            title: "Test Document"
            class: S
            version: "1.0"
            status: APPROVED
            owner: "Test Owner"
            approvers: ["CTO"]
            effective_date: null
            supersedes: null
            dependencies: []
            source_path: null
            publication_hash: null
            review_due: null
            applicable_release: ["all"]
            retention_policy: "PERMANENT"
    """)

    @staticmethod
    def minimal_req(
        req_id="ZTAX-TST-REQ-0001",
        doc_id="ZTAX-TST-001",
        status="PROPOSED",
        vref=None,
        owner="Test Owner",
    ) -> str:
        vref_yaml = f'"{vref}"' if vref is not None else "null"
        return textwrap.dedent(f"""\
            requirements:
              - id: {req_id}
                document_id: "{doc_id}"
                version: "1.0"
                statement: "Test requirement."
                rationale: "Testing."
                source_authority: "test"
                owner: "{owner}"
                applies_to: ["test"]
                verification_method: TEST
                verification_ref: {vref_yaml}
                status: {status}
                effective_from: null
                effective_to: null
        """)

    def run(
        self,
        register_yaml: str,
        reqs_yaml: str,
        today: str = "2026-09-28",
        strict: bool = False,
        extra_files: dict | None = None,
    ) -> tuple[int, list[str]]:
        """
        Write register and requirements YAML to a temp dir, run the validator,
        return (exit_code, output_lines).
        `extra_files` is a dict of {relative_path: content} to write into the temp dir.
        """
        with tempfile.TemporaryDirectory() as tmpdir:
            docs_dir = os.path.join(tmpdir, "docs")
            os.makedirs(docs_dir)

            with open(os.path.join(docs_dir, "register.yaml"), "w", encoding="utf-8") as f:
                f.write(register_yaml)
            with open(os.path.join(docs_dir, "requirements.yaml"), "w", encoding="utf-8") as f:
                f.write(reqs_yaml)

            if extra_files:
                for rel_path, content in extra_files.items():
                    full = os.path.join(tmpdir, rel_path)
                    os.makedirs(os.path.dirname(full), exist_ok=True)
                    with open(full, "w", encoding="utf-8") as f:
                        f.write(content)

            # Capture stdout
            import io
            from contextlib import redirect_stdout
            buf = io.StringIO()
            argv_backup = sys.argv[:]
            try:
                sys.argv = [
                    "validate_registry.py",
                    "--root", tmpdir,
                    "--today", today,
                ]
                if strict:
                    sys.argv.append("--strict")

                with redirect_stdout(buf):
                    exit_code = vr.main()
            finally:
                sys.argv = argv_backup

            output = buf.getvalue()
            lines = [l.strip() for l in output.splitlines() if l.strip()]
            return exit_code, lines

    def fail_lines(self, lines: list[str]) -> list[str]:
        return [l for l in lines if l.startswith("FAIL")]


# ---------------------------------------------------------------------------
# Test cases
# ---------------------------------------------------------------------------

class TestRequirementStatusSet(unittest.TestCase):
    """
    RULE: requirement status must be in {PROPOSED, BASELINED, EFFECTIVE, RETIRED}.
    "APPROVED" is a document status, not a requirement status → FAIL.
    """

    fix = _RegistryFixture()

    def test_baselined_accepted(self):
        """BASELINED is a valid requirement status when source doc is BFB+."""
        reg = self.fix.EFFECTIVE_DOC  # EFFECTIVE doc -> BASELINED req is fine
        req = self.fix.minimal_req(status="BASELINED")
        code, lines = self.fix.run(reg, req)
        fails = self.fix.fail_lines(lines)
        self.assertEqual(code, 0, f"Expected 0 failures, got: {fails}")

    def test_approved_rejected_for_requirement(self):
        """APPROVED is not in the requirement status set → FAIL §9.2-status."""
        reg = self.fix.PROPOSED_DOC
        req = self.fix.minimal_req(status="APPROVED")
        code, lines = self.fix.run(reg, req)
        fails = self.fix.fail_lines(lines)
        self.assertEqual(code, 1)
        self.assertTrue(
            any("§9.2-status" in f for f in fails),
            f"Expected §9.2-status failure, got: {fails}",
        )

    def test_retired_accepted(self):
        """RETIRED is a valid requirement status."""
        reg = self.fix.PROPOSED_DOC
        req = self.fix.minimal_req(status="RETIRED")
        code, lines = self.fix.run(reg, req)
        fails = self.fix.fail_lines(lines)
        # Only failure allowed is the coupling check (PROPOSED doc, RETIRED req is ok)
        status_fails = [f for f in fails if "§9.2-status" in f]
        self.assertEqual(status_fails, [], f"RETIRED should be accepted, got: {status_fails}")


class TestReviewDue(unittest.TestCase):
    """
    RULE: past review_due → FAIL.
    APPROVED/EFFECTIVE doc with null review_due → FAIL.
    """

    fix = _RegistryFixture()

    def test_approved_null_review_due_fails(self):
        """APPROVED document with review_due null → FAIL §9.4-review."""
        reg = self.fix.APPROVED_DOC_NO_DUE
        req = self.fix.minimal_req()
        code, lines = self.fix.run(reg, req)
        fails = self.fix.fail_lines(lines)
        self.assertEqual(code, 1)
        self.assertTrue(
            any("§9.4-review" in f for f in fails),
            f"Expected §9.4-review failure, got: {fails}",
        )

    def test_past_review_due_fails(self):
        """Past review_due (any status) → FAIL §9.4-expired."""
        import textwrap
        reg = textwrap.dedent("""\
            documents:
              - document_id: ZTAX-TST-001
                title: "Test"
                class: S
                version: "1.0"
                status: PROPOSED
                owner: "Test Owner"
                approvers: []
                effective_date: null
                supersedes: null
                dependencies: []
                source_path: null
                publication_hash: null
                review_due: "2020-01-01"
                applicable_release: ["all"]
                retention_policy: "PERMANENT"
        """)
        req = self.fix.minimal_req()
        code, lines = self.fix.run(reg, req, today="2026-09-28")
        fails = self.fix.fail_lines(lines)
        self.assertEqual(code, 1)
        self.assertTrue(
            any("§9.4-expired" in f for f in fails),
            f"Expected §9.4-expired failure, got: {fails}",
        )

    def test_future_review_due_passes(self):
        """Future review_due → no expiry failure."""
        reg = self.fix.EFFECTIVE_DOC  # review_due: 2099-01-01
        req = self.fix.minimal_req(status="BASELINED")  # EFFECTIVE doc -> ok
        code, lines = self.fix.run(reg, req, today="2026-09-28")
        fails = self.fix.fail_lines(lines)
        expired = [f for f in fails if "§9.4-expired" in f]
        self.assertEqual(expired, [], f"Should not have expired failure, got: {expired}")


class TestEffectiveRequirement(unittest.TestCase):
    """
    RULE: EFFECTIVE requirement → verification_ref must be non-null and resolve.
    """

    fix = _RegistryFixture()

    def test_effective_null_ref_fails(self):
        """EFFECTIVE requirement + null verification_ref → FAIL §9.4-effective."""
        reg = self.fix.EFFECTIVE_DOC
        req = self.fix.minimal_req(status="EFFECTIVE", vref=None)
        code, lines = self.fix.run(reg, req)
        fails = self.fix.fail_lines(lines)
        self.assertEqual(code, 1)
        self.assertTrue(
            any("§9.4-effective" in f and "null" in f for f in fails),
            f"Expected §9.4-effective null failure, got: {fails}",
        )

    def test_effective_nonexistent_ref_fails(self):
        """EFFECTIVE requirement + non-existent ref path → FAIL §9.4-refmissing."""
        reg = self.fix.EFFECTIVE_DOC
        req = self.fix.minimal_req(status="EFFECTIVE", vref="docs/nonexistent_file.py")
        code, lines = self.fix.run(reg, req)
        fails = self.fix.fail_lines(lines)
        self.assertEqual(code, 1)
        self.assertTrue(
            any("§9.4-refmissing" in f or "§9.4-effective" in f for f in fails),
            f"Expected §9.4-refmissing or §9.4-effective failure, got: {fails}",
        )

    def test_effective_valid_ref_passes(self):
        """EFFECTIVE requirement + existing ref + EFFECTIVE doc → passes."""
        reg = self.fix.EFFECTIVE_DOC
        req = self.fix.minimal_req(status="EFFECTIVE", vref="docs/sentinel.txt")
        code, lines = self.fix.run(
            reg, req, extra_files={"docs/sentinel.txt": "exists\n"}
        )
        fails = self.fix.fail_lines(lines)
        self.assertEqual(code, 0, f"Expected 0 failures, got: {fails}")


class TestDocumentStatusCoupling(unittest.TestCase):
    """
    RULE: BASELINED requirement on a DRAFT document → FAIL §9.4-coupling.
    """

    fix = _RegistryFixture()

    def test_baselined_req_on_draft_doc_fails(self):
        """BASELINED requirement with source document DRAFT → FAIL §9.4-coupling."""
        reg = self.fix.DRAFT_DOC
        req = self.fix.minimal_req(status="BASELINED")
        code, lines = self.fix.run(reg, req)
        fails = self.fix.fail_lines(lines)
        self.assertEqual(code, 1)
        self.assertTrue(
            any("§9.4-coupling" in f for f in fails),
            f"Expected §9.4-coupling failure, got: {fails}",
        )

    def test_baselined_req_on_effective_doc_passes(self):
        """BASELINED requirement with source document EFFECTIVE → coupling ok."""
        reg = self.fix.EFFECTIVE_DOC
        req = self.fix.minimal_req(status="BASELINED")
        code, lines = self.fix.run(reg, req)
        fails = self.fix.fail_lines(lines)
        coupling = [f for f in fails if "§9.4-coupling" in f]
        self.assertEqual(coupling, [], f"Should have no coupling failure, got: {coupling}")


class TestDuplicateRequirementId(unittest.TestCase):
    """
    RULE: duplicate requirement ID → FAIL §9.4-unique.
    """

    fix = _RegistryFixture()

    def test_duplicate_id_fails(self):
        """Two requirements sharing the same id → FAIL §9.4-unique."""
        import textwrap
        reg = self.fix.PROPOSED_DOC
        reqs = textwrap.dedent("""\
            requirements:
              - id: ZTAX-TST-REQ-0001
                document_id: "ZTAX-TST-001"
                version: "1.0"
                statement: "First."
                rationale: "test"
                source_authority: "test"
                owner: "Test Owner"
                applies_to: ["test"]
                verification_method: TEST
                verification_ref: null
                status: PROPOSED
                effective_from: null
                effective_to: null
              - id: ZTAX-TST-REQ-0001
                document_id: "ZTAX-TST-001"
                version: "1.0"
                statement: "Duplicate."
                rationale: "test"
                source_authority: "test"
                owner: "Test Owner"
                applies_to: ["test"]
                verification_method: TEST
                verification_ref: null
                status: PROPOSED
                effective_from: null
                effective_to: null
        """)
        code, lines = self.fix.run(reg, reqs)
        fails = self.fix.fail_lines(lines)
        self.assertEqual(code, 1)
        self.assertTrue(
            any("§9.4-unique" in f for f in fails),
            f"Expected §9.4-unique failure, got: {fails}",
        )

    def test_unique_ids_pass(self):
        """Two requirements with different ids → no duplicate failure."""
        import textwrap
        reg = self.fix.PROPOSED_DOC
        reqs = textwrap.dedent("""\
            requirements:
              - id: ZTAX-TST-REQ-0001
                document_id: "ZTAX-TST-001"
                version: "1.0"
                statement: "First."
                rationale: "test"
                source_authority: "test"
                owner: "Test Owner"
                applies_to: ["test"]
                verification_method: TEST
                verification_ref: null
                status: PROPOSED
                effective_from: null
                effective_to: null
              - id: ZTAX-TST-REQ-0002
                document_id: "ZTAX-TST-001"
                version: "1.0"
                statement: "Second."
                rationale: "test"
                source_authority: "test"
                owner: "Test Owner"
                applies_to: ["test"]
                verification_method: TEST
                verification_ref: null
                status: PROPOSED
                effective_from: null
                effective_to: null
        """)
        code, lines = self.fix.run(reg, reqs)
        fails = self.fix.fail_lines(lines)
        dup = [f for f in fails if "§9.4-unique" in f]
        self.assertEqual(dup, [], f"Should have no duplicate failure, got: {dup}")


class TestMinimalValidRegistry(unittest.TestCase):
    """
    RULE: a fully correct minimal registry → 0 failures.
    """

    fix = _RegistryFixture()

    def test_clean_registry_passes(self):
        """Fully valid minimal registry with PROPOSED doc and PROPOSED req → OK."""
        reg = self.fix.PROPOSED_DOC
        req = self.fix.minimal_req()
        code, lines = self.fix.run(reg, req)
        fails = self.fix.fail_lines(lines)
        self.assertEqual(code, 0, f"Expected 0 failures, got: {fails}")


class TestIdPrefixMatching(unittest.TestCase):
    """
    RULE: prefix matching strips final -NNN from each doc id before comparing.
    ZTAX-AI-REQ-0001 must map to ZTAX-AI-001, not ZTAX-AIGOV-001.
    """

    fix = _RegistryFixture()

    def test_ai_prefix_matches_ai_not_aigov(self):
        """ZTAX-AI-REQ-0001 with document_id ZTAX-AI-001 succeeds even when ZTAX-AIGOV-001 is registered."""
        reg = textwrap.dedent("""\
            documents:
              - document_id: ZTAX-AI-001
                title: "Intelligence Fabric Architecture"
                class: L
                version: "1.0"
                status: PROPOSED
                owner: "Head of AI"
                approvers: []
                effective_date: null
                supersedes: null
                dependencies: []
                source_path: null
                publication_hash: null
                review_due: null
                applicable_release: ["all"]
                retention_policy: "PERMANENT"
              - document_id: ZTAX-AIGOV-001
                title: "AI Governance"
                class: C
                version: "1.0"
                status: PROPOSED
                owner: "CTO"
                approvers: []
                effective_date: null
                supersedes: null
                dependencies: []
                source_path: null
                publication_hash: null
                review_due: null
                applicable_release: ["all"]
                retention_policy: "PERMANENT"
        """)
        req = self.fix.minimal_req(req_id="ZTAX-AI-REQ-0001", doc_id="ZTAX-AI-001")
        code, lines = self.fix.run(reg, req)
        fails = self.fix.fail_lines(lines)
        self.assertEqual(code, 0, f"Expected 0 failures, got: {fails}")

    def test_ai_prefix_fails_when_declared_as_aigov(self):
        """ZTAX-AI-REQ-0001 declared with document_id ZTAX-AIGOV-001 fails prefix check."""
        reg = textwrap.dedent("""\
            documents:
              - document_id: ZTAX-AI-001
                title: "Intelligence Fabric Architecture"
                class: L
                version: "1.0"
                status: PROPOSED
                owner: "Head of AI"
                approvers: []
                effective_date: null
                supersedes: null
                dependencies: []
                source_path: null
                publication_hash: null
                review_due: null
                applicable_release: ["all"]
                retention_policy: "PERMANENT"
              - document_id: ZTAX-AIGOV-001
                title: "AI Governance"
                class: C
                version: "1.0"
                status: PROPOSED
                owner: "CTO"
                approvers: []
                effective_date: null
                supersedes: null
                dependencies: []
                source_path: null
                publication_hash: null
                review_due: null
                applicable_release: ["all"]
                retention_policy: "PERMANENT"
        """)
        req = self.fix.minimal_req(req_id="ZTAX-AI-REQ-0001", doc_id="ZTAX-AIGOV-001")
        code, lines = self.fix.run(reg, req)
        fails = self.fix.fail_lines(lines)
        self.assertEqual(code, 1)
        self.assertTrue(
            any("§9.4-prefix" in f and "ZTAX-AI-001" in f for f in fails),
            f"Expected prefix mismatch pointing to ZTAX-AI-001, got: {fails}",
        )


class TestStrictFlag(unittest.TestCase):
    """
    RULE: --strict flag causes TODO-CONFIRM entries to fail rather than warn.
    """

    fix = _RegistryFixture()

    def test_todo_confirm_warns_without_strict(self):
        """TODO-CONFIRM document produces a warning and exit code 0 without --strict."""
        reg = textwrap.dedent("""\
            documents:
              - document_id: ZTAX-TST-001
                title: "Test Document"
                class: S
                version: "1.0"
                status: TODO-CONFIRM
                owner: "Test Owner"
                approvers: []
                effective_date: null
                supersedes: null
                dependencies: []
                source_path: null
                publication_hash: null
                review_due: null
                applicable_release: ["all"]
                retention_policy: "PERMANENT"
        """)
        req = self.fix.minimal_req()
        code, lines = self.fix.run(reg, req, strict=False)
        self.assertEqual(code, 0)
        self.assertTrue(any("WARNING:" in l and "TODO-CONFIRM" in l for l in lines))

    def test_todo_confirm_fails_with_strict(self):
        """TODO-CONFIRM document produces FAIL and exit code 1 with --strict."""
        reg = textwrap.dedent("""\
            documents:
              - document_id: ZTAX-TST-001
                title: "Test Document"
                class: S
                version: "1.0"
                status: TODO-CONFIRM
                owner: "Test Owner"
                approvers: []
                effective_date: null
                supersedes: null
                dependencies: []
                source_path: null
                publication_hash: null
                review_due: null
                applicable_release: ["all"]
                retention_policy: "PERMANENT"
        """)
        req = self.fix.minimal_req()
        code, lines = self.fix.run(reg, req, strict=True)
        self.assertEqual(code, 1)
        fails = self.fix.fail_lines(lines)
        self.assertTrue(any("--strict" in f and "TODO-CONFIRM" in f for f in fails))


if __name__ == "__main__":
    unittest.main()
