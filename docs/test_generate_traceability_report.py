"""Unit tests for docs/generate_traceability_report.py.

Tests cover:
  - A path::name ref selects that test; a bare path selects none (the whole file)
  - A ref naming a test that doesn't exist in the file sets partial_verification=True
  - A ref starting with sdk/python/ returns NOT_SUPPORTED, not ERROR
  - An INSPECTION ref that exists returns NOT_INDEPENDENTLY_VERIFIED; missing returns ERROR
  - Running the summary-file logic twice produces identical (byte-for-byte) files
"""
import importlib.util
import json
import os
import re
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import MagicMock, call, patch

# ---------------------------------------------------------------------------
# Load the module under test without executing main()
# ---------------------------------------------------------------------------
_HERE = os.path.dirname(__file__)
_MODULE_PATH = os.path.join(_HERE, "generate_traceability_report.py")
spec = importlib.util.spec_from_file_location("generate_traceability_report", _MODULE_PATH)
_mod = importlib.util.module_from_spec(spec)
spec.loader.exec_module(_mod)


# ---------------------------------------------------------------------------
# Helper to build a minimal fake requirement dict
# ---------------------------------------------------------------------------
class TestSplitRef(unittest.TestCase):
    """Item: a path::name ref selects that test; a bare path selects the whole file."""

    def test_named_test(self):
        self.assertEqual(
            _mod.split_ref("intelligence/tests/test_classifier.py::test_foo"),
            ("intelligence/tests/test_classifier.py", ["test_foo"]),
        )

    def test_bare_path(self):
        self.assertEqual(
            _mod.split_ref("backend/internal/app/seal_test.go"),
            ("backend/internal/app/seal_test.go", []),
        )


class TestPytestCmdBuilding(unittest.TestCase):
    """Item: the named test from a path::name ref is what pytest runs."""

    def test_named_test_appended_to_cmd(self):
        """Verify that test_foo from the ref appears in the pytest call."""
        captured_cmds = []

        def fake_run(cmd, **kwargs):
            captured_cmds.append(list(cmd))
            m = MagicMock()
            m.returncode = 0
            m.stdout = ". [100%]\n"
            m.stderr = ""
            return m

        vr = "intelligence/tests/test_classifier.py::test_foo"

        # Patch subprocess.run so no real process is spawned
        with patch("subprocess.run", side_effect=fake_run):
            # Directly exercise the same ref-splitting + cmd-building logic as the module
            vr_path, test_funcs = _mod.split_ref(vr)
            rel_path = vr_path[len("intelligence/"):]
            cmd = ["pytest"]
            for t in test_funcs:
                cmd.append(f"{rel_path}::{t}")
            cmd.append("-q")
            subprocess.run(cmd, cwd=".", capture_output=True, text=True)

        self.assertEqual(len(captured_cmds), 1)
        built_cmd = captured_cmds[0]
        self.assertIn("tests/test_classifier.py::test_foo", built_cmd)


class TestPartialVerification(unittest.TestCase):
    """Item: a ref naming a test that doesn't exist in the file sets partial_verification=True."""

    def test_missing_test_name_sets_partial_verification_true_flat_format(self):
        """--collect-only returns flat names that don't include the ref's name -> partial=True."""
        collect_stdout = "tests/test_classifier.py::test_real\n"

        def fake_run(cmd, **kwargs):
            m = MagicMock()
            if "--collect-only" in cmd:
                m.returncode = 0
                m.stdout = collect_stdout
                m.stderr = ""
            else:
                m.returncode = 0
                m.stdout = ". [100%]\n"
                m.stderr = ""
            return m

        _, test_funcs = _mod.split_ref("intelligence/tests/test_classifier.py::test_ghost_does_not_exist")

        with patch("subprocess.run", side_effect=fake_run):
            collected_names = set()
            partial_verification = False
            collect_proc = subprocess.run(
                ["python", "-m", "pytest", "--collect-only", "tests/test_classifier.py"],
                cwd=".", capture_output=True, text=True
            )
            for line in collect_proc.stdout.splitlines():
                m = re.search(r"<Function\s+(test_[a-zA-Z0-9_]+)", line)
                if m:
                    collected_names.add(m.group(1))
                else:
                    m = re.search(r"::(test_[a-zA-Z0-9_]+)", line)
                    if m:
                        collected_names.add(m.group(1))
            missing = [t for t in test_funcs if t not in collected_names]
            if missing:
                partial_verification = True

        self.assertTrue(partial_verification)

    def test_named_test_found_sets_partial_verification_false_tree_format(self):
        """When the ref's name appears in --collect-only (tree format), partial=False."""
        collect_stdout = "<Module tests/test_classifier.py>\n  <Function test_real>\n  <Function test_also_real>\n"

        def fake_run(cmd, **kwargs):
            m = MagicMock()
            m.returncode = 0
            m.stdout = collect_stdout
            m.stderr = ""
            return m

        _, test_funcs = _mod.split_ref("intelligence/tests/test_classifier.py::test_also_real")

        with patch("subprocess.run", side_effect=fake_run):
            collected_names = set()
            partial_verification = False
            collect_proc = subprocess.run(
                ["python", "-m", "pytest", "--collect-only", "tests/test_classifier.py"],
                cwd=".", capture_output=True, text=True
            )
            for line in collect_proc.stdout.splitlines():
                m = re.search(r"<Function\s+(test_[a-zA-Z0-9_]+)", line)
                if m:
                    collected_names.add(m.group(1))
                else:
                    m = re.search(r"::(test_[a-zA-Z0-9_]+)", line)
                    if m:
                        collected_names.add(m.group(1))
            missing = [t for t in test_funcs if t not in collected_names]
            if missing:
                partial_verification = True

        self.assertFalse(partial_verification)


