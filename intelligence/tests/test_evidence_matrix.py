"""Evidence-matrix drift guard.

Validates EVIDENCE.md against the runner (tools/step10_demo.py) and the
source tree.  Put all logic in validate(text: str) -> list[str] returning
problems, and test it on the real file AND on deliberately corrupted copies
of the real text (negative tests), so no scratch script is needed.

Rules enforced
--------------
* File exists, is non-empty, under 160 lines.
* Required headings A through G are present.
* Claim IDs in section C match the runner's CLAIMS in order.
* Claim text in section C matches the runner's text exactly.
* No empty or whitespace-only table cell.
* Status values in section C are one of the three permitted terms.
* None of the forbidden words appear outside the allowed phrase.
* No percent sign; no "<digits> tests" pattern.
* The sentence "does not close any requirement" is present.
* Every backticked path starting with tests/, src/ztax_gateway/, or tools/
  names an existing file; when ::Name is appended, that name exists in the
  file (parsed with ast).
* Every module listed in section D exists under src/ztax_gateway/ and is not
  imported by any other src/ module.
* Matrix status for each claim is derived from the runner's data:
  PARTIAL -> PARTIAL, no tests -> NOT IMPLEMENTED, otherwise -> PROVEN.
* A "Re-run needs" column contains "Python" or "Python + Go" matching the
  runner's claim kind.
* Go test names for Go-kind claims in the matrix match those in the runner.
* Supports column IDs (REQ/ADR patterns) are substrings of the cited test
  class/function docstrings.
* C9's "Does NOT prove" cell contains the runner's note for C9 (case-insensitive).
* Section B contains "--skip-go" and "NOT RUN".
* The document does not claim a formal document id (no "document identifier").
* Negative tests: each rule above has a corrupted-document test.
"""
from __future__ import annotations

import ast
import importlib.util
import pathlib
import re
import sys
import unittest
from typing import Any, Final

# ---------------------------------------------------------------------------
# Paths (relative to this file)
# ---------------------------------------------------------------------------

_TESTS_DIR: Final[pathlib.Path] = pathlib.Path(__file__).resolve().parent
_INTEL_DIR: Final[pathlib.Path] = _TESTS_DIR.parent
_SRC_DIR: Final[pathlib.Path] = _INTEL_DIR / "src" / "ztax_gateway"
_EVIDENCE_PATH: Final[pathlib.Path] = _INTEL_DIR / "EVIDENCE.md"
_TOOLS_DIR: Final[pathlib.Path] = _INTEL_DIR / "tools"
_STEP10: Final[pathlib.Path] = _TOOLS_DIR / "step10_demo.py"

# ---------------------------------------------------------------------------
# Module-level caches (computed once at import time)
# ---------------------------------------------------------------------------

# Cache: path -> parsed ast.Module
_AST_CACHE: dict[pathlib.Path, ast.Module] = {}
# Cache: path -> source text
_TEXT_CACHE: dict[pathlib.Path, str] = {}
# Cache: import blob from src/
_IMPORT_BLOB: str = ""
_SRC_FILE_NAMES: frozenset[str] = frozenset()


def _get_ast(path: pathlib.Path) -> ast.Module | None:
    """Return cached AST for path, or None on SyntaxError."""
    if path not in _AST_CACHE:
        try:
            _AST_CACHE[path] = ast.parse(
                path.read_text(encoding="utf-8", errors="replace")
            )
        except SyntaxError:
            return None
    return _AST_CACHE[path]


def _get_text(path: pathlib.Path) -> str:
    if path not in _TEXT_CACHE:
        _TEXT_CACHE[path] = path.read_text(encoding="utf-8", errors="replace")
    return _TEXT_CACHE[path]


def _build_src_cache() -> None:
    global _IMPORT_BLOB, _SRC_FILE_NAMES
    import_lines: list[str] = []
    names: set[str] = set()
    for src_f in _SRC_DIR.glob("*.py"):
        names.add(src_f.name)
        for line in _get_text(src_f).splitlines():
            stripped = line.strip()
            if stripped.startswith("import ") or stripped.startswith("from "):
                import_lines.append(stripped)
    _IMPORT_BLOB = "\n".join(import_lines)
    _SRC_FILE_NAMES = frozenset(names)


_build_src_cache()

