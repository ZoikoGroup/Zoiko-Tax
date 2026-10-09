"""docs/tools/build_release_manifest.py's own suite.

Same reasoning as test_check_quarantine.py: every behavior the tool claims
has a case here, built against small fixture inputs in a temp directory --
never the real repo state, so this suite stays independent of how much
traceability/waiver/quarantine data actually exists today.

    python -m unittest discover -s docs/tools/tests
"""

from __future__ import annotations

import contextlib
import copy
import io
import json
import shutil
import sys
import tempfile
import unittest
from pathlib import Path

import yaml

HERE = Path(__file__).resolve().parent
REPO = HERE.parents[2]
sys.path.insert(0, str(HERE.parent))

import build_release_manifest as brm  # noqa: E402

GENERATED_AT = "2026-10-09T12:00:00Z"
COMMIT = "a" * 40


def base_summary() -> dict:
    return {
        "counts": {
            "PASS": 2, "FAIL": 0, "ERROR": 0, "NOT_SUPPORTED": 0,
            "NOT_INDEPENDENTLY_VERIFIED": 0, "NO_VERIFICATION": 1,
        },
        "git_commit": "deadbee",
        "ids_by_result": {
            "PASS": ["ZTAX-TST-REQ-0001", "ZTAX-TST-REQ-0002"],
            "FAIL": [], "ERROR": [], "NOT_SUPPORTED": [], "NOT_INDEPENDENTLY_VERIFIED": [],
            "NO_VERIFICATION": ["ZTAX-TST-REQ-0003"],
        },
    }


def base_waivers() -> dict:
    return {"waivers": [
        {
            "waiver_id": "ZTAX-WVR-2026-0001", "requirement_id": "ZTAX-TST-REQ-0001",
            "scope": "s", "reason": "r", "risk": "k", "risk_owner": "o",
            "compensating_control": "c", "expires_at": "2026-12-31T23:59:59Z",
            "approvers": ["A", "B"], "approved_at": "2026-10-01",
        },
    ]}


def base_quarantine() -> dict:
    return {"quarantine": [
        {
            "quarantine_id": "ZTAX-QUAR-2026-0001", "test_ref": "backend/x_test.go::TestY",
            "owner": "o", "defect": "JIRA-1", "reason": "r", "quarantined_at": "2026-10-01",
            "expires_at": "2026-11-01", "coverage_impact": "i", "requirement_ids": [],
        },
    ]}


class Fixture:
    """A throwaway repository: the real schema plus small source registers."""

    def __init__(self) -> None:
        self.dir = Path(tempfile.mkdtemp(prefix="ztax-release-manifest-"))
        (self.dir / "docs" / "schema").mkdir(parents=True)
        shutil.copy(
            REPO / "docs" / "schema" / "release_manifest.schema.json",
            self.dir / "docs" / "schema" / "release_manifest.schema.json",
        )
        (self.dir / "docs" / "evidence").mkdir(parents=True)
        self.summary = base_summary()
        self.waivers = base_waivers()
        self.quarantine = base_quarantine()

    def write_sources(self) -> None:
        (self.dir / "docs" / "evidence" / "traceability_summary.json").write_text(
            json.dumps(self.summary), encoding="utf-8")
        (self.dir / "docs" / "waivers.yaml").write_text(
            yaml.safe_dump(self.waivers, sort_keys=False), encoding="utf-8")
        (self.dir / "docs" / "quarantine.yaml").write_text(
            yaml.safe_dump(self.quarantine, sort_keys=False), encoding="utf-8")

    def schema(self) -> dict:
        with (self.dir / "docs" / "schema" / "release_manifest.schema.json").open(encoding="utf-8") as f:
            return json.load(f)

    def close(self) -> None:
        shutil.rmtree(self.dir, ignore_errors=True)


# ---------------------------------------------------------------------------
# assemble() -- pure build logic, no file writing
# ---------------------------------------------------------------------------


