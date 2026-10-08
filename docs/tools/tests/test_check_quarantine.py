"""The quarantine gate's own suite.

Same reasoning as test_check_registry.py: every rule in check_quarantine.py
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

import check_quarantine  # noqa: E402

TODAY = dt.date(2026, 10, 8)

TEST_FILE = "backend/internal/domain/foo/foo_test.go"
TEST_FILE_CONTENT_NO_SKIP = "package foo\n\nfunc TestFlaky(t *testing.T) {}\n"


def _skip_call_content(marker_id: str) -> str:
    return f'package foo\n\nfunc TestFlaky(t *testing.T) {{\n\tt.Skip("QUARANTINE: {marker_id}")\n}}\n'


def _comment_only_content(marker_id: str) -> str:
    return f"// QUARANTINE: {marker_id}\n" + TEST_FILE_CONTENT_NO_SKIP


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
        self.dir = Path(tempfile.mkdtemp(prefix="ztax-quarantine-"))
        (self.dir / "docs" / "schema").mkdir(parents=True)
        for name in ("quarantine.schema.json", "requirements.schema.json"):
            shutil.copy(REPO / "docs" / "schema" / name, self.dir / "docs" / "schema" / name)
        self.register: dict = {"quarantine": []}
        self.requirements = base_requirements()

    def write(self, path: str, text: str) -> None:
        p = self.dir / path
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text(text, encoding="utf-8", newline="\n")

    def plant_test_file(self, rel: str = TEST_FILE, marker_id: str | None = "ZTAX-QUAR-2026-0001") -> None:
        """A real skip call carrying the marker — the only shape that
        actually quarantines a test."""
        content = _skip_call_content(marker_id) if marker_id else TEST_FILE_CONTENT_NO_SKIP
        self.write(rel, content)

    def run(self, today: dt.date = TODAY) -> list[str]:
        self.write("docs/quarantine.yaml", yaml.safe_dump(self.register, sort_keys=False, allow_unicode=True))
        self.write("docs/requirements.yaml", yaml.safe_dump(self.requirements, sort_keys=False, allow_unicode=True))
        return check_quarantine.run(self.dir, today).errors

    def close(self) -> None:
        shutil.rmtree(self.dir, ignore_errors=True)


class QuarantineGate(unittest.TestCase):
    def setUp(self) -> None:
        self.fx = Fixture()

    def tearDown(self) -> None:
        self.fx.close()

    def assertFinding(self, needle: str) -> None:
        errors = self.fx.run()
        self.assertTrue(any(needle in e for e in errors), f"expected a finding containing {needle!r}, got {errors}")

    # ---- the baseline --------------------------------------------------

    def test_empty_register_passes(self) -> None:
        self.assertEqual(self.fx.run(), [])

    def test_the_real_register_is_green(self) -> None:
        errors = check_quarantine.run(REPO, dt.datetime.now(dt.UTC).date()).errors
        self.assertEqual(errors, [])

    def test_valid_entry_passes(self) -> None:
        self.fx.plant_test_file()
        self.fx.register["quarantine"].append(self.entry())
        self.assertEqual(self.fx.run(), [])

    # ---- schema / duplicate IDs ----------------------------------------

    def test_schema_invalid(self) -> None:
        e = self.entry()
        del e["owner"]
        self.fx.register["quarantine"].append(e)
        self.assertFinding("schema:")

    def test_duplicate_id(self) -> None:
        self.fx.plant_test_file()
        self.fx.register["quarantine"] += [self.entry(), self.entry()]
        self.assertFinding("ZTAX-QUAR-2026-0001 is registered twice")

    # ---- expiry ----------------------------------------------------------

    def test_expired(self) -> None:
        self.fx.plant_test_file()
        self.fx.register["quarantine"].append(self.entry(expires_at="2026-09-01"))
        self.assertFinding("ZTAX-QUAR-2026-0001 expired at 2026-09-01")

    # ---- empty owner/reason/defect/coverage_impact ------------------------

    def test_empty_owner(self) -> None:
        self.fx.plant_test_file()
        self.fx.register["quarantine"].append(self.entry(owner="   "))
        self.assertFinding("ZTAX-QUAR-2026-0001 has no owner")

    def test_empty_reason(self) -> None:
        self.fx.plant_test_file()
        self.fx.register["quarantine"].append(self.entry(reason="   "))
        self.assertFinding("ZTAX-QUAR-2026-0001 has no reason")

    def test_empty_defect(self) -> None:
        self.fx.plant_test_file()
        self.fx.register["quarantine"].append(self.entry(defect="   "))
        self.assertFinding("ZTAX-QUAR-2026-0001 has no defect")

    def test_empty_coverage_impact(self) -> None:
        self.fx.plant_test_file()
        self.fx.register["quarantine"].append(self.entry(coverage_impact="   "))
        self.assertFinding("ZTAX-QUAR-2026-0001 has no coverage_impact")

    # ---- test_ref must exist ----------------------------------------------

    def test_missing_test_ref(self) -> None:
        self.fx.register["quarantine"].append(self.entry())
        self.assertFinding(f"ZTAX-QUAR-2026-0001 test_ref {TEST_FILE} does not exist")

    def test_test_ref_outside_scanned_trees(self) -> None:
        # The path exists, so it clears the existence check, but it is not
        # under any tree this checker's marker scan actually reaches.
        self.fx.write("frontend/src/foo.test.ts", "test content\n")
        self.fx.register["quarantine"].append(self.entry(test_ref="frontend/src/foo.test.ts"))
        self.assertFinding(
            "ZTAX-QUAR-2026-0001 test_ref frontend/src/foo.test.ts is outside the scanned test trees "
            "(backend/, intelligence/tests/, sdk/, contracts/); it cannot be verified"
        )

    # ---- requirement_ids must be registered --------------------------------

    def test_requirement_ids_names_unregistered_requirement(self) -> None:
        self.fx.plant_test_file()
        self.fx.register["quarantine"].append(self.entry(requirement_ids=["ZTAX-TST-REQ-0099"]))
        self.assertFinding(
            "ZTAX-QUAR-2026-0001 requirement_ids names ZTAX-TST-REQ-0099 which is not in docs/requirements.yaml"
        )

    def test_requirement_ids_registered_passes(self) -> None:
        self.fx.plant_test_file()
        self.fx.register["quarantine"].append(self.entry(requirement_ids=["ZTAX-TST-REQ-0001"]))
        self.assertEqual(self.fx.run(), [])

    # ---- sole verification (QA-REQ-0105) -----------------------------------

    def test_sole_verification_exact_match(self) -> None:
        self.fx.plant_test_file()
        self.fx.requirements["requirements"][0]["verification_ref"] = f"{TEST_FILE}::TestFlaky"
        self.fx.register["quarantine"].append(self.entry(test_ref=f"{TEST_FILE}::TestFlaky"))
        self.assertFinding("ZTAX-QUAR-2026-0001 is the sole verification_ref for ZTAX-TST-REQ-0001 (QA-REQ-0105)")

    def test_sole_verification_file_level_quarantine(self) -> None:
        # Quarantining the whole file (bare test_ref, no ::TestName) still
        # removes a specific test's verification inside it.
        self.fx.plant_test_file()
        self.fx.requirements["requirements"][0]["verification_ref"] = f"{TEST_FILE}::TestFlaky"
        self.fx.register["quarantine"].append(self.entry(test_ref=TEST_FILE))
        self.assertFinding("ZTAX-QUAR-2026-0001 is the sole verification_ref for ZTAX-TST-REQ-0001 (QA-REQ-0105)")

    # ---- the marker/register round trip ------------------------------------

    def test_marker_not_registered(self) -> None:
        # A real skip call exists in source for an ID nobody registered.
        self.fx.write(TEST_FILE, _skip_call_content("ZTAX-QUAR-2026-0099"))
        self.assertFinding(f"{TEST_FILE}:4: QUARANTINE marker ZTAX-QUAR-2026-0099 is not in docs/quarantine.yaml")

    def test_register_entry_without_marker(self) -> None:
        # The file exists, but carries no QUARANTINE marker at all.
        self.fx.write(TEST_FILE, TEST_FILE_CONTENT_NO_SKIP)
        self.fx.register["quarantine"].append(self.entry())
        self.assertFinding(f"ZTAX-QUAR-2026-0001 has no QUARANTINE: ZTAX-QUAR-2026-0001 marker in {TEST_FILE}")

    def test_register_entry_marker_in_wrong_file(self) -> None:
        # The marker exists, but in a different file than the one registered.
        self.fx.write(TEST_FILE, TEST_FILE_CONTENT_NO_SKIP)
        self.fx.write("backend/internal/domain/bar/bar_test.go", _skip_call_content("ZTAX-QUAR-2026-0001"))
        self.fx.register["quarantine"].append(self.entry())
        self.assertFinding(f"ZTAX-QUAR-2026-0001 has no QUARANTINE: ZTAX-QUAR-2026-0001 marker in {TEST_FILE}")

    # ---- the marker must be inside a real skip call ------------------------

    def test_comment_only_marker_is_a_finding(self) -> None:
        # A marker in a comment, with no skip call on the same line: the
        # test would still run, so this is not a quarantine.
        self.fx.write(TEST_FILE, _comment_only_content("ZTAX-QUAR-2026-0001"))
        self.fx.register["quarantine"].append(self.entry())
        self.assertFinding(
            f"{TEST_FILE}:1: QUARANTINE marker is not inside a skip call, the test would still run"
        )

    def test_marker_inside_real_skip_call_is_not_a_finding(self) -> None:
        self.fx.plant_test_file()
        self.fx.register["quarantine"].append(self.entry())
        errors = self.fx.run()
        self.assertFalse(
            any("is not inside a skip call" in e for e in errors), errors
        )

    def test_commented_out_skip_call_is_still_a_finding(self) -> None:
        # Item 2: a commented-out t.Skip( is text, not code. The test still
        # runs, so the marker is still a finding even though SKIP_CALL would
        # otherwise match the line.
        self.fx.write(
            TEST_FILE,
            'package foo\n\nfunc TestFlaky(t *testing.T) {\n\t// t.Skip("QUARANTINE: ZTAX-QUAR-2026-0001")\n}\n',
        )
        self.fx.register["quarantine"].append(self.entry())
        self.assertFinding(
            f"{TEST_FILE}:4: QUARANTINE marker is not inside a skip call, the test would still run"
        )

    # ---- JavaScript / node:test (contracts/, sdk/typescript) ---------------
    # Confirmed against this repo's actual tests: contracts/tools/
    # lint.test.mjs and sdk/typescript/test/client.test.js both call bare
    # test() from "node:test", not describe/it, and neither skips anything
    # yet.

    def test_js_test_skip_is_recognised(self) -> None:
        rel = "contracts/tools/flaky.test.mjs"
        self.fx.write(
            rel,
            'import { test } from "node:test";\n'
            'test.skip("QUARANTINE: ZTAX-QUAR-2026-0002", () => {});\n',
        )
        self.fx.register["quarantine"].append(self.entry(quarantine_id="ZTAX-QUAR-2026-0002", test_ref=rel))
        errors = self.fx.run()
        self.assertFalse(any("is not inside a skip call" in e for e in errors), errors)
        self.assertFalse(any("has no QUARANTINE" in e for e in errors), errors)

    def test_js_comment_only_marker_still_fails(self) -> None:
        rel = "contracts/tools/flaky.test.mjs"
        self.fx.write(
            rel,
            'import { test } from "node:test";\n'
            "// QUARANTINE: ZTAX-QUAR-2026-0002\n"
            'test("still runs", () => {});\n',
        )
        self.fx.register["quarantine"].append(self.entry(quarantine_id="ZTAX-QUAR-2026-0002", test_ref=rel))
        self.assertFinding(f"{rel}:2: QUARANTINE marker is not inside a skip call, the test would still run")

    def test_js_option_object_skip_form_is_recognised(self) -> None:
        rel = "contracts/tools/flaky2.test.mjs"
        self.fx.write(
            rel,
            'import { test } from "node:test";\n'
            'test("flaky", { skip: "QUARANTINE: ZTAX-QUAR-2026-0003" }, () => {});\n',
        )
        self.fx.register["quarantine"].append(self.entry(quarantine_id="ZTAX-QUAR-2026-0003", test_ref=rel))
        errors = self.fx.run()
        self.assertFalse(any("is not inside a skip call" in e for e in errors), errors)
        self.assertFalse(any("has no QUARANTINE" in e for e in errors), errors)

    # ---- Java / Kotlin JUnit 5 (sdk/java) -----------------------------------
    # Confirmed against this repo's actual tests: sdk/java's pom.xml pins
    # junit.version 5.14.4 and its tests import org.junit.jupiter.api.Test
    # -- JUnit 5 Jupiter, so @Disabled is correct, not JUnit 4's @Ignore.
    # No @Disabled used yet.

    def test_java_disabled_is_recognised(self) -> None:
        rel = "sdk/java/src/test/java/com/zoikotax/sdk/FlakyTest.java"
        self.fx.write(
            rel,
            "import org.junit.jupiter.api.Disabled;\n"
            "import org.junit.jupiter.api.Test;\n"
            "class FlakyTest {\n"
            '    @Disabled("QUARANTINE: ZTAX-QUAR-2026-0004")\n'
            "    @Test\n"
            "    void flaky() {}\n"
            "}\n",
        )
        self.fx.register["quarantine"].append(self.entry(quarantine_id="ZTAX-QUAR-2026-0004", test_ref=rel))
        errors = self.fx.run()
        self.assertFalse(any("is not inside a skip call" in e for e in errors), errors)
        self.assertFalse(any("has no QUARANTINE" in e for e in errors), errors)

    def test_java_comment_only_marker_still_fails(self) -> None:
        rel = "sdk/java/src/test/java/com/zoikotax/sdk/FlakyTest.java"
        self.fx.write(
            rel,
            "import org.junit.jupiter.api.Test;\n"
            "class FlakyTest {\n"
            "    // QUARANTINE: ZTAX-QUAR-2026-0004\n"
            "    @Test\n"
            "    void flaky() {}\n"
            "}\n",
        )
        self.fx.register["quarantine"].append(self.entry(quarantine_id="ZTAX-QUAR-2026-0004", test_ref=rel))
        self.assertFinding(f"{rel}:3: QUARANTINE marker is not inside a skip call, the test would still run")

    # ---- .NET xUnit (sdk/dotnet) ---------------------------------------------
    # Confirmed against this repo's actual tests: sdk/dotnet's ClientTests.cs
    # uses xUnit's [Fact(DisplayName = "...")] throughout; no Skip = "..."
    # used yet.

    def test_dotnet_skip_is_recognised(self) -> None:
        rel = "sdk/dotnet/tests/ZoikoTax.Sdk.Tests/FlakyTests.cs"
        self.fx.write(
            rel,
            "public class FlakyTests {\n"
            '    [Fact(Skip = "QUARANTINE: ZTAX-QUAR-2026-0005")]\n'
            "    public void Flaky() {}\n"
            "}\n",
        )
        self.fx.register["quarantine"].append(self.entry(quarantine_id="ZTAX-QUAR-2026-0005", test_ref=rel))
        errors = self.fx.run()
        self.assertFalse(any("is not inside a skip call" in e for e in errors), errors)
        self.assertFalse(any("has no QUARANTINE" in e for e in errors), errors)

    def test_dotnet_comment_only_marker_still_fails(self) -> None:
        rel = "sdk/dotnet/tests/ZoikoTax.Sdk.Tests/FlakyTests.cs"
        self.fx.write(
            rel,
            "public class FlakyTests {\n"
            "    // QUARANTINE: ZTAX-QUAR-2026-0005\n"
            "    [Fact]\n"
            "    public void Flaky() {}\n"
            "}\n",
        )
        self.fx.register["quarantine"].append(self.entry(quarantine_id="ZTAX-QUAR-2026-0005", test_ref=rel))
        self.assertFinding(f"{rel}:2: QUARANTINE marker is not inside a skip call, the test would still run")

    # ---- helpers ------------------------------------------------------------

    @staticmethod
    def entry(**overrides: object) -> dict:
        e = {
            "quarantine_id": "ZTAX-QUAR-2026-0001",
            "test_ref": f"{TEST_FILE}::TestFlaky",
            "owner": "Head of Testing",
            "defect": "JIRA-1234",
            "reason": "Intermittent timeout under -race",
            "quarantined_at": "2026-10-01",
            "expires_at": "2026-12-01",
            "coverage_impact": "No other test covers this path",
        }
        e.update(overrides)
        return e


if __name__ == "__main__":
    unittest.main()