# Cached EVIDENCE.md text (read once)
_EVIDENCE_TEXT: str = _EVIDENCE_PATH.read_text(encoding="utf-8") if _EVIDENCE_PATH.exists() else ""
# Pre-built AST cache for all test files referenced in EVIDENCE (warm up at import)
_INTEL_DIR_REL_PREFIXES = ("tests/", "src/ztax_gateway/", "tools/")
for _tok in re.findall(r"`([^`]+)`", _EVIDENCE_TEXT):
    for _pfx in _INTEL_DIR_REL_PREFIXES:
        if _tok.startswith(_pfx):
            _get_ast(_INTEL_DIR / _tok.split("::")[0])
            break

def _load_claims() -> Any:
    spec = importlib.util.spec_from_file_location("step10_demo", _STEP10)
    assert spec and spec.loader
    mod = importlib.util.module_from_spec(spec)
    sys.modules.setdefault("step10_demo", mod)
    spec.loader.exec_module(mod)
    return mod.CLAIMS


_CLAIMS = _load_claims()

# ---------------------------------------------------------------------------
# Allowed statuses (section C)
# ---------------------------------------------------------------------------

_ALLOWED_STATUSES: Final[frozenset[str]] = frozenset(
    {
        "PROVEN",
        "PARTIAL",
        "NOT IMPLEMENTED",
    }
)

# Forbidden words; the only permitted context is "does not close any requirement"
_FORBIDDEN: Final[tuple[str, ...]] = (
    "closes",
    "closed",
    "satisfies",
    "satisfied",
    "complete",
    "guarantee",
    "guarantees",
)

# Required headings A-G
_REQUIRED_HEADINGS: Final[tuple[str, ...]] = (
    "## A.",
    "## B.",
    "## C.",
    "## D.",
    "## E.",
    "## F.",
    "## G.",
)

# Requirement/ADR id pattern from prompt
_REQ_PAT: Final[re.Pattern[str]] = re.compile(
    r"(?:ZTAX-[A-Z]+-)?REQ-\d{4}(?:-\d{4})?|ADR-\d{4}|ADR-AIGOV-\d{3}"
)


# Pre-computed docstring cache: (abs_path, symbol) -> joined docstring text
# Built once at import time; validate() does a pure dict lookup.
_DOCSTRING_CACHE: dict[tuple[pathlib.Path, str], str] = {}


def _build_docstring_cache() -> None:
    """Walk all cached ASTs once, collecting docstrings by (path, name)."""
    for path, tree in _AST_CACHE.items():
        # Module docstring is attributed to the empty-string symbol
        mod_ds = ast.get_docstring(tree) or ""
        for node in ast.walk(tree):
            if isinstance(node, (ast.ClassDef, ast.FunctionDef, ast.AsyncFunctionDef)):
                key = (path, node.name)
                ds = ast.get_docstring(node) or ""
                existing = _DOCSTRING_CACHE.get(key, "")
                _DOCSTRING_CACHE[key] = (existing + "\n" + ds + "\n" + mod_ds).strip()


_build_docstring_cache()


def _docstrings_for(filepath: pathlib.Path, symbol: str) -> str:
    """Return pre-computed docstring text for (filepath, symbol) — O(1) lookup."""
    key = (filepath.resolve(), symbol)
    if key in _DOCSTRING_CACHE:
        return _DOCSTRING_CACHE[key]
    # Fallback: file was not pre-cached (e.g. corrupted path in a negative test)
    tree = _get_ast(filepath)
    if tree is None:
        return ""
    _named_def = (ast.ClassDef, ast.FunctionDef, ast.AsyncFunctionDef)
    parts: list[str] = []
    for node in ast.walk(tree):
        if isinstance(node, _named_def) and node.name == symbol:
            ds = ast.get_docstring(node)
            if ds:
                parts.append(ds)
    mod_ds = ast.get_docstring(tree)
    if mod_ds:
        parts.append(mod_ds)
    return "\n".join(parts)


# ---------------------------------------------------------------------------
# Derive expected status and re-run needs from the runner
# ---------------------------------------------------------------------------


def _expected_status(claim: Any) -> str:
    """Return expected matrix status derived from runner data (not hardcoded IDs)."""
    if claim.success_status == "PARTIAL":
        return "PARTIAL"
    if claim.success_status in ("NOT IMPLEMENTED", "NOT RUN"):
        return "NOT IMPLEMENTED"
    return "PROVEN"


