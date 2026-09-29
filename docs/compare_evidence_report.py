import json
import sys
import subprocess
import os

def strip_ts(entries):
    return [{k: v for k, v in e.items() if k != "generated_at"} for e in entries]

def main():
    repo_root = os.path.abspath(os.path.dirname(os.path.dirname(__file__)))
    try:
        committed_raw = subprocess.check_output(
            ["git", "show", "HEAD:docs/evidence/traceability_report.json"],
            cwd=repo_root
        )
        committed = json.loads(committed_raw)
    except subprocess.CalledProcessError as e:
        print(f"ERROR: could not read committed docs/evidence/traceability_report.json from git HEAD. {e}")
        sys.exit(1)
    except Exception as e:
        print(f"ERROR: failed to parse committed report. {e}")
        sys.exit(1)

    report_path = os.path.join(repo_root, "docs", "evidence", "traceability_report.json")
    try:
        with open(report_path, "r", encoding="utf-8") as f:
            fresh = json.load(f)
    except FileNotFoundError:
        print(f"ERROR: fresh report not found at {report_path}.")
        sys.exit(1)
    except Exception as e:
        print(f"ERROR: failed to read fresh report. {e}")
        sys.exit(1)

    if strip_ts(committed) != strip_ts(fresh):
        print("ERROR: committed traceability_report.json differs from fresh run (excluding timestamps).")
        print("Re-run 'python docs/generate_traceability_report.py' locally and commit the updated file.")
        sys.exit(1)

    print("OK: traceability_report.json matches committed version.")
    sys.exit(0)

if __name__ == "__main__":
    main()
