#!/usr/bin/env python3
"""
ZoikoTax registry validator — docs/validate_registry.py

Checks per ZTAX-GOV-001 v3.0 §4.4, §9 and §19:

  CHECK                           RULE
  ──────────────────────────────────────────────────────────────────────
  register.yaml statuses          §4.4 document lifecycle set
  requirements.yaml statuses      §9.2 requirement status set (different)
  APPROVED/EFFECTIVE → review_due §9.4 expired or null review_due
  Unique requirement IDs          §9.4
  document_id in register         §9.4
  id prefix matches document_id   §9.4 (against actual register IDs)
  Non-empty owner                 §9.4
  verification_ref is plain path  task rule (no spaces / description text)
  verification_ref exists on disk task rule
  BASELINED req → doc ≥ BFB       §9.4 document-status coupling
  EFFECTIVE req → doc = EFFECTIVE §9.4 document-status coupling + non-null ref
  APPROVED/EFFECTIVE doc with     §9.4
    null review_due

Document status set (GOV §4.4) + TODO-CONFIRM sentinel:
  PROPOSED | DRAFT | BASELINED-FOR-BUILD | APPROVED | EFFECTIVE |
  SUPERSEDED | WITHDRAWN | TODO-CONFIRM

Requirement status set (GOV §9.2) — intentionally different from document set:
  PROPOSED | BASELINED | EFFECTIVE | RETIRED

Usage:
    python docs/validate_registry.py [--root <dir>] [--today YYYY-MM-DD] [--strict]

Flags:
    --root     Repo root (default: parent of this script's directory)
    --today    Override today's date for deterministic tests (YYYY-MM-DD)
    --strict   Exit 1 if any TODO-CONFIRM entries exist (treat as failure)

Exit code 0 = 0 failures (warnings may still be printed).
Exit code 1 = ≥1 failure.  Every failure is one line starting with FAIL:.
"""

import argparse
import os
import re
import sys
from datetime import date

if sys.stdout.encoding and sys.stdout.encoding.lower() != "utf-8":
    try:
        sys.stdout.reconfigure(encoding="utf-8")
    except Exception:
        pass

try:
    import yaml
except ImportError:
    print("FAIL: PyYAML is not installed — run `pip install pyyaml` and retry.")
    sys.exit(1)

# ---------------------------------------------------------------------------
# Status sets  (GOV §4.4 for documents; GOV §9.2 for requirements)
# ---------------------------------------------------------------------------

# Document statuses — GOV §4.4 + TODO-CONFIRM sentinel
DOC_STATUSES: frozenset = frozenset({
    "PROPOSED",
    "DRAFT",
    "BASELINED-FOR-BUILD",
    "APPROVED",
    "EFFECTIVE",
    "SUPERSEDED",
    "WITHDRAWN",
    "TODO-CONFIRM",  # sentinel: "For approval" docs awaiting user mapping decision
})

# Requirement statuses — GOV §9.2
# NOTE: intentionally NOT a superset of DOC_STATUSES.
REQ_STATUSES: frozenset = frozenset({
    "PROPOSED",
    "BASELINED",
    "EFFECTIVE",
    "RETIRED",
})

# Document statuses sufficient for a BASELINED requirement (§9.4 coupling)
_DOC_STATUSES_FOR_BASELINED: frozenset = frozenset({
    "BASELINED-FOR-BUILD",
    "APPROVED",
    "EFFECTIVE",
})

# Plain path: no whitespace, no em-dash (U+2014), no en-dash (U+2013)
_PLAIN_PATH_RE = re.compile(r"^[^\s\u2014\u2013]+$")


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

def load_yaml(path):
    with open(path, encoding="utf-8") as fh:
        return yaml.safe_load(fh)


def parse_review_due(raw, doc_id, failures):
    """Parse a review_due field; append FAIL if format is bad. Returns date or None."""
    if raw is None:
        return None
    raw_str = str(raw).strip()
    try:
        return date.fromisoformat(raw_str)
    except ValueError:
        failures.append(
            f"FAIL [§9.4-date] register.yaml: {doc_id} has unparseable review_due "
            f"'{raw_str}' (expected YYYY-MM-DD)"
        )
        return None


def _doc_id_prefix(req_id):
    """Return ZTAX-<TOKENS> prefix (everything before -REQ-NNNN), or None."""
    m = re.fullmatch(r"(ZTAX-[A-Z0-9]+(?:-[A-Z0-9]+)*)-REQ-\d+", req_id)
    return m.group(1) if m else None