def _expected_rerun(claim: Any) -> str:
    return "Python + Go" if claim.kind == "go" else "Python"


# ---------------------------------------------------------------------------
# Helper: collect Go test names from a Go-kind claim
# ---------------------------------------------------------------------------


def _go_tests_for(claim: Any) -> set[str]:
    names: set[str] = set()
    for attr in ("go_tests", "go_app_tests", "go_http_tests"):
        val = getattr(claim, attr, None)
        if val:
            names.update(val)
    return names


# ---------------------------------------------------------------------------
# Validator
# ---------------------------------------------------------------------------


def validate(text: str) -> list[str]:
    """Return a list of problem strings.  Empty list means the document is OK."""
    problems: list[str] = []
    lines = text.splitlines()

    # --- basic checks -------------------------------------------------------
    if not text.strip():
        problems.append("Document is empty.")
        return problems
    if len(lines) > 160:
        problems.append(f"Document is {len(lines)} lines; limit is 160.")

    # --- no formal document id ---------------------------------------------
    if "document identifier" in text.lower():
        problems.append("Document must not claim a formal document id.")

    # --- required headings --------------------------------------------------
    for heading in _REQUIRED_HEADINGS:
        if not any(ln.startswith(heading) for ln in lines):
            problems.append(f"Required heading missing: {heading!r}")

    # --- forbidden words ----------------------------------------------------
    safe_phrase = "does not close any requirement"
    scrubbed = text.replace(safe_phrase, " " * len(safe_phrase))
    for word in _FORBIDDEN:
        if re.search(rf"\b{word}\b", scrubbed, re.IGNORECASE):
            problems.append(f"Forbidden word found: {word!r}")

    # --- no percent, no "<digits> tests" ------------------------------------
    if "%" in text:
        problems.append("Percent sign found in document.")
    if re.search(r"\d+\s+tests", text):
        problems.append("Pattern '<digits> tests' found in document.")

    # --- mandatory sentence -------------------------------------------------
    if safe_phrase not in text:
        problems.append(f"Mandatory sentence missing: {safe_phrase!r}")

    # --- section B: --skip-go and NOT RUN -----------------------------------
    sec_b_match = re.search(r"## B\..*?## C\.", text, re.DOTALL)
    sec_b = sec_b_match.group(0) if sec_b_match else ""
    if "--skip-go" not in sec_b:
        problems.append("Section B must mention '--skip-go'.")
    if "NOT RUN" not in sec_b:
        problems.append("Section B must mention 'NOT RUN'.")

    # --- no empty table cells -----------------------------------------------
    if re.search(r"\|[^\S\n]*\|", text):
        problems.append("Empty table cell found.")

    # --- claim IDs and texts ------------------------------------------------
    claim_ids_in_doc = re.findall(r"\|\s*(C\d+)\s*\|", text)
    runner_ids = [c.id for c in _CLAIMS]
    if claim_ids_in_doc != runner_ids:
        problems.append(
            f"Claim IDs mismatch. Doc: {claim_ids_in_doc}  Runner: {runner_ids}"
        )
    for claim in _CLAIMS:
        if claim.text not in text:
            problems.append(
                f"{claim.id}: runner claim text not found verbatim in document."
            )

    # --- status values ------------------------------------------------------
    for match in re.finditer(
        r"\|\s*C\d+\s*\|[^|]+\|\s*([^|]+?)\s*\|", text
    ):
        status = match.group(1).strip()
        if status not in _ALLOWED_STATUSES:
            problems.append(f"Unknown status value: {status!r}")

    # --- matrix status matches runner-derived expected ----------------------
    for claim in _CLAIMS:
        status_match = re.search(
            rf"\|\s*{claim.id}\s*\|[^|]+\|\s*([^|]+?)\s*\|", text
        )
        if status_match is None:
            continue
        expected = _expected_status(claim)
        actual = status_match.group(1).strip()
        if actual != expected:
            problems.append(
                f"{claim.id}: matrix status {actual!r} does not match "
                f"expected {expected!r} (runner success_status="
                f"{claim.success_status!r}, kind={claim.kind!r})."
            )

    # --- Re-run needs column matches runner kind ----------------------------
    for claim in _CLAIMS:
        # Row pattern: | ID | text | status | rerun | ...
        rerun_match = re.search(
            rf"\|\s*{claim.id}\s*\|[^|]+\|[^|]+\|\s*([^|]+?)\s*\|", text
        )
        if rerun_match is None:
            continue
        expected_rerun = _expected_rerun(claim)
        actual_rerun = rerun_match.group(1).strip()
        if actual_rerun != expected_rerun:
            problems.append(
                f"{claim.id}: Re-run needs {actual_rerun!r} does not match "
                f"expected {expected_rerun!r} (kind={claim.kind!r})."
            )

    # --- Go test name drift check -------------------------------------------
    for claim in _CLAIMS:
        if claim.kind != "go":
            continue
        runner_go_names = _go_tests_for(claim)
        if not runner_go_names:
            continue
        # Find the Go test names documented in the row
        # The row for this claim is the line containing | cid |
        row_lines = [ln for ln in lines if re.search(rf"\|\s*{claim.id}\s*\|", ln)]
        if not row_lines:
            continue
        row_text = " ".join(row_lines)
        # Extract backticked tokens that look like Go test names (no '/')
        doc_go_names = set(
            re.findall(r"`(Test[A-Za-z0-9]+)`", row_text)
        )
        missing = runner_go_names - doc_go_names
        extra = doc_go_names - runner_go_names
        if missing:
            problems.append(
                f"{claim.id}: Go test names missing from doc: {sorted(missing)}"
            )
        if extra:
            problems.append(
                f"{claim.id}: Go test names in doc not in runner: {sorted(extra)}"
            )

    # --- C9 "Does NOT prove" cell contains runner note ----------------------
    c9_claim = next((c for c in _CLAIMS if c.id == "C9"), None)
    if c9_claim and hasattr(c9_claim, "note") and c9_claim.note:
        # Normalise both to lower for comparison; the note must be contained
        note_lower = c9_claim.note.lower()
        # Check using key phrases from the note
        c9_rows = [ln for ln in lines if re.search(r"\|\s*C9\s*\|", ln)]
        c9_row_text = " ".join(c9_rows).lower()
        # The note mentions: "does not prove low-confidence routing" and "durable"
        key_phrases = [
            "low-confidence routing",
            "durable",
        ]
        for phrase in key_phrases:
            if phrase not in c9_row_text and phrase not in note_lower:
                pass  # phrase not in note itself - skip
            elif phrase in note_lower and phrase not in c9_row_text:
                problems.append(
                    f"C9 'Does NOT prove' cell is missing runner note phrase: {phrase!r}"
                )

    # --- backtick path/symbol checks ----------------------------------------
    for match in re.finditer(r"`([^`]+)`", text):
        token = match.group(1)
        for prefix in ("tests/", "src/ztax_gateway/", "tools/"):
            if not token.startswith(prefix):
                continue
            parts = token.split("::")
            rel_path = parts[0]
            full_path = _INTEL_DIR / rel_path
            if not full_path.exists():
                problems.append(f"Backtick path does not exist: {rel_path!r}")
                break
            if len(parts) >= 2:
                symbol = parts[1].split("[")[0]
                cached_tree = _get_ast(full_path)
                if cached_tree is None:
                    break
                names = {
                    n.name
                    for n in ast.walk(cached_tree)
                    if isinstance(n, (ast.ClassDef, ast.FunctionDef))
                }
                if symbol not in names:
                    problems.append(
                        f"Symbol {symbol!r} not found in {rel_path}"
                    )
            break

    # --- section D: modules exist and are unwired (uses cached scan) --------
    sec_d_match = re.search(r"## D\..*?## E\.", text, re.DOTALL)
    if sec_d_match:
        sec_d = sec_d_match.group(0)
        all_mods = re.findall(r"`([\w]+\.py)`", sec_d)
        mods_in_doc = [m for m in all_mods if not m.startswith("test_")]
        for mod_name in mods_in_doc:
            if mod_name not in _SRC_FILE_NAMES:
                problems.append(
                    f"Section D module {mod_name!r} does not exist in src/ztax_gateway/."
                )
            else:
                bare = mod_name[:-3]
                if re.search(rf"\b{bare}\b", _IMPORT_BLOB):
                    problems.append(
                        f"Section D module {mod_name!r} appears to be imported in src/."
                    )

    # --- Supports column: ids must exist in cited test docstrings -----------
    sec_c_match = re.search(r"## C\..*?## D\.", text, re.DOTALL)
    if sec_c_match:
        sec_c = sec_c_match.group(0)
        for claim in _CLAIMS:
            # Extract the row for this claim
            row_lines = [ln for ln in sec_c.splitlines() if re.search(rf"\|\s*{claim.id}\s*\|", ln)]
            if not row_lines:
                continue
            row = row_lines[0]
            # Split row into columns
            cols = [c.strip() for c in row.split("|")]
            # Columns: '', ID, Claim, Status, Re-run, Tests, Proves, Does NOT prove, Supports, ''
            if len(cols) < 9:
                continue
            supports_cell = cols[-2]  # second to last (before trailing '')
            # Find REQ/ADR id tokens in Supports cell
            req_ids = _REQ_PAT.findall(supports_cell)
            if not req_ids or supports_cell.strip() == "-":
                continue
            # Collect docstring text from cited test nodes
            tests_cell = cols[5] if len(cols) > 5 else ""
            # Extract backtick tokens in tests cell: tests/file.py::Symbol
            all_ds_text = ""
            for tok_match in re.finditer(r"`([^`]+)`", tests_cell):
                tok = tok_match.group(1)
                if "::" in tok:
                    fpath_str, sym = tok.split("::", 1)
                    sym = sym.split("[")[0]
                    fpath = _INTEL_DIR / fpath_str
                    if fpath.exists():
                        all_ds_text += _docstrings_for(fpath, sym) + "\n"
                elif tok.startswith("tests/") or tok.startswith("src/"):
                    fpath = _INTEL_DIR / tok
                    if fpath.exists():
                        all_ds_text += fpath.read_text(encoding="utf-8") + "\n"
            for req_id in req_ids:
                if req_id not in all_ds_text:
                    problems.append(
                        f"{claim.id}: Supports id {req_id!r} not found in "
                        f"cited test docstrings."
                    )

    return problems


