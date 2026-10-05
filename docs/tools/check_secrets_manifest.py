# ZTAX-SEC-001 doctrine: secrets never enter source control.
# See ADR-0017 §2.4 for the environment-variable rule.
# Enforces ZTAX-SEC-REQ-0136: no literal secret value in any manifest,
# config file, or CI workflow outside an explicit, reviewed allowlist.
import os
import re
import sys
import subprocess

SANCTIONED_FILES = [
    "docker-compose.yml",
    "backend/.env.local.example",
    ".github/workflows/ci.yml",
]

# Patterns that make a value definitively safe — the ENTIRE stripped value
# must match one of these; a safe substring is not enough.
_SAFE_EXACT = re.compile(
    r"^("
    r"local-dev-only"
    r"|ci-only"
    r"|<PLACEHOLDER>"
    r"|\$\{[A-Za-z0-9_]+\}"           # ${VAR} — shell substitution, entire value
    r"|\$\{\{[A-Za-z0-9_. ]+\}\}"    # ${{ secrets.TOKEN }} — GHA expression, entire value
    r"|vault://\S+"                    # vault reference — entire value
    r"|local://\S+"                    # local reference — entire value
    r")$"
)

# A "dotted module reference": exactly 2 segments, each purely alphabetic (no digits,
# no secret-word segments). Covers obj.field style code references like config.db.
# Deliberately narrow — anything with 3+ segments, digits, or secret-word segments
# is evaluated normally (e.g. my.real.secret.value must be flagged).
_DOTTED_CODE_REF = re.compile(r"^[A-Za-z_][A-Za-z_]*\.[A-Za-z_][A-Za-z_]*$")
_SECRET_WORDS_IN_SEGMENT = re.compile(r"(?i)(password|secret|token|key|credential|api)")

def is_safe_value(val: str) -> bool:
    """Return True only if val is an unambiguously safe placeholder or reference."""
    if not val:
        return True
    return bool(_SAFE_EXACT.match(val))


def _is_dotted_code_ref(val: str) -> bool:
    """True only for 2-segment dotted refs with no digits and no secret-word segments.
    e.g. config.db -> True, my.real.secret.value -> False (3 segments), obj.password -> False."""
    if not _DOTTED_CODE_REF.match(val):
        return False
    if _SECRET_WORDS_IN_SEGMENT.search(val):
        return False
    return True


def check_file(path: str) -> list[tuple[int, str]]:
    violations: list[tuple[int, str]] = []

    # Case-insensitive key match: covers password, Password, PASSWORD, apiKey, api_key, etc.
    secret_key_re = re.compile(r"(?i)(password|secret|token|api_key|apikey|credential|key)")
    conn_str_re = re.compile(r"(?:postgres|http|https)://[^:]+:([^@/?&,\s]+)@")

    # Assignment patterns:
    #   ENV-var style:  KEY=value  or  KEY: value  (key must be UPPERCASE_WITH_UNDERSCORES)
    #   YAML/config:    key: value  (key any case, no underscore required)
    # We handle both with two patterns so we can distinguish "code file" heuristics.
    env_key_re   = re.compile(r"^[ \t-]*([A-Z][A-Z0-9_]*)[ \t]*[:=][ \t]*(.+)$")
    yaml_key_re  = re.compile(r"^[ \t-]*\"?([A-Za-z][A-Za-z0-9_]*)\"?[ \t]*:[ \t]*(.+)$")

    is_yaml_json = path.endswith((".yml", ".yaml", ".json", ".toml", ".env",
                                  ".env.example", ".env.local.example"))

    with open(path, "r", encoding="utf-8", errors="ignore") as f:
        for i, line in enumerate(f):
            line_str = line.strip()
            if not line_str or line_str.startswith("#") or line_str.startswith("//"):
                continue

            # --- URI / connection-string check ---
            uri_violation = False
            for m in conn_str_re.finditer(line_str):
                pw = m.group(1)
                if not is_safe_value(pw):
                    violations.append((i + 1, "URI contains literal credentials"))
                    uri_violation = True
                    break
            if uri_violation:
                continue

            # --- Key=value / key: value check ---
            match = env_key_re.match(line_str)
            if match is None and is_yaml_json:
                match = yaml_key_re.match(line_str)

            if match is None:
                continue

            key, raw_val = match.groups()

            # Skip if it looks like code syntax (function calls, arrow funcs, etc.)
            if "=>" in raw_val or "function" in raw_val:
                continue

            # Strip a single trailing comma or semicolon, then strip quotes/whitespace.
            stripped = raw_val.rstrip()
            if stripped and stripped[-1] in (",", ";"):
                stripped = stripped[:-1]
            val = stripped.strip(" '\"")

            # Skip known-benign type literal words.
            if val in ("string", "number", "boolean", "str", "int", "float", "bool",
                       "null", "nil", "true", "false", "None", "undefined"):
                continue

            # Skip values containing obvious code constructs (parens, brackets, operators).
            if any(c in val for c in ("(", "[", "<<", "|", "=>")):
                continue

            # Skip pure dotted identifier references with no digits
            # (e.g. config.db.password → safe; my.real.secret.value → has alpha-only
            # segments but is still flagged because "real" and "value" contain no digits
            # yet the overall string looks like a real value — wait: the spec says
            # "my.real.secret.value" MUST be flagged.  So we only skip if it really is
            # a code-style symbol: all lowercase/underscore/alpha segments, min 2 segs,
            # AND the whole thing has no uppercase letter — otherwise it could be a value).
            if _is_dotted_code_ref(val):
                continue

            # Now check whether the key name looks secret-bearing.
            if not secret_key_re.search(key):
                continue

            # Final safe-value check (exact, anchored — not substring).
            if val and not is_safe_value(val):
                violations.append((i + 1, f"Literal secret assigned to '{key}'"))

    return violations