def id_prefix_match(req_id, registered_doc_ids):
    """
    Match a requirement id's prefix against the registered document IDs.

    Algorithm:
      1. Strip the -REQ-NNNN suffix to get the raw prefix (e.g. ZTAX-AI).
      2. For every registered doc ID, strip its final -NNN segment
         (e.g. ZTAX-AI-001 -> ZTAX-AI, ZTAX-AIGOV-001 -> ZTAX-AIGOV)
         and compare to the raw prefix.
      3. Collect all doc IDs whose stripped form equals the raw prefix.

    This means ZTAX-AI-REQ-0001 matches ZTAX-AI-001 but NOT ZTAX-AIGOV-001.

    Returns (ok: bool, matched_doc_id or None).
    """
    prefix = _doc_id_prefix(req_id)
    if prefix is None:
        return False, None

    matches = [
        doc_id for doc_id in registered_doc_ids
        if re.sub(r"-\d+$", "", doc_id) == prefix
    ]

    if len(matches) == 1:
        return True, matches[0]
    if len(matches) == 0:
        return False, None
    # Multiple doc IDs share the same stripped prefix — accept, let caller
    # verify the declared doc_id is among registered ones.
    return True, None


def is_plain_path(ref):
    """Return True if ref is a plain file path (no spaces, no em/en-dashes)."""
    return bool(_PLAIN_PATH_RE.match(ref.strip()))


def path_exists(ref, root):
    """Return True if ref resolved relative to root exists on disk."""
    return os.path.exists(os.path.join(root, ref.strip()))


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