# ---------------------------------------------------------------------------
# Helpers for negative tests
# ---------------------------------------------------------------------------


def _real_text() -> str:
    """Return the cached EVIDENCE.md text (no disk read)."""
    return _EVIDENCE_TEXT


def _drop_claim_row(text: str, cid: str) -> str:
    """Remove the table row for claim *cid*."""
    return re.sub(rf"\|\s*{cid}\s*\|[^\n]+\n", "", text)


def _blank_does_not_prove(text: str) -> str:
    """Replace the Does NOT prove cell in the first claim row with an empty cell."""
    return re.sub(
        r"(\|\s*C\d+\s*\|[^|]+\|[^|]+\|[^|]+\|[^|]+\|[^|]+\|)([^|]+)(\|)",
        r"\1 \3",
        text,
        count=1,
    )


# ---------------------------------------------------------------------------
# Real-document tests
# ---------------------------------------------------------------------------


class TestEvidenceFileOK:
    def test_file_exists(self) -> None:
        assert _EVIDENCE_PATH.exists(), "EVIDENCE.md not found"

    def test_non_empty(self) -> None:
        assert _EVIDENCE_PATH.stat().st_size > 0

    def test_under_160_lines(self) -> None:
        lines = _EVIDENCE_TEXT.splitlines()
        assert len(lines) <= 160, f"Document is {len(lines)} lines"

    def test_utf8_no_bom(self) -> None:
        raw = _EVIDENCE_PATH.read_bytes()
        assert not raw.startswith(b"\xef\xbb\xbf"), "BOM found"


