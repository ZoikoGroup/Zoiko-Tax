"""No Direct Provider Call -- structural proof for the ADR-0006 S2.1 boundary.

Specification basis
--------------------
ADR-0006 S2.1 (cited verbatim in intelligence/pyproject.toml's own header
comment): the Governed Model Gateway is "the only path between the fiscal
core and any model provider." Nothing in the Gateway's own source may reach
a provider SDK or a provider's network endpoint directly -- every call must
go through the (currently unconfigured, in development) model runtime
boundary in runtime.py, which answers AI_GATEWAY_NOT_CONFIGURED locally
(see docker-compose.yml's ztax-model-gateway comment). A provider import or
a provider hostname literal anywhere under intelligence/src would mean that
boundary has a hole.

No standalone ADR-0006 document file exists in this repository (searched
the whole tree for any ADR-0006 file; only in-code citations like the one
above were found) -- this file cites the boundary as intelligence/
pyproject.toml and docker-compose.yml already describe it in their own
comments, not a document this repo does not have.

Proof obligations
------------------
P1  No .py file under intelligence/src imports (or from-imports) a provider
    SDK module, at any depth under that module.
P2  No .py file under intelligence/src contains a string constant naming a
    provider API hostname.
P3  intelligence/pyproject.toml's runtime and optional dependencies name
    none of the blocked SDK distribution names.
P4  The detector itself is proven correct on synthetic input before it is
    trusted on the real tree: it must flag a plain import, a from-import,
    and a hostname string literal, and it must NOT flag the word "openai"
    appearing only in a comment (ast.parse never sees comments at all) or a
    variable merely named anthropic_like (no import or string literal is a
    substring match target -- only exact/prefix-matched import paths and
    string *constants* are inspected).

Design rules (enforced here, not assumed)
------------------------------------------
* The detector parses source with the ast module and inspects
  ast.Import / ast.ImportFrom / ast.Constant nodes only. No regex over raw
  source text anywhere in this file, by design: a regex scan of file text
  cannot tell an import from a comment or a docstring, which is exactly the
  distinction P4 exists to prove the detector gets right.
* Standard library and pytest only. No network, no files outside tmp_path
  (the self-tests use in-memory source strings, not files).
* ALLOWLIST is empty. Adding an entry is a review event: it means a provider
  SDK import or hostname has been reviewed and is believed correct, which
  needs sign-off on the model runtime design (ADR-0006 S2.1), not a
  reflexive add-to-list to make a red test green.
"""

from __future__ import annotations

import ast
import tomllib
from dataclasses import dataclass
from pathlib import Path
from typing import Final

# ---------------------------------------------------------------------------
# Allowlist
# ---------------------------------------------------------------------------

# Reviewed exceptions to the checks below. Empty on purpose: every entry here
# is a real provider import or hostname that someone has reviewed against the
# model runtime design (ADR-0006 S2.1) and decided is correct -- it is not a
# place to silence a finding. Each entry must be {"<file>": "<reason, with
# who/what approved it>"}.
ALLOWLIST: dict[str, str] = {}

# ---------------------------------------------------------------------------
# Blocked provider SDK import paths (Check 1)
# ---------------------------------------------------------------------------

# Matched against the dotted import path: a hit is an exact match, or the
# import path starting with "<entry>." (so "openai" also blocks
# "openai.types", and "google.generativeai" also blocks
# "google.generativeai.types", without "google" alone being blocked).
_BLOCKED_IMPORT_PREFIXES: Final[tuple[str, ...]] = (
    "openai",
    "anthropic",
    "groq",
    "cohere",
    "mistralai",
    "google.generativeai",
    "google.genai",
    "vertexai",
    "boto3",
    "botocore",
    "litellm",
    "together",
    "ollama",
    "huggingface_hub",
    "transformers",
)

# ---------------------------------------------------------------------------
# Blocked provider SDK distribution names (Check 3 -- pyproject.toml)
# ---------------------------------------------------------------------------

# PyPI distribution names, normalised to lower-case with "-" -> "_" before
# comparison. Two entries (google_generativeai, google_genai) differ from
# their import path above because pyproject.toml names the *distribution*,
# not the import path: "pip install google-generativeai" provides the
# "google.generativeai" import.
_BLOCKED_PACKAGE_NAMES: Final[frozenset[str]] = frozenset(
    {
        "openai",
        "anthropic",
        "groq",
        "cohere",
        "mistralai",
        "google_generativeai",
        "google_genai",
        "vertexai",
        "boto3",
        "botocore",
        "litellm",
        "together",
        "ollama",
        "huggingface_hub",
        "transformers",
    }
)

