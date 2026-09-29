"""Generate a traceability report linking requirements to their verification evidence.

Reads docs/requirements.yaml and for each requirement:
  - TEST refs under intelligence/tests/: runs pytest for the named test functions,
    and uses --collect-only to detect any note-named tests not actually found in the
    file (sets partial_verification=true if any are missing).
  - TEST ref docs/test_validate_registry.py: runs via python -m unittest.
  - TEST refs under backend/: runs go test, detecting nested go.mod modules.
  - INSPECTION refs: confirms the file exists (NOT_INDEPENDENTLY_VERIFIED).
  - sdk/ refs: NOT_SUPPORTED (no runner configured yet).
  - null refs: NO_VERIFICATION.

Outputs:
  docs/evidence/traceability_report.json  -- full per-requirement detail (with timestamps)
  docs/evidence/traceability_summary.json -- counts + IDs, deterministic (no timestamps)

Ratchet / baseline check (docs/trace_baseline.json):
  On each run the script compares results_counts against the baseline and fails if:
    - PASS count decreased
    - NO_VERIFICATION count increased
    - (FAIL + ERROR) count increased
  If the baseline file does not exist, a warning is printed and the check is skipped.
  If results improved, a hint is printed to run --update-baseline.

CLI flags:
  --update-baseline   Write the current counts to docs/trace_baseline.json and exit 0
                      without performing the ratchet comparison.
"""
import sys
import argparse
import yaml
import subprocess
import os
import datetime
import json
import re


def _baseline_path(repo_root):
    return os.path.join(repo_root, "docs", "trace_baseline.json")


def _run_ratchet(results_counts, repo_root):
    """Compare results_counts against the baseline. Returns a list of regression notices."""
    bp = _baseline_path(repo_root)
    if not os.path.exists(bp):
        print(f"WARNING: baseline file {bp} not found; skipping ratchet check.")
        return []

    with open(bp, "r", encoding="utf-8") as f:
        baseline = json.load(f)

    bc = baseline.get("counts", {})
    regressions = []

    # Rule 1: PASS must not decrease
    if results_counts.get("PASS", 0) < bc.get("PASS", 0):
        regressions.append(
            f"NOTICE: Ratchet regression: PASS dropped from {bc.get('PASS', 0)} to "
            f"{results_counts.get('PASS', 0)} versus {bp}"
        )

    # Rule 2: NO_VERIFICATION must not increase
    if results_counts.get("NO_VERIFICATION", 0) > bc.get("NO_VERIFICATION", 0):
        regressions.append(
            f"NOTICE: Ratchet regression: NO_VERIFICATION increased from {bc.get('NO_VERIFICATION', 0)} to "
            f"{results_counts.get('NO_VERIFICATION', 0)} versus {bp}"
        )

    # Rule 3: (FAIL + ERROR) must not increase
    cur_bad = results_counts.get("FAIL", 0) + results_counts.get("ERROR", 0)
    base_bad = bc.get("FAIL", 0) + bc.get("ERROR", 0)
    if cur_bad > base_bad:
        regressions.append(
            f"NOTICE: Ratchet regression: (FAIL+ERROR) increased from {base_bad} to {cur_bad} versus {bp}"
        )

    if regressions:
        for msg in regressions:
            print(msg)

    # Check for improvements and hint
    improved = False
    if results_counts.get("PASS", 0) > bc.get("PASS", 0):
        improved = True
    if results_counts.get("NO_VERIFICATION", 0) < bc.get("NO_VERIFICATION", 0):
        improved = True
    if cur_bad < base_bad:
        improved = True
    if improved:
        print("Note: results improved vs baseline. Run --update-baseline to lock in the new baseline.")

    return regressions


def _write_baseline(results_counts, repo_root):
    bp = _baseline_path(repo_root)
    baseline = {"counts": {k: results_counts[k] for k in sorted(results_counts)}}
    with open(bp, "w", encoding="utf-8", newline="\n") as f:
        json.dump(baseline, f, indent=2, sort_keys=True)
        f.write("\n")
    print(f"Baseline written to {bp}")