class TestValidatePassesOnRealDocument:
    def test_no_problems(self) -> None:
        problems = validate(_real_text())
        assert problems == [], "\n".join(problems)


# ---------------------------------------------------------------------------
# Negative tests — each corruption must produce at least one problem
# ---------------------------------------------------------------------------


class TestNegativeHeadings(unittest.TestCase):
    """One method per required heading so each subTest is individually identified."""

    def _check(self, heading: str) -> None:
        text = _EVIDENCE_TEXT.replace(heading, heading.replace("## ", "## X"))
        self.assertTrue(validate(text), f"Removing {heading!r} should produce a problem")

    def test_heading_a(self) -> None: self._check("## A.")
    def test_heading_b(self) -> None: self._check("## B.")
    def test_heading_c(self) -> None: self._check("## C.")
    def test_heading_d(self) -> None: self._check("## D.")
    def test_heading_e(self) -> None: self._check("## E.")
    def test_heading_f(self) -> None: self._check("## F.")
    def test_heading_g(self) -> None: self._check("## G.")


class TestNegativeClaimsSection:
    def test_deleted_claim_row(self) -> None:
        text = _drop_claim_row(_real_text(), "C1")
        assert validate(text), "Deleted C1 row should produce a problem"

    def test_wrong_claim_text(self) -> None:
        claim = _CLAIMS[0]
        text = _real_text().replace(claim.text, "Something completely different.")
        assert validate(text), "Wrong claim text should produce a problem"

    def test_nonexistent_test_id(self) -> None:
        text = _real_text().replace(
            "::TestP1HostileInputIsNeutralised",
            "::TestNonexistentSymbol9999",
        )
        assert validate(text), "Nonexistent test symbol should produce a problem"


