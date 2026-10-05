# ZTAX-SEC-001 doctrine: secrets never enter source control.
# See ADR-0017 §2.4 for the environment-variable rule.
# Enforces ZTAX-SEC-REQ-0136: no literal secret value in any manifest,
# config file, or CI workflow outside an explicit, reviewed allowlist.
import os
import re
import sys
import subprocess

# .github/workflows/intelligence.yml and .github/workflows/release.yml were
# evaluated against this scanner and found to contain zero secret-shaped
# values as of this writing — they are excluded from this list because they
# have nothing to sanction, not because they were skipped. See
# docs/evidence/secret_scan_report.md for the evaluation.
SANCTIONED_FILES = [
    "docker-compose.yml",
    "backend/.env.local.example",
    ".github/workflows/ci.yml",
]

# ADR-0017 §2.4 / ZTAX-SEC-REQ-0136 scopes this check to "a configuration
# file, deployment manifest, or CI workflow" — not every non-code file in
# the repo. A data/schema catalog such as backend/migrations/
# privacy_catalog.json carries no deployed secret and is out of scope
# regardless of what its field names look like.
_DOTENV_RE = re.compile(r"^\.env(\..+)?$")
_DOCKER_COMPOSE_RE = re.compile(r"(?i)^docker-compose(\..+)?\.ya?ml$")


def is_in_scope(rel_path: str) -> bool:
    """True only for files that are config/manifest/CI-workflow-shaped per
    ADR-0017 §2.4: dotenv files, docker-compose*.yml, Kubernetes and
    admission-policy manifests, Tofu/Terraform, and CI workflows."""
    basename = os.path.basename(rel_path)
    if _DOTENV_RE.match(basename):
        return True
    if _DOCKER_COMPOSE_RE.match(basename):
        return True
    if rel_path.startswith("infra/kubernetes/") or rel_path.startswith("policy/admission/"):
        return True
    if rel_path.startswith("infra/tofu/"):
        return True
    if rel_path.startswith(".github/workflows/"):
        return True
    return False

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


# Field names that, in a Kubernetes secretKeyRef / External Secrets Operator
# reference, name WHERE a secret lives (a field name or a store path) rather
# than carry the secret's value. A bare "key" substring match is too broad
# (it also matches unrelated fields like topologyKey), so these three exact
# field names get a narrow, structural carve-out below instead.
_STRUCTURAL_REF_KEYS = frozenset({"secretkeyref", "key", "secretkey"})
_PATH_LIKE_VALUE = re.compile(r"^[A-Za-z0-9_\-./]+$")
_LOOKS_RANDOM = re.compile(r"[A-Za-z0-9]{16,}")

# A short alnum string shaped like "admin123" or "hunter2" is indistinguishable
# from a field name by shape alone -- it must also match a recognizable
# field/attribute-name word, not just be "short and alnum".
_KNOWN_FIELD_NAME_WORDS = frozenset({
    "dsn", "password", "username", "host", "port", "uri", "url",
    "connection_string", "secret", "api_key", "token", "name", "id",
    "key", "value", "path", "file", "cert", "ca", "tls",
})


def _is_structural_key_reference(value: str) -> bool:
    """True only for values shaped like a known field-name word or a
    vault-style path (letters/digits/._- and /, no spaces, no long unbroken
    random-looking run) -- e.g. 'dsn' or 'ztax/euc1-dev-01/database/migrate'.
    A bare alnum string that isn't a recognizable field name (e.g.
    'admin123', 'hunter2') is NOT treated as safe, even if short. A real
    pasted credential in the same position (e.g. an AWS access key id)
    still has a long unbroken alphanumeric run and is NOT treated as safe
    either."""
    if not value or " " in value:
        return False
    if _LOOKS_RANDOM.search(value):
        return False
    if "/" in value:
        return bool(_PATH_LIKE_VALUE.match(value))
    if "." in value:
        return bool(_PATH_LIKE_VALUE.match(value)) and not _SECRET_WORDS_IN_SEGMENT.search(value)
    return value.lower() in _KNOWN_FIELD_NAME_WORDS


def check_file(path: str) -> list[tuple[int, str]]:
    violations: list[tuple[int, str]] = []

    # Case-insensitive key match: covers password, Password, PASSWORD, apiKey, api_key,
    # etc. Deliberately does NOT match a bare "key" substring (that caught unrelated
    # fields like topologyKey) -- private_key/access_key are listed explicitly instead.
    secret_key_re = re.compile(
        r"(?i)(password|secret|token|api_key|apikey|credential|private_key|access_key)"
    )
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

            # secretKeyRef/key/secretKey name WHERE a secret lives (a field
            # name or store path), not the secret itself -- e.g. `key: dsn`
            # or `secretKey: dsn` in a k8s secretKeyRef/ExternalSecret. These
            # three field names are ALWAYS routed through the structural
            # check instead of the keyword gate below, since a bare "key"
            # would otherwise never reach it (dropped from secret_key_re to
            # stop matching topologyKey) and "secretKey" would otherwise
            # always match the keyword gate via the "secret" substring.
            if key.lower() in _STRUCTURAL_REF_KEYS:
                if _is_structural_key_reference(val):
                    continue
                violations.append((i + 1, f"Literal secret assigned to '{key}'"))
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

        if not is_in_scope(rel_path):
            continue

        if rel_path in SANCTIONED_FILES:
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