class Assemble(unittest.TestCase):
    def setUp(self) -> None:
        self.fx = Fixture()
        self.fx.write_sources()

    def tearDown(self) -> None:
        self.fx.close()

    def test_normal_assemble(self) -> None:
        m = brm.assemble(self.fx.dir, "1.2.3", COMMIT, GENERATED_AT, None)
        self.assertEqual(m["version"], "1.2.3")
        self.assertEqual(m["commit"], COMMIT)
        self.assertEqual(m["generated_at"], GENERATED_AT)
        self.assertIsNone(m["rollback_ref"])
        self.assertEqual(m["waivers"]["count"], 1)
        self.assertEqual(m["quarantine"]["count"], 1)
        self.assertEqual(m["traceability"], self.fx.summary)
        self.assertRegex(m["manifest_hash"], r"^[0-9a-f]{64}$")
        findings = brm.check_manifest(m, self.fx.schema(), "test")
        self.assertEqual(findings.errors, [])

    def test_hash_deterministic_for_same_inputs_and_generated_at(self) -> None:
        m1 = brm.assemble(self.fx.dir, "1.2.3", COMMIT, GENERATED_AT, None)
        m2 = brm.assemble(self.fx.dir, "1.2.3", COMMIT, GENERATED_AT, None)
        self.assertEqual(m1["manifest_hash"], m2["manifest_hash"])

    def test_missing_summary_file_raises_with_clear_message(self) -> None:
        (self.fx.dir / "docs" / "evidence" / "traceability_summary.json").unlink()
        with self.assertRaises(FileNotFoundError) as ctx:
            brm.assemble(self.fx.dir, "1.2.3", COMMIT, GENERATED_AT, None)
        self.assertIn("generate_traceability_report.py", str(ctx.exception))

    def test_rollback_ref_is_carried_through(self) -> None:
        m = brm.assemble(self.fx.dir, "1.2.3", COMMIT, GENERATED_AT, "docs/runbooks/rollback.md")
        self.assertEqual(m["rollback_ref"], "docs/runbooks/rollback.md")


# ---------------------------------------------------------------------------
# tamper-evidence: changing any field after the fact breaks verify
# ---------------------------------------------------------------------------


class TamperDetection(unittest.TestCase):
    def setUp(self) -> None:
        self.fx = Fixture()
        self.fx.write_sources()
        self.manifest = brm.assemble(self.fx.dir, "1.2.3", COMMIT, GENERATED_AT, "r")
        self.schema = self.fx.schema()

    def tearDown(self) -> None:
        self.fx.close()

    def assertTamperDetected(self, mutated: dict) -> None:
        findings = brm.check_manifest(mutated, self.schema, "test")
        self.assertTrue(
            any("manifest_hash mismatch" in e for e in findings.errors),
            f"expected a hash-mismatch finding, got {findings.errors}",
        )

    def test_intact_manifest_passes(self) -> None:
        findings = brm.check_manifest(self.manifest, self.schema, "test")
        self.assertEqual(findings.errors, [])

    def test_tamper_version(self) -> None:
        m = copy.deepcopy(self.manifest)
        m["version"] = "9.9.9"
        self.assertTamperDetected(m)

    def test_tamper_commit(self) -> None:
        m = copy.deepcopy(self.manifest)
        m["commit"] = "b" * 40
        self.assertTamperDetected(m)

    def test_tamper_generated_at(self) -> None:
        m = copy.deepcopy(self.manifest)
        m["generated_at"] = "2099-01-01T00:00:00Z"
        self.assertTamperDetected(m)

    def test_tamper_rollback_ref(self) -> None:
        m = copy.deepcopy(self.manifest)
        m["rollback_ref"] = "something-else"
        self.assertTamperDetected(m)

    def test_tamper_embedded_traceability(self) -> None:
        m = copy.deepcopy(self.manifest)
        m["traceability"]["counts"]["PASS"] = 999
        self.assertTamperDetected(m)

    def test_tamper_waivers_count(self) -> None:
        m = copy.deepcopy(self.manifest)
        m["waivers"]["count"] = 0
        self.assertTamperDetected(m)

    def test_tamper_embedded_waiver_entry(self) -> None:
        m = copy.deepcopy(self.manifest)
        m["waivers"]["list"][0]["scope"] = "a different scope"
        self.assertTamperDetected(m)

    def test_tamper_embedded_quarantine_entry(self) -> None:
        m = copy.deepcopy(self.manifest)
        m["quarantine"]["entries"][0]["owner"] = "someone else"
        self.assertTamperDetected(m)

    def test_tamper_manifest_hash_itself(self) -> None:
        m = copy.deepcopy(self.manifest)
        m["manifest_hash"] = "0" * 64
        self.assertTamperDetected(m)


# ---------------------------------------------------------------------------
# schema rejection
# ---------------------------------------------------------------------------