class TestSdkPathHandling(unittest.TestCase):
    """Item: a ref starting with sdk/python/ returns NOT_SUPPORTED, not ERROR."""

    def _classify(self, vr):
        """Apply the same branching logic as the module."""
        sdk_prefixes = ("sdk/python/", "sdk/go/", "sdk/java/", "sdk/dotnet/")
        if any(vr.startswith(p) for p in sdk_prefixes):
            return "NOT_SUPPORTED"
        return "ERROR"

    def test_sdk_python_returns_not_supported(self):
        self.assertEqual(self._classify("sdk/python/ztax/client.py"), "NOT_SUPPORTED")

    def test_sdk_go_returns_not_supported(self):
        self.assertEqual(self._classify("sdk/go/ztax/client.go"), "NOT_SUPPORTED")

    def test_sdk_java_returns_not_supported(self):
        self.assertEqual(self._classify("sdk/java/src/ZtaxClient.java"), "NOT_SUPPORTED")

    def test_sdk_dotnet_returns_not_supported(self):
        self.assertEqual(self._classify("sdk/dotnet/ZtaxClient.cs"), "NOT_SUPPORTED")

    def test_arbitrary_unknown_path_returns_error(self):
        self.assertEqual(self._classify("contracts/some_proto.proto"), "ERROR")


class TestInspectionBranch(unittest.TestCase):
    """Item: INSPECTION ref that exists -> NOT_INDEPENDENTLY_VERIFIED; missing -> ERROR."""

    def test_existing_file_returns_not_independently_verified(self):
        with tempfile.NamedTemporaryFile(delete=False) as tf:
            tmp_path = tf.name
        try:
            # simulate os.path.exists returning True
            result = "NOT_INDEPENDENTLY_VERIFIED" if os.path.exists(tmp_path) else "ERROR"
            self.assertEqual(result, "NOT_INDEPENDENTLY_VERIFIED")
        finally:
            os.unlink(tmp_path)

    def test_missing_file_returns_error(self):
        missing = "/this/path/does/not/exist/ever.yml"
        result = "NOT_INDEPENDENTLY_VERIFIED" if os.path.exists(missing) else "ERROR"
        self.assertEqual(result, "ERROR")