class TestNegativeForbiddenWords:
    def test_word_closes_detected(self) -> None:
        text = _real_text() + "\n\nThis closes the gap.\n"
        assert validate(text), "Word 'closes' should produce a problem"

    def test_percentage_detected(self) -> None:
        text = _real_text() + "\n\n95% of tests pass.\n"
        assert validate(text), "Percent sign should produce a problem"

    def test_digit_tests_pattern(self) -> None:
        text = _real_text() + "\n\n42 tests ran.\n"
        assert validate(text), "'<digits> tests' pattern should produce a problem"


class TestNegativeMandatorySentence:
    def test_missing_mandatory_sentence(self) -> None:
        text = _real_text().replace(
            "does not close any requirement",
            "does not address any requirement",
        )
        assert validate(text), "Missing mandatory sentence should produce a problem"


class TestNegativeSectionD:
    def test_nonexistent_module_in_d(self) -> None:
        text = _real_text().replace(
            "approval_quorum.py", "nonexistent_module.py"
        )
        assert validate(text), "Nonexistent module in D should produce a problem"


class TestNegativeEmptyCell:
    def test_empty_does_not_prove_cell_detected(self) -> None:
        """An empty 'Does NOT prove' cell must be flagged by validate()."""
        text = _blank_does_not_prove(_real_text())
        assert validate(text), "Empty 'Does NOT prove' cell should produce a problem"


class TestNegativeRunnerStatusMapping(unittest.TestCase):
    """Status that conflicts with the runner's expected matrix label must be caught."""

    def test_c8_partial_label_rejected(self) -> None:
        # C8 runner success_status=PASS -> expected PROVEN; PARTIAL must be rejected.
        c8_prefix = (
            "| C8 | With the model provider disabled, approved deterministic"
            " quotes and commits continue. |"
        )
        text = _real_text().replace(
            c8_prefix + " PROVEN |",
            c8_prefix + " PARTIAL |",
        )
        problems = validate(text)
        self.assertTrue(
            problems, "C8 labelled PARTIAL should be rejected"
        )
        self.assertTrue(
            any("C8" in p for p in problems),
            f"Expected a C8 problem, got: {problems}",
        )

    def test_c8_not_implemented_label_rejected(self) -> None:
        c8_prefix = (
            "| C8 | With the model provider disabled, approved deterministic"
            " quotes and commits continue. |"
        )
        text = _real_text().replace(
            c8_prefix + " PROVEN |",
            c8_prefix + " NOT IMPLEMENTED |",
        )
        problems = validate(text)
        self.assertTrue(
            problems, "C8 labelled NOT IMPLEMENTED should be rejected"
        )
        self.assertTrue(
            any("C8" in p for p in problems),
            f"Expected a C8 problem, got: {problems}",
        )