class MalformedSources(unittest.TestCase):
    """docs/waivers.yaml and docs/quarantine.yaml must each be a mapping
    with a list under their expected key. A missing key, a non-mapping
    document or a non-list value is a build error, not a quiet empty
    register -- only an actually-empty list is."""

    def setUp(self) -> None:
        self.fx = Fixture()
        self.fx.write_sources()

    def tearDown(self) -> None:
        self.fx.close()

    def _write(self, rel: str, text: str) -> None:
        (self.fx.dir / rel).write_text(text, encoding="utf-8")

    def test_waivers_key_missing_raises(self) -> None:
        self._write("docs/waivers.yaml", "not_waivers: []\n")
        with self.assertRaises(brm.ManifestSourceError) as ctx:
            brm.assemble(self.fx.dir, "1.2.3", COMMIT, GENERATED_AT, None)
        self.assertIn("waivers", str(ctx.exception))

    def test_quarantine_key_missing_raises(self) -> None:
        self._write("docs/quarantine.yaml", "not_quarantine: []\n")
        with self.assertRaises(brm.ManifestSourceError) as ctx:
            brm.assemble(self.fx.dir, "1.2.3", COMMIT, GENERATED_AT, None)
        self.assertIn("quarantine", str(ctx.exception))

    def test_waivers_value_not_a_list_raises(self) -> None:
        self._write("docs/waivers.yaml", "waivers: not-a-list\n")
        with self.assertRaises(brm.ManifestSourceError) as ctx:
            brm.assemble(self.fx.dir, "1.2.3", COMMIT, GENERATED_AT, None)
        self.assertIn("must be a list", str(ctx.exception))

    def test_quarantine_value_not_a_list_raises(self) -> None:
        self._write("docs/quarantine.yaml", "quarantine: 42\n")
        with self.assertRaises(brm.ManifestSourceError) as ctx:
            brm.assemble(self.fx.dir, "1.2.3", COMMIT, GENERATED_AT, None)
        self.assertIn("must be a list", str(ctx.exception))

    def test_waivers_top_level_not_a_mapping_raises(self) -> None:
        self._write("docs/waivers.yaml", "- just\n- a\n- list\n")
        with self.assertRaises(brm.ManifestSourceError) as ctx:
            brm.assemble(self.fx.dir, "1.2.3", COMMIT, GENERATED_AT, None)
        self.assertIn("must be a mapping", str(ctx.exception))

    def test_quarantine_top_level_not_a_mapping_raises(self) -> None:
        self._write("docs/quarantine.yaml", "just a scalar\n")
        with self.assertRaises(brm.ManifestSourceError) as ctx:
            brm.assemble(self.fx.dir, "1.2.3", COMMIT, GENERATED_AT, None)
        self.assertIn("must be a mapping", str(ctx.exception))

    def test_empty_lists_still_build_with_count_zero(self) -> None:
        self._write("docs/waivers.yaml", "waivers: []\n")
        self._write("docs/quarantine.yaml", "quarantine: []\n")
        m = brm.assemble(self.fx.dir, "1.2.3", COMMIT, GENERATED_AT, None)
        self.assertEqual(m["waivers"]["count"], 0)
        self.assertEqual(m["quarantine"]["count"], 0)

    def test_cli_build_fails_cleanly_on_malformed_waivers(self) -> None:
        self._write("docs/waivers.yaml", "waivers: not-a-list\n")
        out = self.fx.dir / "out" / "release_manifest.json"
        rc = brm.main([
            "--root", str(self.fx.dir), "build", "--version", "1.2.3", "--commit", COMMIT,
            "--out", str(out), "--generated-at", GENERATED_AT,
        ])
        self.assertEqual(rc, 1)
        self.assertFalse(out.exists())

    def test_cli_build_fails_cleanly_on_missing_quarantine_key(self) -> None:
        self._write("docs/quarantine.yaml", "wrong_key: []\n")
        out = self.fx.dir / "out" / "release_manifest.json"
        rc = brm.main([
            "--root", str(self.fx.dir), "build", "--version", "1.2.3", "--commit", COMMIT,
            "--out", str(out), "--generated-at", GENERATED_AT,
        ])
        self.assertEqual(rc, 1)
        self.assertFalse(out.exists())

    def test_cli_build_fails_cleanly_on_non_mapping_waivers(self) -> None:
        self._write("docs/waivers.yaml", "- just\n- a\n- list\n")
        out = self.fx.dir / "out" / "release_manifest.json"
        rc = brm.main([
            "--root", str(self.fx.dir), "build", "--version", "1.2.3", "--commit", COMMIT,
            "--out", str(out), "--generated-at", GENERATED_AT,
        ])
        self.assertEqual(rc, 1)
        self.assertFalse(out.exists())