# ---------------------------------------------------------------------------
# Blocked provider API hostnames (Check 2)
# ---------------------------------------------------------------------------

_BLOCKED_HOSTNAMES: Final[tuple[str, ...]] = (
    "api.openai.com",
    "api.anthropic.com",
    "api.groq.com",
    "api.cohere.com",
    "api.mistral.ai",
    "generativelanguage.googleapis.com",
    "openai.azure.com",
    "bedrock-runtime",
    "localhost:11434",
    "127.0.0.1:11434",
)


# ---------------------------------------------------------------------------
# Findings
# ---------------------------------------------------------------------------


@dataclass(frozen=True, slots=True)
class Finding:
    """One detected problem: which file, which line, what was found."""

    file: str
    line: int
    kind: str  # "import" | "hostname"
    detail: str

    def __str__(self) -> str:
        return f"{self.file}:{self.line}: {self.detail}"


# ---------------------------------------------------------------------------
# The detector
# ---------------------------------------------------------------------------


def _import_is_blocked(dotted_name: str) -> str | None:
    """Return the matching blocked prefix, or None."""
    for prefix in _BLOCKED_IMPORT_PREFIXES:
        if dotted_name == prefix or dotted_name.startswith(prefix + "."):
            return prefix
    return None


def scan_source(source: str, *, filename: str) -> list[Finding]:
    """Parse *source* as Python and return every Check-1/Check-2 finding.

    Inspects only ast.Import, ast.ImportFrom and ast.Constant (string) nodes.
    Comments do not exist in the parsed tree at all; identifiers that merely
    look like a blocked name (a variable, a dict key, a docstring word) are
    never inspected unless they are one of those three node kinds.
    """
    tree = ast.parse(source, filename=filename)
    findings: list[Finding] = []

    for node in ast.walk(tree):
        if isinstance(node, ast.Import):
            for alias in node.names:
                blocked = _import_is_blocked(alias.name)
                if blocked is not None:
                    findings.append(
                        Finding(
                            file=filename,
                            line=node.lineno,
                            kind="import",
                            detail=(
                                f"import of blocked provider SDK module "
                                f"{alias.name!r} (matches {blocked!r})"
                            ),
                        )
                    )
        elif isinstance(node, ast.ImportFrom):
            if node.module is None:
                # A relative import, e.g. "from . import x" -- cannot name an
                # external provider SDK.
                continue
            blocked = _import_is_blocked(node.module)
            if blocked is not None:
                findings.append(
                    Finding(
                        file=filename,
                        line=node.lineno,
                        kind="import",
                        detail=(
                            f"from-import of blocked provider SDK module "
                            f"{node.module!r} (matches {blocked!r})"
                        ),
                    )
                )
        elif isinstance(node, ast.Constant) and isinstance(node.value, str):
            for hostname in _BLOCKED_HOSTNAMES:
                if hostname in node.value:
                    findings.append(
                        Finding(
                            file=filename,
                            line=node.lineno,
                            kind="hostname",
                            detail=(
                                f"string constant contains blocked provider "
                                f"hostname {hostname!r} (full value: "
                                f"{node.value!r})"
                            ),
                        )
                    )

    return findings


def _iter_src_py_files(src_root: Path) -> list[Path]:
    """Every .py file under *src_root*, excluding __pycache__, sorted."""
    return sorted(
        p
        for p in src_root.rglob("*.py")
        if "__pycache__" not in p.parts
    )


def _src_root() -> Path:
    # tests/test_no_direct_provider.py -> tests -> intelligence -> src
    return Path(__file__).resolve().parents[1] / "src"


def scan_src_tree(src_root: Path) -> list[Finding]:
    """Scan every .py file under *src_root*; findings whose relative file
    path is in ALLOWLIST are dropped (ALLOWLIST is empty by default)."""
    findings: list[Finding] = []
    for path in _iter_src_py_files(src_root):
        rel = path.relative_to(src_root.parent).as_posix()
        # utf-8-sig strips a leading BOM if present (evaluation_quality.py has
        # one); it is a no-op for files that do not.
        source = path.read_text(encoding="utf-8-sig")
        for finding in scan_source(source, filename=rel):
            if rel in ALLOWLIST:
                continue
            findings.append(finding)
    return findings


# ---------------------------------------------------------------------------
# P1 / P2: the real intelligence/src tree
# ---------------------------------------------------------------------------


