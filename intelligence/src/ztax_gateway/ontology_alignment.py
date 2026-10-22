"""Ontology-alignment checker.

Reports whether ontology Markdown headings and gold-set expected classes line up
using EXACTLY the matching rule that ``evaluation_quality.MetricEngine`` applies:

    ``heading.strip().lower() == expected_class.strip().lower()``

The checker does NOT fix anything.  It surfaces two distinct problems:

1. **Marker-only mismatch** -- a gold class would match a heading if only the
   leading ``#`` markers were removed (e.g. gold has
   ``"ontology:telecom/voice/mobile"`` but the heading stored by
   ``classifier.py`` is ``"# ontology:telecom/voice/mobile"``).  These cases
   can NEVER score today, because ``classifier.py`` sets
   ``ontology_class = result.chunk.heading`` verbatim and ``MetricEngine``
   compares that verbatim value against the gold class.

2. **Near-match** -- heading and gold class differ only by case, whitespace, or
   punctuation *after* marker removal, but are not exact after full
   normalisation.

Known quirk (mirrored from ``rag.py``): a line that starts with ``#`` inside a
Markdown fenced code block is treated as a heading because ``_HEADING_RE`` is
applied to the whole document with ``re.MULTILINE`` and does not track fence
state.  ``extract_headings`` preserves this behaviour intentionally so the
helper cannot drift from ``rag``.

Standard-library only at module level.  The CLI lazily imports the gold-set
loader module inside ``main()`` to avoid circular imports.
"""

from __future__ import annotations

import json
import re
import string
import sys
from dataclasses import dataclass
from pathlib import Path
from typing import TYPE_CHECKING

if TYPE_CHECKING:  # pragma: no cover
    pass  # no extra imports needed for type-checking only

__all__: list[str] = [
    "AlignmentReport",
    "check_alignment",
    "extract_headings",
    "format_report",
    "main",
    "matches",
]

# ---------------------------------------------------------------------------
# Heading extractor -- mirrors rag._HEADING_RE exactly
# ---------------------------------------------------------------------------

# Levels 1 to 3 only; the full heading line (including '#' characters) is kept.
# re.MULTILINE is required so '^' matches at every line start, not just the
# start of the string -- matching the rag.py constant exactly.
_HEADING_RE: re.Pattern[str] = re.compile(r"^#{1,3}\s+.+", re.MULTILINE)


def extract_headings(markdown: str) -> tuple[str, ...]:
    """Return every ATX heading (levels 1-3) found in *markdown*, in order.

    The full heading line is returned verbatim -- including the leading ``#``
    characters and any trailing whitespace stripped at the line boundary, which
    mirrors the ``Chunk.heading`` value that ``rag._chunk_document`` stores.

    Heading lines inside fenced code blocks are NOT excluded.  This is a known
    quirk that ``rag._chunk_document`` shares: ``_HEADING_RE`` is applied with
    ``re.MULTILINE`` over the full document text without fence-state tracking,
    so a ``#`` line inside a code fence counts as a heading here too.  The
    quirk is intentional here so this helper cannot drift from rag.

    The synthetic ``"Preamble"`` chunk that ``rag._chunk_document`` creates for
    content before the first heading is excluded: it is not a real heading.

    Args:
        markdown: The full text of a Markdown document.

    Returns:
        A tuple of heading strings in document order, each equal to the
        ``chunk.heading`` value that ``rag._chunk_document`` would assign.
    """
    results: list[str] = []
    for match in _HEADING_RE.finditer(markdown):
        line = match.group(0).rstrip("\r\n")
        results.append(line)
    return tuple(results)


# ---------------------------------------------------------------------------
# Matching rule -- mirrors MetricEngine exactly
# ---------------------------------------------------------------------------


def matches(heading: str, expected: str) -> bool:
    """Return True if *heading* matches *expected* using the MetricEngine rule.

    The rule is: ``heading.strip().lower() == expected.strip().lower()``.

    This is the EXACT comparison that ``MetricEngine.run`` performs:

        if predicted.strip().lower() == case.normalised_expected  (evaluation_quality.py:382)

    where ``case.normalised_expected = expected_class.strip().lower()``
    (evaluation_quality.py:138) and ``predicted = top.ontology_class`` which
    equals ``result.chunk.heading`` (classifier.py:275).

    Because ``classifier.py`` stores the raw heading line (including ``#``
    markers), a gold class ``"ontology:telecom/voice/mobile"`` can never match
    the heading ``"# ontology:telecom/voice/mobile"`` through this rule -- the
    strip() call removes only surrounding whitespace, not interior characters
    like ``#``.

    Args:
        heading:  The full heading line as stored in ``Chunk.heading``.
        expected: The ``expected_class`` string from a gold-set case.

    Returns:
        True if the heading and expected class match under the MetricEngine rule.
    """
    return heading.strip().lower() == expected.strip().lower()