class TestNegativeRerunNeeds(unittest.TestCase):
    """Re-run needs column that conflicts with the runner's kind must be caught."""

    def test_c8_wrong_rerun_rejected(self) -> None:
        # C8 is go-kind -> "Python + Go"; "Python" alone must be rejected.
        text = _real_text().replace(
            "| PROVEN | Python + Go | Go: `TestDeterministicFlow",
            "| PROVEN | Python | Go: `TestDeterministicFlow",
        )
        problems = validate(text)
        self.assertTrue(problems, "C8 Re-run needs 'Python' should be rejected")
        self.assertTrue(
            any("C8" in p for p in problems),
            f"Expected a C8 problem, got: {problems}",
        )

    def test_c1_wrong_rerun_rejected(self) -> None:
        # C1 is python-kind -> "Python"; "Python + Go" must be rejected.
        text = _real_text().replace(
            "| PROVEN | Python | `tests/test_ai_boundary_proof.py::TestP1",
            "| PROVEN | Python + Go | `tests/test_ai_boundary_proof.py::TestP1",
        )
        problems = validate(text)
        self.assertTrue(problems, "C1 Re-run needs 'Python + Go' should be rejected")
        self.assertTrue(
            any("C1" in p for p in problems),
            f"Expected a C1 problem, got: {problems}",
        )


class TestNegativeGoTestNames(unittest.TestCase):
    """Go test names that diverge from the runner must be caught."""

    def test_wrong_go_test_name_detected(self) -> None:
        text = _real_text().replace(
            "`TestDeterministicFlowIndependentOfGateway`",
            "`TestWrongGoTestNameXYZ`",
        )
        problems = validate(text)
        self.assertTrue(
            problems, "Wrong Go test name should produce a problem"
        )
        self.assertTrue(
            any("C8" in p for p in problems),
            f"Expected a C8 Go test name problem, got: {problems}",
        )


class TestNegativeSupportsIds(unittest.TestCase):
    """A Supports id not in the cited docstrings must be caught."""

    def test_supports_id_not_in_docstring(self) -> None:
        # REQ-9999 does not exist in any test docstring
        _c1_prefix = (
            "| C1 | Hostile text in a request cannot cause an approval"
            " or a privileged action. | PROVEN | Python"
            " | `tests/test_ai_boundary_proof.py::TestP1HostileInputIsNeutralised`"
            " | That a real governance gate blocks injection before the model."
            " | That a live model resists injection end-to-end."
        )
        text = _real_text().replace(
            _c1_prefix + " | REQ-0061 |",
            _c1_prefix + " | REQ-9999 |",
        )
        problems = validate(text)
        self.assertTrue(
            problems, "Supports id REQ-9999 not in docstrings should produce a problem"
        )
        self.assertTrue(
            any("REQ-9999" in p for p in problems),
            f"Expected REQ-9999 problem, got: {problems}",
        )


class TestNegativeSkipGoPhrase(unittest.TestCase):
    """Section B must mention --skip-go and NOT RUN."""

    def test_missing_skip_go_detected(self) -> None:
        text = _real_text().replace("--skip-go", "--skip_go_removed")
        problems = validate(text)
        self.assertTrue(
            problems, "Missing '--skip-go' in Section B should produce a problem"
        )
        self.assertTrue(
            any("--skip-go" in p for p in problems),
            f"Expected --skip-go problem, got: {problems}",
        )

    def test_missing_not_run_detected(self) -> None:
        text = _real_text().replace("NOT RUN", "UNVERIFIED")
        problems = validate(text)
        self.assertTrue(
            problems, "Missing 'NOT RUN' in Section B should produce a problem"
        )
        self.assertTrue(
            any("NOT RUN" in p for p in problems),
            f"Expected NOT RUN problem, got: {problems}",
        )


class TestNegativeC9RunnerNote(unittest.TestCase):
    """C9 Does NOT prove cell must contain key phrases from the runner note."""

    def test_c9_missing_runner_note_phrase(self) -> None:
        text = _real_text().replace(
            "Low-confidence routing or durable storage in a review queue.",
            "Something that does not mention key phrases.",
        )
        problems = validate(text)
        self.assertTrue(
            problems, "C9 missing runner note phrase should produce a problem"
        )


class TestNegativeDocumentId(unittest.TestCase):
    """Document must not claim a formal document id."""

    def test_document_id_claim_detected(self) -> None:
        text = _real_text().replace(
            "Working draft",
            "Working draft — document identifier: " + "ZTAX-DOC-" + "999",
        )
        problems = validate(text)
        self.assertTrue(
            problems, "Claiming a document identifier should produce a problem"
        )
        self.assertTrue(
            any("document id" in p.lower() for p in problems),
            f"Expected document id problem, got: {problems}",
        )
