"""Gold-set status, coverage and review-sheet tool.

Reports facts about a gold-set file: case counts, ontology coverage (when an
approved list is supplied), and a CSV review worksheet.  The tool is strictly
read-only: it never edits the gold file, never marks anything REVIEWED, and
never defines thresholds or scores.

Usage
-----
  python tools/gold_set_status.py --gold PATH
      [--approved-list PATH --as-of YYYY-MM-DD]
      [--review-sheet OUT.csv [--force]]
"""

from __future__ import annotations

import argparse
import csv
import sys
from collections import Counter
from datetime import date
from pathlib import Path

# ---------------------------------------------------------------------------
# Spreadsheet-injection prefix characters (CSV safety)
# ---------------------------------------------------------------------------
_INJECTION_PREFIXES: tuple[str, ...] = ("=", "+", "-", "@")

# Review-sheet column headers
_SHEET_COLUMNS: list[str] = [
    "case_id",
    "sku_description",
    "expected_class",
    "notes",
    "origin",
    "author",
    "current_review_status",
    # Last five columns are always empty — filled by the human reviewer.
    "reviewer_decision",
    "reviewer_name",
    "reviewed_on",
    "source_ref",
    "reviewer_comments",
]

# Number of reviewer-only columns written empty.
_EMPTY_COLUMNS: int = 5


# ---------------------------------------------------------------------------
# Spreadsheet-injection safety helper
# ---------------------------------------------------------------------------


def _safe_cell(text: str) -> str:
    """Prefix any cell that starts with an injection character with ``'``."""
    stripped = text.lstrip()
    if stripped and stripped[0] in _INJECTION_PREFIXES:
        return "'" + text
    return text


# ---------------------------------------------------------------------------
# Status output
# ---------------------------------------------------------------------------


def _print_status(gsf: object) -> None:  # gsf: GoldSetFile
    from ztax_gateway.gold_set_file import Origin, ReviewStatus

    cases = gsf.cases  # type: ignore[attr-defined]
    total = len(cases)

    # Counts by review_status
    by_status: Counter[str] = Counter(c.review_status.value for c in cases)
    # Counts by origin
    by_origin: Counter[str] = Counter(c.origin.value for c in cases)
    # Cases per expected_class
    by_class: Counter[str] = Counter(c.expected_class for c in cases)
    # Empty source_ref
    empty_source_ref = sum(1 for c in cases if not c.source_ref)
    # REVIEWED cases with empty reviewer
    reviewed_empty_reviewer = sum(
        1
        for c in cases
        if c.review_status == ReviewStatus.REVIEWED and not c.reviewer
    )

    print(f"gold set: {gsf.name!r} version {gsf.version}")  # type: ignore[attr-defined]
    print(f"  total cases     : {total}")
    for status in sorted(by_status):
        print(f"  {status:<16}: {by_status[status]}")
    print()
    for origin in sorted(by_origin):
        print(f"  {origin:<24}: {by_origin[origin]}")
    print()
    print(f"  distinct expected_class values: {len(by_class)}")
    for cls in sorted(by_class):
        print(f"    {cls}: {by_class[cls]}")
    print()
    print(f"  empty source_ref            : {empty_source_ref}")
    print(f"  REVIEWED with empty reviewer: {reviewed_empty_reviewer}")
    print()

    # READINESS line
    eligible = sum(
        1
        for c in cases
        if c.review_status == ReviewStatus.REVIEWED
        and c.origin != Origin.SYNTHETIC
    )
    if eligible == 0:
        print(
            "READINESS: not usable as evaluation evidence"
            " (no REVIEWED case with a non-SYNTHETIC origin)"
        )
    else:
        print(
            f"READINESS: {eligible} case(s) are eligible as evaluation evidence;"
            " the rest are not"
        )


# ---------------------------------------------------------------------------
# Coverage output
# ---------------------------------------------------------------------------


def _print_coverage(gsf: object, approved_list: object, as_of: date) -> None:
    from ztax_gateway.gold_set_file import ReviewStatus
    from ztax_gateway.ontology_guard import check_code

    cases = gsf.cases  # type: ignore[attr-defined]
    al = approved_list

    # Map expected_class -> review statuses in this gold set
    classes_in_gold: dict[str, set[str]] = {}
    for c in cases:
        classes_in_gold.setdefault(c.expected_class, set()).add(c.review_status.value)

    # Collect all approved codes
    approved_codes: list[str] = [e.code for e in al.entries]  # type: ignore[attr-defined]

    # Codes on the approved list with no gold case at all
    no_case = [code for code in approved_codes if code not in classes_in_gold]
    # Codes on the approved list with no REVIEWED gold case
    no_reviewed = [
        code
        for code in approved_codes
        if ReviewStatus.REVIEWED.value not in classes_in_gold.get(code, set())
    ]

    # Verdict count over gold expected_class values
    verdict_counts: Counter[str] = Counter()
    for cls in classes_in_gold:
        v = check_code(cls, as_of, al)
        verdict_counts[v.value] += 1

    print("--- COVERAGE ---")
    print(f"Approved list version: {al.list_version}")  # type: ignore[attr-defined]
    print(f"As-of date           : {as_of}")
    print()
    print(f"Approved codes with no gold case   : {len(no_case)}")
    for code in sorted(no_case):
        print(f"  {code}")
    print()
    print(f"Approved codes with no REVIEWED case: {len(no_reviewed)}")
    for code in sorted(no_reviewed):
        print(f"  {code}")
    print()
    print("Gold expected_class verdict counts:")
    for verdict in sorted(verdict_counts):
        print(f"  {verdict:<22}: {verdict_counts[verdict]}")