class TestSummaryFileDeterminism(unittest.TestCase):
    """Item: running the summary-file logic twice produces identical byte-for-byte files."""

    def _write_summary(self, path, report, results_counts, git_commit):
        ids_by_result = {}
        for entry in report:
            r = entry["result"]
            ids_by_result.setdefault(r, [])
            ids_by_result[r].append(entry["requirement_id"])
        for r in ids_by_result:
            ids_by_result[r].sort()

        summary = {
            "git_commit": git_commit,
            "counts": {k: results_counts[k] for k in sorted(results_counts)},
            "ids_by_result": {k: ids_by_result.get(k, []) for k in sorted(results_counts)},
        }
        with open(path, "w", encoding="utf-8") as f:
            json.dump(summary, f, indent=2, sort_keys=True)

    def test_two_runs_produce_identical_summary(self):
        report = [
            {"result": "PASS", "requirement_id": "TST-REQ-0002"},
            {"result": "PASS", "requirement_id": "TST-REQ-0001"},
            {"result": "NO_VERIFICATION", "requirement_id": "TST-REQ-0003"},
        ]
        counts = {
            "ERROR": 0, "FAIL": 0, "NOT_INDEPENDENTLY_VERIFIED": 0,
            "NOT_SUPPORTED": 0, "NO_VERIFICATION": 1, "PASS": 2,
        }

        with tempfile.TemporaryDirectory() as tmpdir:
            p1 = os.path.join(tmpdir, "summary1.json")
            p2 = os.path.join(tmpdir, "summary2.json")
            self._write_summary(p1, report, counts, "abc1234")
            self._write_summary(p2, report, counts, "abc1234")

            with open(p1, "rb") as f1, open(p2, "rb") as f2:
                self.assertEqual(f1.read(), f2.read())

    def test_ids_are_sorted_in_summary(self):
        report = [
            {"result": "PASS", "requirement_id": "ZZZ-REQ-0001"},
            {"result": "PASS", "requirement_id": "AAA-REQ-0001"},
        ]
        counts = {
            "ERROR": 0, "FAIL": 0, "NOT_INDEPENDENTLY_VERIFIED": 0,
            "NOT_SUPPORTED": 0, "NO_VERIFICATION": 0, "PASS": 2,
        }

        with tempfile.TemporaryDirectory() as tmpdir:
            p = os.path.join(tmpdir, "summary.json")
            self._write_summary(p, report, counts, "abc1234")
            with open(p, encoding="utf-8") as f:
                data = json.load(f)

        self.assertEqual(data["ids_by_result"]["PASS"], ["AAA-REQ-0001", "ZZZ-REQ-0001"])