# ---------------------------------------------------------------------------
# AlignmentReport
# ---------------------------------------------------------------------------


def _strip_markers(heading: str) -> str:
    """Remove leading '#' characters and the first following whitespace run."""
    return re.sub(r"^#+\s*", "", heading)


def _near_match(heading: str, expected: str) -> bool:
    """Return True if heading and expected differ only by case, whitespace, or
    punctuation AFTER the leading '#' markers are removed from the heading.

    This catches e.g. ``"# Ontology:Telecom/Voice/Mobile"`` vs
    ``"ontology:telecom/voice/mobile"`` -- the classes refer to the same node
    but the capitalisation or punctuation differs.
    """
    stripped_h = _strip_markers(heading)

    def _normalise(s: str) -> str:
        s = s.strip().lower()
        # Remove all punctuation to make the comparison robust.
        s = s.translate(str.maketrans("", "", string.punctuation))
        # Collapse internal whitespace.
        return " ".join(s.split())

    return _normalise(stripped_h) == _normalise(expected) and not matches(heading, expected)


@dataclass(frozen=True, slots=True)
class AlignmentReport:
    """The result of checking whether ontology headings and gold classes align.

    All tuple fields are **sorted** and **deduplicated** for deterministic output.

    Attributes:
        covered:
            Gold classes that match at least one heading under the MetricEngine
            rule (exact match after strip().lower()).
        marker_only:
            Gold classes that do NOT match any heading directly but WOULD match
            if the leading ``#`` markers were stripped from every heading first.
            These cases can never score today.
        near_matches:
            ``(gold_class, heading)`` pairs where the gold class and heading
            differ only by case, whitespace or punctuation after marker removal,
            but are not an exact match.  These are candidates for ontology or
            gold-set corrections.
        missing:
            Gold classes that have no match -- not even a marker-only or near
            match -- in the heading set.
        duplicate_headings:
            Heading strings that appear more than once in the ontology documents.
        non_canonical_headings:
            Headings whose stripped text does not appear in *canonical_ids*.
            Only populated when ``canonical_ids`` is supplied to
            ``check_alignment``.
        ok:
            True if every gold class is covered, no duplicates, and no
            non-canonical headings.
    """

    covered: tuple[str, ...]
    marker_only: tuple[str, ...]
    near_matches: tuple[tuple[str, str], ...]
    missing: tuple[str, ...]
    duplicate_headings: tuple[str, ...]
    non_canonical_headings: tuple[str, ...]

    @property
    def ok(self) -> bool:
        """True if the alignment is clean: all gold classes covered, no issues."""
        return (
            len(self.missing) == 0
            and len(self.marker_only) == 0
            and len(self.duplicate_headings) == 0
            and len(self.non_canonical_headings) == 0
        )


# ---------------------------------------------------------------------------
# check_alignment
# ---------------------------------------------------------------------------