def test_no_src_file_imports_a_provider_sdk() -> None:
    """P1: no .py file under intelligence/src imports a provider SDK."""
    findings = [f for f in scan_src_tree(_src_root()) if f.kind == "import"]
    assert findings == [], "provider SDK import(s) found:\n" + "\n".join(
        str(f) for f in findings
    )


def test_no_src_file_contains_a_provider_hostname() -> None:
    """P2: no .py file under intelligence/src names a provider hostname."""
    findings = [f for f in scan_src_tree(_src_root()) if f.kind == "hostname"]
    assert findings == [], "provider hostname string(s) found:\n" + "\n".join(
        str(f) for f in findings
    )


# ---------------------------------------------------------------------------
# P3: pyproject.toml dependencies
# ---------------------------------------------------------------------------


def _normalise_package_name(requirement: str) -> str:
    """Return the bare distribution name from a PEP 508 requirement string,
    lower-cased with "-" normalised to "_" (e.g. "Google-GenAI>=1.0" ->
    "google_genai")."""
    name = requirement.strip()
    for sep in ("[", "=", "<", ">", "!", "~", " ", ";"):
        idx = name.find(sep)
        if idx != -1:
            name = name[:idx]
    return name.strip().lower().replace("-", "_")


def test_pyproject_dependencies_name_no_provider_sdk() -> None:
    """P3: neither the runtime nor the optional dependencies of
    intelligence/pyproject.toml name a blocked provider SDK distribution."""
    pyproject_path = Path(__file__).resolve().parents[1] / "pyproject.toml"
    with pyproject_path.open("rb") as fh:
        doc = tomllib.load(fh)

    project = doc.get("project", {})
    requirements: list[str] = list(project.get("dependencies", []))
    for group in project.get("optional-dependencies", {}).values():
        requirements.extend(group)

    hits = {
        req: _normalise_package_name(req)
        for req in requirements
        if _normalise_package_name(req) in _BLOCKED_PACKAGE_NAMES
    }
    assert hits == {}, f"pyproject.toml names blocked provider SDK(s): {hits}"


# ---------------------------------------------------------------------------
# P4: detector self-tests on synthetic source (not the real tree)
# ---------------------------------------------------------------------------


def test_detector_flags_a_plain_import() -> None:
    findings = scan_source("import openai\n", filename="synthetic.py")
    assert len(findings) == 1
    assert findings[0].kind == "import"
    assert findings[0].line == 1
    assert "openai" in findings[0].detail


def test_detector_flags_a_from_import() -> None:
    findings = scan_source(
        "from openai import OpenAI\n", filename="synthetic.py"
    )
    assert len(findings) == 1
    assert findings[0].kind == "import"
    assert "openai" in findings[0].detail


def test_detector_flags_a_deeper_import_under_a_blocked_prefix() -> None:
    findings = scan_source(
        "import google.generativeai.types\n", filename="synthetic.py"
    )
    assert len(findings) == 1
    assert "google.generativeai" in findings[0].detail


def test_detector_flags_a_hostname_string() -> None:
    source = 'URL = "https://api.openai.com/v1/chat/completions"\n'
    findings = scan_source(source, filename="synthetic.py")
    assert len(findings) == 1
    assert findings[0].kind == "hostname"
    assert findings[0].line == 1
    assert "api.openai.com" in findings[0].detail


def test_detector_does_not_flag_the_word_openai_in_a_comment() -> None:
    source = "# openai is a provider we might use one day\nx = 1\n"
    findings = scan_source(source, filename="synthetic.py")
    assert findings == []


def test_detector_does_not_flag_a_variable_named_like_a_provider() -> None:
    source = "anthropic_like = 5\nopenai_adjacent = anthropic_like + 1\n"
    findings = scan_source(source, filename="synthetic.py")
    assert findings == []


def test_detector_does_not_flag_an_unrelated_dotted_import() -> None:
    """"google.cloud.storage" must not be caught by the "google.generativeai"
    / "google.genai" entries -- only exact or prefixed-with-dot matches."""
    findings = scan_source(
        "import google.cloud.storage\n", filename="synthetic.py"
    )
    assert findings == []


def test_detector_does_not_flag_a_relative_import() -> None:
    findings = scan_source(
        "from . import governance\n", filename="synthetic.py"
    )
    assert findings == []


def test_detector_does_not_flag_harmless_code() -> None:
    source = (
        "import json\n"
        "from dataclasses import dataclass\n\n"
        "TOGETHER_WE_STAND = 'not a provider'\n\n"
        "def f(a, b):\n"
        "    '''Runs a and b together.'''\n"
        "    return a + b\n"
    )
    findings = scan_source(source, filename="synthetic.py")
    assert findings == []