# ---------------------------------------------------------------------------
# known-answer: pins the canonicalization itself, not just "it's stable"
# ---------------------------------------------------------------------------


class KnownAnswerHash(unittest.TestCase):
    """One hard-coded SHA-256 over fixed fixture inputs. This pins the
    canonicalization rules (sorted keys, separators (",", ":"), UTF-8) --
    not just that the hash is internally self-consistent, which the
    tamper-detection tests above already cover. KNOWN_HASH must only change
    on purpose: if this test breaks, either the fixtures below changed, or
    canonical_bytes()/compute_hash() changed, and either is worth noticing
    explicitly rather than silently re-pinning."""

    # Computed once via assemble() with: version "1.2.3", commit COMMIT,
    # generated_at GENERATED_AT, rollback_ref "r", and exactly the
    # base_summary() / base_waivers() / base_quarantine() fixtures above.
    KNOWN_HASH = "719f2d1787b1db15a7f996690756fa5cc056fc6e8a57a040b5c892ae7fc52747"

    def setUp(self) -> None:
        self.fx = Fixture()
        self.fx.write_sources()

    def tearDown(self) -> None:
        self.fx.close()

    def test_known_answer(self) -> None:
        m = brm.assemble(self.fx.dir, "1.2.3", COMMIT, GENERATED_AT, "r")
        self.assertEqual(m["manifest_hash"], self.KNOWN_HASH)


# ---------------------------------------------------------------------------
# the real docs/waivers.yaml and docs/quarantine.yaml in this repo
# ---------------------------------------------------------------------------


class RealRegistersInRepo(unittest.TestCase):
    """Builds against the REAL docs/waivers.yaml and docs/quarantine.yaml.
    Only the traceability snapshot is a fixture, since that file is
    generated by a separate tool and is not guaranteed to exist (or to have
    any particular content) in a fresh checkout."""

    def setUp(self) -> None:
        self.fx = Fixture()  # schema + docs/evidence dir, but no write_sources()
        shutil.copy(REPO / "docs" / "waivers.yaml", self.fx.dir / "docs" / "waivers.yaml")
        shutil.copy(REPO / "docs" / "quarantine.yaml", self.fx.dir / "docs" / "quarantine.yaml")
        (self.fx.dir / "docs" / "evidence" / "traceability_summary.json").write_text(
            json.dumps(base_summary()), encoding="utf-8")

    def tearDown(self) -> None:
        self.fx.close()

    def test_real_registers_build_and_verify(self) -> None:
        m = brm.assemble(self.fx.dir, "1.2.3", COMMIT, GENERATED_AT, "r")
        findings = brm.check_manifest(m, self.fx.schema(), "test")
        self.assertEqual(findings.errors, [])

        real_waivers = yaml.safe_load((REPO / "docs" / "waivers.yaml").read_text(encoding="utf-8"))
        real_quarantine = yaml.safe_load((REPO / "docs" / "quarantine.yaml").read_text(encoding="utf-8"))
        self.assertEqual(m["waivers"]["count"], len(real_waivers["waivers"]))
        self.assertEqual(m["quarantine"]["count"], len(real_quarantine["quarantine"]))


class SchemaRejection(unittest.TestCase):
    def setUp(self) -> None:
        self.fx = Fixture()
        self.fx.write_sources()
        self.manifest = brm.assemble(self.fx.dir, "1.2.3", COMMIT, GENERATED_AT, None)
        self.schema = self.fx.schema()

    def tearDown(self) -> None:
        self.fx.close()

    def test_unknown_field_rejected(self) -> None:
        m = copy.deepcopy(self.manifest)
        m["extra_field"] = "not allowed"
        findings = brm.check_manifest(m, self.schema, "test")
        self.assertTrue(any("schema:" in e for e in findings.errors), findings.errors)

    def test_bad_hash_format_rejected(self) -> None:
        m = copy.deepcopy(self.manifest)
        m["manifest_hash"] = "not-hex"
        findings = brm.check_manifest(m, self.schema, "test")
        self.assertTrue(any("schema:" in e for e in findings.errors), findings.errors)

    def test_bad_commit_length_rejected(self) -> None:
        m = copy.deepcopy(self.manifest)
        m["commit"] = "abc"
        findings = brm.check_manifest(m, self.schema, "test")
        self.assertTrue(any("schema:" in e for e in findings.errors), findings.errors)