def check_alignment(
    headings: tuple[str, ...],
    expected_classes: tuple[str, ...],
    canonical_ids: tuple[str, ...] | None = None,
) -> AlignmentReport:
    """Check whether *expected_classes* and *headings* align.

    Args:
        headings:
            Heading strings as returned by ``extract_headings``.  Each string
            includes the leading ``#`` markers.
        expected_classes:
            The ``expected_class`` strings from gold-set cases.  All cases are
            included regardless of review status (this is an alignment check,
            not an evidence check).
        canonical_ids:
            Optional set of canonical ontology identifiers (strings beginning
            with ``"ontology:"``).  When supplied, headings whose stripped text
            does not appear in this set are reported as
            ``non_canonical_headings``.

    Returns:
        A frozen :class:`AlignmentReport` with sorted, deterministic fields.
    """
    # --- Duplicates in the heading set ---
    seen_headings: set[str] = set()
    dup_set: set[str] = set()
    for h in headings:
        key = h.strip().lower()
        if key in seen_headings:
            dup_set.add(h)
        seen_headings.add(key)

    # Unique expected classes (preserve all, de-dup for reporting).
    unique_expected: list[str] = list(dict.fromkeys(expected_classes))

    covered: list[str] = []
    marker_only: list[str] = []
    near_matches_list: list[tuple[str, str]] = []
    missing: list[str] = []

    for exp in unique_expected:
        # 1. Direct match (MetricEngine rule).
        if any(matches(h, exp) for h in headings):
            covered.append(exp)
            continue

        # 2. Marker-only: strip '#' markers from every heading and try again.
        marker_stripped_matches = [
            h for h in headings if _strip_markers(h).strip().lower() == exp.strip().lower()
        ]
        if marker_stripped_matches:
            marker_only.append(exp)
            # Also collect near-matches found during marker check.
            for h in headings:
                if _near_match(h, exp):
                    pair = (exp, h)
                    if pair not in near_matches_list:
                        near_matches_list.append(pair)
            continue

        # 3. Near-match: differ only by case/whitespace/punctuation after strip.
        near_h = [h for h in headings if _near_match(h, exp)]
        if near_h:
            for h in near_h:
                pair = (exp, h)
                if pair not in near_matches_list:
                    near_matches_list.append(pair)
            missing.append(exp)  # still missing from MetricEngine perspective
            continue

        # 4. Truly missing.
        missing.append(exp)

    # --- Non-canonical headings ---
    non_canonical: list[str] = []
    if canonical_ids is not None:
        canonical_lower: set[str] = {c.strip().lower() for c in canonical_ids}
        for h in headings:
            stripped = _strip_markers(h).strip().lower()
            if stripped not in canonical_lower and h not in non_canonical:
                non_canonical.append(h)

    return AlignmentReport(
        covered=tuple(sorted(covered)),
        marker_only=tuple(sorted(marker_only)),
        near_matches=tuple(sorted(near_matches_list)),
        missing=tuple(sorted(missing)),
        duplicate_headings=tuple(sorted(dup_set)),
        non_canonical_headings=tuple(sorted(non_canonical)),
    )


# ---------------------------------------------------------------------------
# format_report
# ---------------------------------------------------------------------------


def format_report(report: AlignmentReport) -> str:
    """Return a human-readable, deterministic report string for *report*.

    The output is always deterministic because all tuple fields in
    :class:`AlignmentReport` are sorted.

    Args:
        report: The alignment report to format.

    Returns:
        A multi-line string suitable for printing to stdout.
    """
    lines: list[str] = []

    status = "OK" if report.ok else "PROBLEMS FOUND"
    lines.append(f"=== Ontology Alignment Report: {status} ===")
    lines.append("")

    lines.append(f"Covered ({len(report.covered)}): gold classes with a direct heading match")
    for cls in report.covered:
        lines.append(f"  + {cls}")

    lines.append("")
    lines.append(
        f"Marker-only ({len(report.marker_only)}): gold classes that match a heading ONLY"
        " after '#' markers are removed -- these CANNOT score today"
    )
    for cls in report.marker_only:
        lines.append(f"  ! {cls}")

    lines.append("")
    lines.append(
        f"Near-matches ({len(report.near_matches)}): gold class and heading differ only"
        " by case/whitespace/punctuation after marker removal"
    )
    for gold_cls, heading in report.near_matches:
        lines.append(f"  ~ gold:    {gold_cls!r}")
        lines.append(f"    heading: {heading!r}")

    lines.append("")
    lines.append(f"Missing ({len(report.missing)}): no heading at all (not even near-match)")
    for cls in report.missing:
        lines.append(f"  - {cls}")

    lines.append("")
    lines.append(f"Duplicate headings ({len(report.duplicate_headings)})")
    for h in report.duplicate_headings:
        lines.append(f"  = {h!r}")

    lines.append("")
    lines.append(f"Non-canonical headings ({len(report.non_canonical_headings)})")
    for h in report.non_canonical_headings:
        lines.append(f"  ? {h!r}")

    lines.append("")
    lines.append(f"Result: {'OK' if report.ok else 'FAIL'}")

    return "\n".join(lines)


# ---------------------------------------------------------------------------
# CLI helper -- canonical ID extraction from arbitrary JSON
# ---------------------------------------------------------------------------


def _extract_ontology_ids_from_json(data: object) -> list[str]:
    """Recursively collect every string beginning with ``"ontology:"`` found at
    any nesting level of *data* (the parsed JSON object).

    Args:
        data: Any Python object that came from ``json.loads``.

    Returns:
        Sorted, deduplicated list of ontology ID strings.
    """
    found: set[str] = set()

    def _walk(obj: object) -> None:
        if isinstance(obj, str):
            if obj.startswith("ontology:"):
                found.add(obj)
        elif isinstance(obj, dict):
            for v in obj.values():
                _walk(v)
        elif isinstance(obj, list):
            for item in obj:
                _walk(item)

    _walk(data)
    return sorted(found)