def main():
    parser = argparse.ArgumentParser(description="Generate traceability report.")
    parser.add_argument(
        "--update-baseline",
        action="store_true",
        help="Write current counts to docs/trace_baseline.json and exit 0.",
    )
    args = parser.parse_args()

    repo_root = os.path.abspath(os.path.dirname(os.path.dirname(__file__)))
    requirements_path = os.path.join(repo_root, "docs", "requirements.yaml")

    try:
        with open(requirements_path, "r", encoding="utf-8") as f:
            data = yaml.safe_load(f)
    except Exception as e:
        print(f"Error reading requirements.yaml: {e}")
        sys.exit(0)

    report = []

    # get git commit
    try:
        git_commit = subprocess.check_output(
            ["git", "rev-parse", "--short", "HEAD"], cwd=repo_root
        ).decode("utf-8").strip()
    except Exception:
        git_commit = "unknown"

    fail_or_error_reqs = []
    results_counts = {
        "PASS": 0,
        "FAIL": 0,
        "ERROR": 0,
        "NOT_INDEPENDENTLY_VERIFIED": 0,
        "NOT_SUPPORTED": 0,
        "NO_VERIFICATION": 0,
    }

    for req in data.get("requirements", []):
        req_id = req["id"]
        doc_id = req.get("document_id")
        vm = req.get("verification_method")
        vr = req.get("verification_ref")
        note = req.get("note", "") or ""

        result = "NO_VERIFICATION"
        detail = "No verification ref provided."

        if vr is None:
            result = "NO_VERIFICATION"
            detail = "verification_ref is null."
            partial_verification = False
        elif vm == "INSPECTION":
            path = os.path.join(repo_root, vr.replace("/", os.sep))
            if os.path.exists(path):
                result = "NOT_INDEPENDENTLY_VERIFIED"
                detail = "file exists (this only confirms the file exists, not that the rule holds)"
            else:
                result = "ERROR"
                detail = "file not found"
            partial_verification = False
        elif vm == "TEST":
            # Case A: intelligence/tests/
            if vr.startswith("intelligence/tests/"):
                test_funcs = re.findall(r"test_[a-zA-Z0-9_]+", note)
                # rel_path is relative to intelligence/: e.g. "tests/test_classifier.py"
                rel_path = vr[len("intelligence/"):]
                cmd = ["python", "-m", "pytest"]
                if test_funcs:
                    for t in test_funcs:
                        cmd.append(f"{rel_path}::{t}")
                else:
                    cmd.append(rel_path)
                cmd.append("-q")

                # Collect real test names from file for partial_verification check
                collected_names = set()
                partial_verification = False
                try:
                    collect_proc = subprocess.run(
                        ["python", "-m", "pytest", "--collect-only", rel_path],
                        cwd=os.path.join(repo_root, "intelligence"),
                        capture_output=True, text=True
                    )
                    for line in collect_proc.stdout.splitlines():
                        # Try tree format: <Function test_xyz>
                        m = re.search(r"<Function\s+(test_[a-zA-Z0-9_]+)", line)
                        if m:
                            collected_names.add(m.group(1))
                        else:
                            # Fallback flat format: tests/test_classifier.py::test_xyz
                            m = re.search(r"::(test_[a-zA-Z0-9_]+)", line)
                            if m:
                                collected_names.add(m.group(1))
                    if test_funcs:
                        missing = [t for t in test_funcs if t not in collected_names]
                        if missing:
                            partial_verification = True
                except Exception:
                    partial_verification = True  # can't confirm if collection fails

                try:
                    proc = subprocess.run(
                        cmd, cwd=os.path.join(repo_root, "intelligence"),
                        capture_output=True, text=True
                    )
                    detail = proc.stdout + proc.stderr
                    if proc.returncode == 0:
                        result = "PASS"
                    else:
                        result = "FAIL"
                except Exception as e:
                    result = "ERROR"
                    detail = str(e)

            # Case B: docs/test_validate_registry.py
            elif vr == "docs/test_validate_registry.py":
                partial_verification = False
                cmd = ["python", "-m", "unittest", "docs.test_validate_registry"]
                try:
                    proc = subprocess.run(cmd, cwd=repo_root, capture_output=True, text=True)
                    detail = proc.stdout + proc.stderr
                    if proc.returncode == 0:
                        result = "PASS"
                    else:
                        result = "FAIL"
                except Exception as e:
                    result = "ERROR"
                    detail = str(e)

            # Go tests under backend/
            elif vr.startswith("backend/"):
                partial_verification = False
                # This logic has not been tested against a real nested go.mod case.
                # Review carefully before trusting it for a new backend test path.
                file_dir = os.path.join(repo_root, os.path.dirname(vr))
                if os.path.exists(os.path.join(file_dir, "go.mod")):
                    # The subdirectory is its own Go module — run `go test .` from there
                    cmd = ["go", "test", "-v", "."]
                    run_dir = file_dir
                else:
                    # Part of the main backend module — run from backend/
                    test_dir = "./" + os.path.dirname(vr[len("backend/"):])
                    cmd = ["go", "test", "-v", test_dir]
                    run_dir = os.path.join(repo_root, "backend")
                try:
                    proc = subprocess.run(cmd, cwd=run_dir, capture_output=True, text=True)
                    detail = proc.stdout + proc.stderr
                    if proc.returncode == 0:
                        result = "PASS"
                    else:
                        result = "FAIL"
                except Exception as e:
                    result = "ERROR"
                    detail = str(e)

            elif any(vr.startswith(p) for p in ("sdk/python/", "sdk/go/", "sdk/java/", "sdk/dotnet/")):
                partial_verification = False
                result = "NOT_SUPPORTED"
                detail = "No test runner configured for this SDK path yet."
            else:
                partial_verification = False
                result = "ERROR"
                detail = f"Could not guess test command for ref: {vr}"
        else:
            partial_verification = False
            result = "ERROR"
            detail = f"Unknown verification_method '{vm}' with non-null ref."

        report.append({
            "requirement_id": req_id,
            "document_id": doc_id,
            "verification_method": vm,
            "verification_ref": vr,
            "result": result,
            "partial_verification": partial_verification,
            "detail": detail.strip(),
            "git_commit": git_commit,
            "generated_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
        })

        results_counts[result] = results_counts.get(result, 0) + 1
        if result in ("FAIL", "ERROR"):
            fail_or_error_reqs.append({"id": req_id, "vr": vr, "result": result})

    # --update-baseline: write and exit, do not run ratchet
    if args.update_baseline:
        _write_baseline(results_counts, repo_root)
        sys.exit(0)

    # Write full report
    evidence_dir = os.path.join(repo_root, "docs", "evidence")
    os.makedirs(evidence_dir, exist_ok=True)
    out_path = os.path.join(evidence_dir, "traceability_report.json")
    with open(out_path, "w", encoding="utf-8", newline="\n") as f:
        json.dump(report, f, indent=2)
        f.write("\n")

    # Write deterministic summary file (no timestamps; sorted for byte-for-byte reproducibility)
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
    summary_path = os.path.join(evidence_dir, "traceability_summary.json")
    with open(summary_path, "w", encoding="utf-8", newline="\n") as f:
        json.dump(summary, f, indent=2, sort_keys=True)
        f.write("\n")

    # Print summary
    print(f"Traceability Report Generated: {out_path}")
    print("--- Summary ---")
    for k, v in results_counts.items():
        print(f"{k}: {v}")

    # Ratchet check
    ratchet_notices = _run_ratchet(results_counts, repo_root)

    for item in fail_or_error_reqs:
        # e.g., ::warning::Requirement REQ-1 verification FAILED (docs/some_file.py)
        res_word = "FAILED" if item["result"] == "FAIL" else "ERRORED"
        print(f"::warning::Requirement {item['id']} verification {res_word} ({item['vr']})")

    step_summary_file = os.environ.get("GITHUB_STEP_SUMMARY")
    if step_summary_file:
        with open(step_summary_file, "a", encoding="utf-8") as f:
            f.write("## Traceability Report Summary\n\n")
            f.write("| Result | Count |\n")
            f.write("|--------|-------|\n")
            for k in sorted(results_counts.keys()):
                f.write(f"| {k} | {results_counts[k]} |\n")
            f.write("\n")
            
            if ratchet_notices:
                for notice in ratchet_notices:
                    f.write(f"**NOTICE**: {notice}\n\n")
            
            if fail_or_error_reqs:
                f.write("### Failed / Errored Requirements\n\n")
                for item in fail_or_error_reqs:
                    f.write(f"- **{item['id']}** ({item['result']}): `{item['vr']}`\n")
            else:
                f.write("No requirements failed or errored.\n")

    print("\nTraceability report completed.")
    sys.exit(0)


if __name__ == "__main__":
    main()