# ---------------------------------------------------------------------------
# CLI round trip
# ---------------------------------------------------------------------------


class CliBuildAndVerify(unittest.TestCase):
    def setUp(self) -> None:
        self.fx = Fixture()
        self.fx.write_sources()

    def tearDown(self) -> None:
        self.fx.close()

    def test_build_then_verify_round_trip(self) -> None:
        out = self.fx.dir / "out" / "release_manifest.json"
        rc = brm.main([
            "--root", str(self.fx.dir), "build", "--version", "1.2.3", "--commit", COMMIT,
            "--out", str(out), "--generated-at", GENERATED_AT, "--rollback-ref", "r",
        ])
        self.assertEqual(rc, 0)
        self.assertTrue(out.is_file())

        text = out.read_text(encoding="utf-8")
        self.assertTrue(text.endswith("\n"), "output file must end with a newline")
        json.loads(text)  # must be valid JSON

        rc = brm.main(["--root", str(self.fx.dir), "verify", str(out)])
        self.assertEqual(rc, 0)

    def test_build_then_tamper_then_verify_fails(self) -> None:
        out = self.fx.dir / "out" / "release_manifest.json"
        brm.main([
            "--root", str(self.fx.dir), "build", "--version", "1.2.3", "--commit", COMMIT,
            "--out", str(out), "--generated-at", GENERATED_AT, "--rollback-ref", "r",
        ])
        text = out.read_text(encoding="utf-8")
        tampered = text.replace('"1.2.3"', '"1.2.4"')
        self.assertNotEqual(text, tampered, "fixture did not actually change anything")
        out.write_text(tampered, encoding="utf-8")

        rc = brm.main(["--root", str(self.fx.dir), "verify", str(out)])
        self.assertEqual(rc, 1)

    def test_missing_summary_file_fails_build_cleanly(self) -> None:
        (self.fx.dir / "docs" / "evidence" / "traceability_summary.json").unlink()
        out = self.fx.dir / "out" / "release_manifest.json"
        rc = brm.main([
            "--root", str(self.fx.dir), "build", "--version", "1.2.3", "--commit", COMMIT,
            "--out", str(out), "--generated-at", GENERATED_AT,
        ])
        self.assertEqual(rc, 1)
        self.assertFalse(out.exists())

    def test_null_rollback_ref_warns_but_succeeds(self) -> None:
        out = self.fx.dir / "out" / "release_manifest.json"
        buf = io.StringIO()
        with contextlib.redirect_stdout(buf):
            rc = brm.main([
                "--root", str(self.fx.dir), "build", "--version", "1.2.3", "--commit", COMMIT,
                "--out", str(out), "--generated-at", GENERATED_AT,
            ])
        self.assertEqual(rc, 0)
        self.assertIn("WARNING: QA-REQ-0112", buf.getvalue())
        self.assertTrue(out.is_file())
        manifest = json.loads(out.read_text(encoding="utf-8"))
        self.assertIsNone(manifest["rollback_ref"])

    def test_require_rollback_fails_without_one(self) -> None:
        out = self.fx.dir / "out" / "release_manifest.json"
        rc = brm.main([
            "--root", str(self.fx.dir), "build", "--version", "1.2.3", "--commit", COMMIT,
            "--out", str(out), "--generated-at", GENERATED_AT, "--require-rollback",
        ])
        self.assertEqual(rc, 1)
        self.assertFalse(out.exists())

    def test_require_rollback_succeeds_with_one(self) -> None:
        out = self.fx.dir / "out" / "release_manifest.json"
        rc = brm.main([
            "--root", str(self.fx.dir), "build", "--version", "1.2.3", "--commit", COMMIT,
            "--out", str(out), "--generated-at", GENERATED_AT,
            "--rollback-ref", "docs/runbooks/rollback.md", "--require-rollback",
        ])
        self.assertEqual(rc, 0)


if __name__ == "__main__":
    unittest.main()