# ---------------------------------------------------------------------------
# Gold-set loader (stdlib only)
# ---------------------------------------------------------------------------


def _load_expected_classes(gold_path: Path) -> tuple[str, ...]:
    """Return ``expected_class`` values from a gold-set JSON file.

    Reads the file with the standard library only (no other ztax_gateway
    modules imported).  Requires a top-level ``"cases"`` list; every entry
    must have a non-empty string ``"expected_class"``.

    All cases are collected regardless of ``review_status``.

    Args:
        gold_path: Path to the gold-set JSON file.

    Raises:
        SystemExit: On any read error, invalid JSON, missing ``"cases"`` key,
            or a case with a missing / empty ``expected_class``.

    Returns:
        Tuple of ``expected_class`` strings in document order.
    """
    try:
        text = gold_path.read_text(encoding="utf-8")
    except OSError as exc:
        raise SystemExit(f"Cannot read gold file: {exc}") from exc

    try:
        data = json.loads(text)
    except json.JSONDecodeError as exc:
        raise SystemExit(f"Invalid JSON in gold file: {exc}") from exc

    if not isinstance(data, dict) or "cases" not in data:
        raise SystemExit('Gold file must have a top-level "cases" key.')

    cases = data["cases"]
    if not isinstance(cases, list):
        raise SystemExit('"cases" must be a JSON array.')

    result: list[str] = []
    for i, case in enumerate(cases):
        if not isinstance(case, dict):
            raise SystemExit(f"Case {i} is not a JSON object.")
        ec = case.get("expected_class")
        if not isinstance(ec, str) or not ec.strip():
            raise SystemExit(
                f'Case {i} is missing a non-empty string "expected_class".'
            )
        result.append(ec)
    return tuple(result)


# ---------------------------------------------------------------------------
# CLI entry-point
# ---------------------------------------------------------------------------


def main(argv: list[str] | None = None) -> int:
    """CLI entry-point for the ontology alignment checker.

    Usage::

        python -m ztax_gateway.ontology_alignment \\
            --gold <gold.json> \\
            [--ontology <file.md> ...] \\
            [--canonical-ids-json <path>]

    Exit code 0 if the report is OK, 1 if problems were found.

    Args:
        argv: Argument list (defaults to ``sys.argv[1:]``).

    Returns:
        Exit code (0 or 1).
    """
    import argparse  # stdlib -- only needed in CLI

    if argv is None:
        argv = sys.argv[1:]

    parser = argparse.ArgumentParser(
        prog="python -m ztax_gateway.ontology_alignment",
        description=(
            "Check whether ontology headings and gold-set expected classes align "
            "using the same rule as MetricEngine."
        ),
    )
    parser.add_argument(
        "--gold",
        required=True,
        metavar="PATH",
        help="Path to the gold-set JSON file.",
    )
    parser.add_argument(
        "--ontology",
        metavar="PATH",
        action="append",
        default=[],
        help="Path to an ontology Markdown file. May be repeated.",
    )
    parser.add_argument(
        "--canonical-ids-json",
        metavar="PATH",
        default=None,
        help=(
            "Path to a JSON file whose string values (at any nesting level) "
            "starting with 'ontology:' are used as canonical IDs."
        ),
    )

    args = parser.parse_args(argv)

    # 1. Load gold-set: all cases regardless of review status.
    expected_classes = _load_expected_classes(Path(args.gold))

    # 2. Load ontology headings.
    headings: list[str] = []
    for ont_path in args.ontology:
        text = Path(ont_path).read_text(encoding="utf-8")
        headings.extend(extract_headings(text))

    # 3. Load canonical IDs (optional).
    canonical_ids: tuple[str, ...] | None = None
    if args.canonical_ids_json:
        raw = Path(args.canonical_ids_json).read_bytes()
        data = json.loads(raw.decode("utf-8"))
        ids = _extract_ontology_ids_from_json(data)
        canonical_ids = tuple(ids)

    # 4. Check alignment and print report.
    report = check_alignment(tuple(headings), expected_classes, canonical_ids)
    print(format_report(report))

    return 0 if report.ok else 1


if __name__ == "__main__":
    sys.exit(main())