# ---------------------------------------------------------------------------
# Review sheet
# ---------------------------------------------------------------------------


def _write_review_sheet(
    gsf: object, out_path: Path, *, force: bool
) -> int:
    if out_path.exists() and not force:
        print(
            f"error: {out_path.name} already exists; use --force to overwrite.",
            file=sys.stderr,
        )
        return 1

    try:
        with out_path.open("w", newline="", encoding="utf-8") as fh:
            writer = csv.writer(fh)
            writer.writerow(_SHEET_COLUMNS)
            for c in gsf.cases:  # type: ignore[attr-defined]
                row = [
                    _safe_cell(c.case_id),
                    _safe_cell(c.sku_description),
                    _safe_cell(c.expected_class),
                    _safe_cell(c.notes),
                    _safe_cell(c.origin.value),
                    _safe_cell(c.author),
                    _safe_cell(c.review_status.value),
                    # Last five reviewer columns — always empty
                    "",
                    "",
                    "",
                    "",
                    "",
                ]
                writer.writerow(row)
    except OSError:
        print("error: could not write review sheet", file=sys.stderr)
        return 1

    print(f"Review sheet written to: {out_path.name}")
    return 0


# ---------------------------------------------------------------------------
# main()
# ---------------------------------------------------------------------------


def main(argv: list[str] | None = None) -> int:
    """CLI entry point.  Returns exit code 0 on success, 1 on error."""
    parser = argparse.ArgumentParser(
        prog="gold_set_status",
        description="Gold-set status, coverage and review-sheet tool (read-only).",
    )
    parser.add_argument(
        "--gold",
        type=Path,
        required=True,
        help="Path to the gold-set JSON file.",
    )
    parser.add_argument(
        "--approved-list",
        type=Path,
        default=None,
        dest="approved_list",
        help="Path to an approved-ontology-list JSON (requires --as-of).",
    )
    parser.add_argument(
        "--as-of",
        type=str,
        default=None,
        dest="as_of",
        help="YYYY-MM-DD reference date for the approved list (required with --approved-list).",
    )
    parser.add_argument(
        "--review-sheet",
        type=Path,
        default=None,
        dest="review_sheet",
        help="Write a CSV review worksheet to this path.",
    )
    parser.add_argument(
        "--force",
        action="store_true",
        default=False,
        help="Overwrite --review-sheet file if it already exists.",
    )

    args = parser.parse_args(argv)

    # Validate --approved-list / --as-of pairing
    if (args.approved_list is None) != (args.as_of is None):
        print(
            "error: --approved-list and --as-of must be given together or not at all.",
            file=sys.stderr,
        )
        return 1

    # Parse as_of date
    as_of_date: date | None = None
    if args.as_of is not None:
        try:
            as_of_date = date.fromisoformat(args.as_of)
        except ValueError:
            print(
                f"error: --as-of {args.as_of!r} is not a valid YYYY-MM-DD date.",
                file=sys.stderr,
            )
            return 1

    # Load gold set
    try:
        from ztax_gateway.gold_set_file import GoldSetFileError, load_gold_set_file

        gsf = load_gold_set_file(args.gold)
    except GoldSetFileError:
        print("error: could not load gold set", file=sys.stderr)
        return 1
    except OSError:
        print("error: could not load gold set", file=sys.stderr)
        return 1

    # Load approved list (optional)
    approved_list = None
    if args.approved_list is not None:
        from ztax_gateway.ontology_guard import OntologyGuardError, load_approved_list

        try:
            payload = args.approved_list.read_bytes()
            approved_list = load_approved_list(payload)
        except (OntologyGuardError, OSError, UnicodeDecodeError):
            print("error: could not load approved list", file=sys.stderr)
            return 1

    # --- STATUS (always) ---
    _print_status(gsf)

    # --- COVERAGE (only when approved list given) ---
    if approved_list is not None and as_of_date is not None:
        _print_coverage(gsf, approved_list, as_of_date)

    # --- REVIEW SHEET (optional) ---
    if args.review_sheet is not None:
        rc = _write_review_sheet(gsf, args.review_sheet, force=args.force)
        if rc != 0:
            return rc

    return 0


if __name__ == "__main__":
    sys.exit(main())