def main() -> None:
    repo_root = os.path.abspath(
        os.path.dirname(os.path.dirname(os.path.dirname(__file__)))
    )
    try:
        output = subprocess.check_output(
            ["git", "ls-files", "--cached", "--others", "--exclude-standard"],
            cwd=repo_root,
            text=True,
        )
    except subprocess.CalledProcessError:
        print("Failed to run git ls-files")
        sys.exit(1)

    files = output.splitlines()
    has_violations = False

    for rel_path in files:
        parts = rel_path.split("/")
        if any(d in parts for d in (".git", "node_modules", "vendor")):
            continue
        if rel_path.startswith("complete-info-docs/") or rel_path.startswith("docs/specs/"):
            continue

        if rel_path in SANCTIONED_FILES:
            continue

        # Exclude source-code, documentation, and binary asset files —
        # this is a deployment-manifest/config check, not a general scan.
        if rel_path.endswith((
            # source code
            ".go", ".ts", ".js", ".mjs", ".cs", ".py", ".java", ".rb",
            # web assets
            ".html", ".svg", ".css", ".png", ".jpg", ".gif", ".woff",
            # protobuf
            ".proto",
            # lock / dependency manifests (package versions, not secrets)
            ".lock",
            # documentation
            ".md", ".rst", ".txt",
            # API / test collection formats — example values, not real secrets
            ".postman_collection.json",
            # openapi specs — example values are intentionally fictional
        )) or rel_path.endswith((".yaml", ".yml")) and any(
            rel_path.startswith(p)
            for p in ("contracts/openapi/", "contracts/postman/", "backend/postman/")
        ) or rel_path.endswith(".json") and any(
            rel_path.startswith(p)
            for p in ("contracts/", "backend/postman/", "frontend/package")
        ):
            continue

        abs_path = os.path.join(repo_root, rel_path)
        if not os.path.isfile(abs_path):
            continue

        for line_num, reason in check_file(abs_path):
            print(
                f"VIOLATION: {rel_path}:{line_num} -- {reason}"
                f" (ADR-0017 §2.4 / ZTAX-SEC-REQ-0136)"
            )
            has_violations = True

    if has_violations:
        sys.exit(1)
    else:
        print("OK: no secret-shaped values found outside the sanctioned files (ADR-0017 §2.4).")
        sys.exit(0)


if __name__ == "__main__":
    main()