def main():
    parser = argparse.ArgumentParser(
        description="Validate ZoikoTax registry files (GOV §9.4)."
    )
    parser.add_argument(
        "--root",
        default=os.path.dirname(os.path.dirname(os.path.abspath(__file__))),
        help="Repo root (default: parent of this script's directory)",
    )
    parser.add_argument(
        "--today",
        default=None,
        metavar="YYYY-MM-DD",
        help="Override today's date for deterministic testing",
    )
    parser.add_argument(
        "--strict",
        action="store_true",
        help="Exit 1 if any TODO-CONFIRM entries exist",
    )
    args = parser.parse_args()
    root = args.root

    # Validate --today
    if args.today is not None:
        try:
            today = date.fromisoformat(args.today)
        except ValueError:
            print(f"FAIL: --today '{args.today}' is not a valid YYYY-MM-DD date.")
            return 1
    else:
        today = date.today()

    register_path = os.path.join(root, "docs", "register.yaml")
    reqs_path     = os.path.join(root, "docs", "requirements.yaml")

    failures = []
    warnings = []

    # ------------------------------------------------------------------
    # Load files
    # ------------------------------------------------------------------
    missing = [p for p in [register_path, reqs_path] if not os.path.isfile(p)]
    for p in missing:
        failures.append(f"FAIL: file not found: {p}")
    if missing:
        _report(failures, warnings, strict=args.strict)
        return 1

    register_data = load_yaml(register_path)
    reqs_data     = load_yaml(reqs_path)

    documents    = register_data.get("documents", []) or []
    requirements = reqs_data.get("requirements", []) or []

    # Build index: document_id -> document record
    doc_index = {}
    for doc in documents:
        doc_id = doc.get("document_id")
        if doc_id:
            doc_index[doc_id] = doc
    registered_doc_ids = set(doc_index)

    # ------------------------------------------------------------------
    # A. register.yaml checks
    # ------------------------------------------------------------------
    todo_confirm_docs = []

    for doc in documents:
        doc_id = doc.get("document_id", "<unknown>")
        status = doc.get("status", "")

        # A1. Status in the document status set
        if status not in DOC_STATUSES:
            failures.append(
                f"FAIL [§4.4-doc] register.yaml: {doc_id} has unknown document status "
                f"'{status}' (allowed for documents: {', '.join(sorted(DOC_STATUSES))})"
            )

        # A2. TODO-CONFIRM sentinel tracking
        if status == "TODO-CONFIRM":
            todo_confirm_docs.append(doc_id)

        # A3. review_due: parse and check
        raw_due  = doc.get("review_due")
        due_date = parse_review_due(raw_due, doc_id, failures)

        if status in ("APPROVED", "EFFECTIVE"):
            if raw_due is None:
                failures.append(
                    f"FAIL [§9.4-review] register.yaml: {doc_id} has status {status} "
                    f"but review_due is null (APPROVED/EFFECTIVE require a review date)"
                )
            elif due_date is not None and due_date < today:
                failures.append(
                    f"FAIL [§9.4-expired] register.yaml: {doc_id} review_due "
                    f"{due_date.isoformat()} is in the past (today={today.isoformat()})"
                )
        elif due_date is not None and due_date < today:
            # Any status with a past review_due is still a failure
            failures.append(
                f"FAIL [§9.4-expired] register.yaml: {doc_id} review_due "
                f"{due_date.isoformat()} is in the past (today={today.isoformat()})"
            )

    # ------------------------------------------------------------------
    # B. requirements.yaml checks
    # ------------------------------------------------------------------
    seen_req_ids = {}

    for idx, req in enumerate(requirements):
        req_id = req.get("id", f"<entry {idx}>")
        doc_id = req.get("document_id", "")
        status = req.get("status", "")
        vref   = req.get("verification_ref")
        owner  = req.get("owner")

        # B1. Globally unique id
        if req_id in seen_req_ids:
            failures.append(
                f"FAIL [§9.4-unique] {req_id}: duplicate id "
                f"(first at entry {seen_req_ids[req_id]}, again at entry {idx})"
            )
        seen_req_ids[req_id] = idx

        # B2. document_id must exist in register
        if doc_id not in registered_doc_ids:
            failures.append(
                f"FAIL [§9.4-docref] {req_id}: document_id '{doc_id}' "
                f"not found in register.yaml"
            )

        # B3. id prefix must match a registered document_id
        ok, matched_doc = id_prefix_match(req_id, registered_doc_ids)
        if not ok:
            prefix = _doc_id_prefix(req_id) or req_id
            failures.append(
                f"FAIL [§9.4-prefix] {req_id}: id prefix '{prefix}' does not match "
                f"any document_id in register.yaml (strip final -NNN, then compare)"
            )
        elif matched_doc is not None and matched_doc != doc_id:
            failures.append(
                f"FAIL [§9.4-prefix] {req_id}: id prefix implies document_id "
                f"'{matched_doc}' but entry declares '{doc_id}'"
            )

        # B4. Status in the requirement status set
        if status not in REQ_STATUSES:
            failures.append(
                f"FAIL [§9.2-status] {req_id}: unknown requirement status '{status}' "
                f"(allowed for requirements: {', '.join(sorted(REQ_STATUSES))})"
            )

        # B5. Non-empty owner
        if not owner:
            failures.append(f"FAIL [§9.4-owner] {req_id}: missing owner")

        # B6. Document-status coupling
        src_doc    = doc_index.get(doc_id, {})
        src_status = src_doc.get("status", "")

        if status == "BASELINED":
            if src_status not in _DOC_STATUSES_FOR_BASELINED:
                failures.append(
                    f"FAIL [§9.4-coupling] {req_id}: status BASELINED requires source "
                    f"document in {{BASELINED-FOR-BUILD, APPROVED, EFFECTIVE}} "
                    f"but '{doc_id}' is '{src_status}'"
                )

        if status == "EFFECTIVE":
            if src_status != "EFFECTIVE":
                failures.append(
                    f"FAIL [§9.4-coupling] {req_id}: status EFFECTIVE requires source "
                    f"document to be EFFECTIVE but '{doc_id}' is '{src_status}'"
                )

        # B7 & B8. verification_ref format and existence
        vref_ok = False
        if vref is not None:
            vref_str = str(vref).strip()
            if not is_plain_path(vref_str):
                failures.append(
                    f"FAIL [§9.4-refformat] {req_id}: verification_ref must be a plain "
                    f"repo-relative path (no spaces/dashes): '{vref_str[:80]}'"
                )
            else:
                if path_exists(vref_str, root):
                    vref_ok = True
                else:
                    failures.append(
                        f"FAIL [§9.4-refmissing] {req_id}: verification_ref path does "
                        f"not exist in repo: '{vref_str}'"
                    )

        # B9. EFFECTIVE requires a resolved ref
        if status == "EFFECTIVE":
            if vref is None:
                failures.append(
                    f"FAIL [§9.4-effective] {req_id}: status EFFECTIVE but "
                    f"verification_ref is null"
                )
            elif not vref_ok:
                failures.append(
                    f"FAIL [§9.4-effective] {req_id}: status EFFECTIVE but "
                    f"verification_ref does not resolve to an existing file"
                )

    # ------------------------------------------------------------------
    # C. TODO-CONFIRM summary (warning, or failure under --strict)
    # ------------------------------------------------------------------
    if todo_confirm_docs:
        count = len(todo_confirm_docs)
        ids   = ", ".join(todo_confirm_docs)
        msg   = (
            f"WARNING: {count} register entr{'y' if count == 1 else 'ies'} "
            f"{'has' if count == 1 else 'have'} status TODO-CONFIRM "
            f"(pending lifecycle mapping): {ids}"
        )
        if args.strict:
            failures.append(f"FAIL [--strict] {msg}")
        else:
            warnings.append(msg)

    _report(failures, warnings, strict=args.strict)
    return 1 if failures else 0


def _report(failures, warnings, *, strict):
    sep = "=" * 70
    for w in warnings:
        print(w)
    if failures:
        print(f"\n{sep}")
        print(f"Registry validation: {len(failures)} failure(s) found")
        print(sep)
        for f in failures:
            print(f)
        print(f"{sep}\n")
    else:
        warn_note = f" ({len(warnings)} warning(s))" if warnings else ""
        print(f"Registry validation: OK — 0 failures{warn_note}.")


if __name__ == "__main__":
    sys.exit(main())