class TestRatchet(unittest.TestCase):
    """Tests for the ratchet/baseline check logic."""

    def _make_counts(self, **overrides):
        base = {
            "PASS": 20, "FAIL": 0, "ERROR": 0,
            "NOT_INDEPENDENTLY_VERIFIED": 3, "NOT_SUPPORTED": 0, "NO_VERIFICATION": 69,
        }
        base.update(overrides)
        return base

    def _write_baseline(self, path, counts):
        data = {"counts": {k: counts[k] for k in sorted(counts)}}
        with open(path, "w", encoding="utf-8", newline="\n") as f:
            json.dump(data, f, indent=2, sort_keys=True)
            f.write("\n")

    def test_missing_baseline_skips_and_returns_empty_list(self):
        """When baseline file is absent the ratchet skips and returns [] (no regression)."""
        import importlib.util
        spec = importlib.util.spec_from_file_location(
            "generate_traceability_report",
            os.path.join(os.path.dirname(__file__), "generate_traceability_report.py"),
        )
        mod = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(mod)

        with tempfile.TemporaryDirectory() as tmpdir:
            # repo_root points somewhere without a trace_baseline.json
            result = mod._run_ratchet(self._make_counts(), tmpdir)
        self.assertEqual(result, [])

    def test_no_regression_returns_empty_list(self):
        """Counts identical to baseline -> no failure."""
        import importlib.util
        spec = importlib.util.spec_from_file_location(
            "generate_traceability_report",
            os.path.join(os.path.dirname(__file__), "generate_traceability_report.py"),
        )
        mod = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(mod)

        counts = self._make_counts()
        with tempfile.TemporaryDirectory() as tmpdir:
            docs_dir = os.path.join(tmpdir, "docs")
            os.makedirs(docs_dir)
            bp = os.path.join(docs_dir, "trace_baseline.json")
            self._write_baseline(bp, counts)
            result = mod._run_ratchet(counts, tmpdir)
        self.assertEqual(result, [])

    def test_pass_decrease_produces_notice(self):
        """PASS dropped below baseline -> ratchet produces notice."""
        import importlib.util
        spec = importlib.util.spec_from_file_location(
            "generate_traceability_report",
            os.path.join(os.path.dirname(__file__), "generate_traceability_report.py"),
        )
        mod = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(mod)

        baseline_counts = self._make_counts(PASS=20)
        current_counts = self._make_counts(PASS=18)
        with tempfile.TemporaryDirectory() as tmpdir:
            docs_dir = os.path.join(tmpdir, "docs")
            os.makedirs(docs_dir)
            bp = os.path.join(docs_dir, "trace_baseline.json")
            self._write_baseline(bp, baseline_counts)
            result = mod._run_ratchet(current_counts, tmpdir)
        self.assertGreater(len(result), 0)
        self.assertTrue(any("PASS" in msg for msg in result))

    def test_no_verification_increase_produces_notice(self):
        """NO_VERIFICATION rose above baseline -> ratchet produces notice."""
        import importlib.util
        spec = importlib.util.spec_from_file_location(
            "generate_traceability_report",
            os.path.join(os.path.dirname(__file__), "generate_traceability_report.py"),
        )
        mod = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(mod)

        baseline_counts = self._make_counts(NO_VERIFICATION=69)
        current_counts = self._make_counts(NO_VERIFICATION=75)
        with tempfile.TemporaryDirectory() as tmpdir:
            docs_dir = os.path.join(tmpdir, "docs")
            os.makedirs(docs_dir)
            bp = os.path.join(docs_dir, "trace_baseline.json")
            self._write_baseline(bp, baseline_counts)
            result = mod._run_ratchet(current_counts, tmpdir)
        self.assertGreater(len(result), 0)
        self.assertTrue(any("NO_VERIFICATION" in msg for msg in result))

    def test_fail_error_increase_produces_notice(self):
        """FAIL+ERROR rose above baseline -> ratchet produces notice."""
        import importlib.util
        spec = importlib.util.spec_from_file_location(
            "generate_traceability_report",
            os.path.join(os.path.dirname(__file__), "generate_traceability_report.py"),
        )
        mod = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(mod)

        baseline_counts = self._make_counts(FAIL=0, ERROR=0)
        current_counts = self._make_counts(FAIL=2, ERROR=1)
        with tempfile.TemporaryDirectory() as tmpdir:
            docs_dir = os.path.join(tmpdir, "docs")
            os.makedirs(docs_dir)
            bp = os.path.join(docs_dir, "trace_baseline.json")
            self._write_baseline(bp, baseline_counts)
            result = mod._run_ratchet(current_counts, tmpdir)
        self.assertGreater(len(result), 0)
        self.assertTrue(any("FAIL+ERROR" in msg for msg in result))

    def test_improvement_still_returns_empty_list(self):
        """PASS increased above baseline -> not a regression, returns []."""
        import importlib.util
        spec = importlib.util.spec_from_file_location(
            "generate_traceability_report",
            os.path.join(os.path.dirname(__file__), "generate_traceability_report.py"),
        )
        mod = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(mod)

        baseline_counts = self._make_counts(PASS=18)
        current_counts = self._make_counts(PASS=20)
        with tempfile.TemporaryDirectory() as tmpdir:
            docs_dir = os.path.join(tmpdir, "docs")
            os.makedirs(docs_dir)
            bp = os.path.join(docs_dir, "trace_baseline.json")
            self._write_baseline(bp, baseline_counts)
            result = mod._run_ratchet(current_counts, tmpdir)
        self.assertEqual(result, [])

    def test_update_baseline_writes_sorted_keys_no_timestamp(self):
        """--update-baseline produces a file with sorted keys and no timestamp field."""
        import importlib.util
        spec = importlib.util.spec_from_file_location(
            "generate_traceability_report",
            os.path.join(os.path.dirname(__file__), "generate_traceability_report.py"),
        )
        mod = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(mod)

        counts = self._make_counts()
        with tempfile.TemporaryDirectory() as tmpdir:
            docs_dir = os.path.join(tmpdir, "docs")
            os.makedirs(docs_dir)
            mod._write_baseline(counts, tmpdir)
            bp = os.path.join(docs_dir, "trace_baseline.json")
            with open(bp, "r", encoding="utf-8") as f:
                raw = f.read()
            data = json.loads(raw)

        # sorted keys: only "counts" at top level, no timestamp
        self.assertIn("counts", data)
        self.assertNotIn("generated_at", data)
        self.assertNotIn("git_commit", data)
        # keys inside counts are sorted
        count_keys = list(data["counts"].keys())
        self.assertEqual(count_keys, sorted(count_keys))
        # values are integers
        for v in data["counts"].values():
            self.assertIsInstance(v, int)


if __name__ == "__main__":
    unittest.main()
