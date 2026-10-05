"""Tests for docs/tools/check_secrets_manifest.py.

Run standalone (no discover needed):
    cd docs/tools/tests && python -m unittest test_check_secrets_manifest
Or via discover:
    python -m unittest discover -s docs/tools/tests
"""

from __future__ import annotations

import os
import sys
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parent))   # docs/tools/ — same pattern as test_check_registry.py

import check_secrets_manifest  # noqa: E402


class TestCheckSecretsManifest(unittest.TestCase):

    def setUp(self) -> None:
        self.tmp_dir = tempfile.TemporaryDirectory()
        self.repo_root = self.tmp_dir.name

    def tearDown(self) -> None:
        self.tmp_dir.cleanup()

    def write_file(self, rel_path: str, content: str) -> str:
        abs_path = os.path.join(self.repo_root, rel_path)
        os.makedirs(os.path.dirname(abs_path), exist_ok=True)
        with open(abs_path, "w", encoding="utf-8") as f:
            f.write(content)
        return abs_path

    def _run(self, files: list[str], content_map: dict[str, str]):
        """Write files, mock git ls-files, run main(), return (exit_code, stdout_lines)."""
        for rel, content in content_map.items():
            self.write_file(rel, content)
        git_out = "\n".join(files) + "\n"
        with patch("subprocess.check_output", return_value=git_out):
            with patch("os.path.abspath", return_value=self.repo_root):
                printed = []
                with patch("builtins.print", side_effect=lambda *a: printed.append(" ".join(str(x) for x in a))):
                    with patch("sys.exit") as mock_exit:
                        check_secrets_manifest.main()
                        exit_code = mock_exit.call_args[0][0]
        return exit_code, printed

    # ------------------------------------------------------------------ #
    # Sanctioned files                                                      #
    # ------------------------------------------------------------------ #

    def test_sanctioned_docker_compose_ignored(self):
        """docker-compose.yml is always skipped even if it has a literal value."""
        exit_code, _ = self._run(
            ["docker-compose.yml"],
            {"docker-compose.yml": "POSTGRES_PASSWORD: my-secret-value\n"},
        )
        self.assertEqual(exit_code, 0)

    def test_sanctioned_env_example_ignored(self):
        """backend/.env.local.example is always skipped."""
        exit_code, _ = self._run(
            ["backend/.env.local.example"],
            {"backend/.env.local.example": "API_KEY=real_secret\n"},
        )
        self.assertEqual(exit_code, 0)

    # ------------------------------------------------------------------ #
    # Safe values — must exit 0                                             #
    # Fixtures live under .github/workflows/ -- an in-scope path per        #
    # ADR-0017 §2.4 (CI workflow) -- so these exercise check_file()'s       #
    # value-safety logic rather than being skipped by the scope gate.       #
    # ------------------------------------------------------------------ #

    def test_vault_reference_is_safe(self):
        """ZTAX_TEST_REF=vault://cells/eu-west-1/db must pass."""
        exit_code, _ = self._run(
            [".github/workflows/config.yml"],
            {".github/workflows/config.yml": "ZTAX_TEST_REF=vault://cells/eu-west-1/db\n"},
        )
        self.assertEqual(exit_code, 0)

    def test_shell_substitution_is_safe(self):
        """DB_PASSWORD=${DB_PASSWORD} must pass."""
        exit_code, _ = self._run(
            [".github/workflows/config.yml"],
            {".github/workflows/config.yml": "DB_PASSWORD=${DB_PASSWORD}\n"},
        )
        self.assertEqual(exit_code, 0)

    def test_ci_only_is_safe(self):
        """POSTGRES_PASSWORD: ci-only must pass."""
        exit_code, _ = self._run(
            [".github/workflows/config.yml"],
            {".github/workflows/config.yml": "POSTGRES_PASSWORD: ci-only\n"},
        )
        self.assertEqual(exit_code, 0)

    def test_local_dev_only_is_safe(self):
        """POSTGRES_PASSWORD: local-dev-only must pass."""
        exit_code, _ = self._run(
            [".github/workflows/config.yml"],
            {".github/workflows/config.yml": "POSTGRES_PASSWORD: local-dev-only\n"},
        )
        self.assertEqual(exit_code, 0)

    def test_gha_expression_is_safe(self):
        """password: ${{ secrets.GITHUB_TOKEN }} must pass (GHA expression)."""
        exit_code, _ = self._run(
            [".github/workflows/config.yml"],
            {".github/workflows/config.yml": "password: ${{ secrets.GITHUB_TOKEN }}\n"},
        )
        self.assertEqual(exit_code, 0)

    def test_local_reference_is_safe(self):
        """ZTAX_DB=local://postgres must pass."""
        exit_code, _ = self._run(
            [".github/workflows/config.yml"],
            {".github/workflows/config.yml": "ZTAX_DB=local://postgres\n"},
        )
        self.assertEqual(exit_code, 0)

    # ------------------------------------------------------------------ #
    # Literal secrets — must exit 1                                         #
    # ------------------------------------------------------------------ #

    def test_literal_password_fails(self):
        """password: hunter2realpass must be flagged (lowercase key, yaml form)."""
        exit_code, printed = self._run(
            [".github/workflows/config.yml"],
            {".github/workflows/config.yml": "password: hunter2realpass\n"},
        )
        self.assertEqual(exit_code, 1)
        self.assertTrue(any("password" in line for line in printed), printed)

    def test_api_key_camelcase_fails(self):
        """apiKey: AKIAIOSFODNN7EXAMPLE must be flagged (camelCase key)."""
        exit_code, printed = self._run(
            [".github/workflows/config.yml"],
            {".github/workflows/config.yml": "apiKey: AKIAIOSFODNN7EXAMPLE\n"},
        )
        self.assertEqual(exit_code, 1)
        self.assertTrue(any("apiKey" in line for line in printed), printed)

    def test_dotted_value_with_secret_word_fails(self):
        """DB_PASSWORD=my.real.secret.value must be flagged (dotted value, secret word)."""
        exit_code, printed = self._run(
            [".github/workflows/config.yml"],
            {".github/workflows/config.yml": "DB_PASSWORD=my.real.secret.value\n"},
        )
        self.assertEqual(exit_code, 1)
        self.assertTrue(any("DB_PASSWORD" in line for line in printed), printed)

    def test_token_with_trailing_comma_fails(self):
        """AUTH_TOKEN=ghp_abc..., must be flagged (trailing comma stripped first)."""
        exit_code, printed = self._run(
            [".github/workflows/config.yml"],
            {".github/workflows/config.yml": "AUTH_TOKEN=ghp_abcdefghijklmnopqrstuvwxyz0123456789,\n"},
        )
        self.assertEqual(exit_code, 1)
        self.assertTrue(any("AUTH_TOKEN" in line for line in printed), printed)

    def test_literal_secret_uppercase(self):
        """SECRET_KEY=12345 must be flagged."""
        exit_code, _ = self._run(
            [".github/workflows/config.yml"],
            {".github/workflows/config.yml": "SECRET_KEY=12345\n"},
        )
        self.assertEqual(exit_code, 1)

    def test_disguised_secret_with_safe_substring_fails(self):
        """DB_PASSWORD: actualSecret123vault://suffix must fail (not a pure vault:// reference)."""
        exit_code, printed = self._run(
            [".github/workflows/config.yml"],
            {".github/workflows/config.yml": "DB_PASSWORD: actualSecret123vault://notreallyasecretithink\n"},
        )
        self.assertEqual(exit_code, 1)
        self.assertTrue(any("DB_PASSWORD" in line for line in printed), printed)

    # ------------------------------------------------------------------ #
    # URI / connection-string checks                                        #
    # ------------------------------------------------------------------ #

    def test_literal_connection_string_fails(self):
        """postgres://user:literal_pass@host must be flagged."""
        exit_code, _ = self._run(
            ["infra/tofu/config.json"],
            {"infra/tofu/config.json": '{"db": "postgres://user:literal_pass@host/db"}\n'},
        )
        self.assertEqual(exit_code, 1)

    def test_safe_local_connection_string_passes(self):
        """postgres://user:local-dev-only@host must pass."""
        exit_code, _ = self._run(
            [".github/workflows/config.yml"],
            {".github/workflows/config.yml": "DB_URL=postgres://user:local-dev-only@postgres:5432/ztax\n"},
        )
        self.assertEqual(exit_code, 0)

    # ------------------------------------------------------------------ #
    # Scope narrowing (ADR-0017 §2.4): only config/manifest/CI-workflow     #
    # files are in scope -- a data/schema catalog is not, regardless of    #
    # how secret-shaped its field names look.                              #
    # ------------------------------------------------------------------ #

    def test_data_catalog_outside_scope_not_flagged(self):
        """A privacy/data-classification catalog (not a deployment manifest
        or config file) must not be scanned at all, even though its
        "..._key" fields open a nested object -- the same shape that used to
        produce 13 false positives in backend/migrations/privacy_catalog.json."""
        exit_code, printed = self._run(
            ["backend/migrations/privacy_catalog.json"],
            {
                "backend/migrations/privacy_catalog.json": (
                    '{\n  "accumulator_key": {\n    "class": "P0"\n  }\n}\n'
                )
            },
        )
        self.assertEqual(exit_code, 0)
        self.assertEqual(printed, ["OK: no secret-shaped values found outside the sanctioned files (ADR-0017 §2.4)."])

    def test_topology_key_not_flagged(self):
        """topologyKey: topology.kubernetes.io/zone is a k8s spread-constraint
        field, not a secret -- must not be flagged even inside an in-scope
        Kubernetes manifest."""
        exit_code, _ = self._run(
            ["infra/kubernetes/base/core.yaml"],
            {"infra/kubernetes/base/core.yaml": "topologyKey: topology.kubernetes.io/zone\n"},
        )
        self.assertEqual(exit_code, 0)

    def test_secret_key_ref_structural_reference_not_flagged(self):
        """A genuine secretKeyRef/ExternalSecret-style k8s reference -- key:
        dsn, secretKey: dsn, key: ztax/euc1-dev-01/database/migrate -- names
        WHERE a secret lives, not the secret itself, and must not be flagged."""
        exit_code, _ = self._run(
            ["infra/kubernetes/overlays/euc1-dev-01/migrate-credential.yaml"],
            {
                "infra/kubernetes/overlays/euc1-dev-01/migrate-credential.yaml": (
                    "secretKey: dsn\n"
                    "key: ztax/euc1-dev-01/database/migrate\n"
                )
            },
        )
        self.assertEqual(exit_code, 0)

    def test_secret_key_ref_with_real_credential_still_flagged(self):
        """The structural carve-out must not swallow a real pasted credential
        placed in the same secretKey/key position -- a long, unbroken,
        random-looking value still fails."""
        exit_code, printed = self._run(
            ["infra/kubernetes/overlays/euc1-dev-01/migrate-credential.yaml"],
            {
                "infra/kubernetes/overlays/euc1-dev-01/migrate-credential.yaml": (
                    "secretKey: AKIAIOSFODNN7EXAMPLE\n"
                )
            },
        )
        self.assertEqual(exit_code, 1)
        self.assertTrue(any("secretKey" in line for line in printed), printed)

    # ------------------------------------------------------------------ #
    # Gap closure: _is_structural_key_reference() required only "short and  #
    # alnum" to treat a value as a safe field-name reference, so a short    #
    # real secret (admin123, hunter2) was indistinguishable from a field    #
    # name by shape alone. It now also requires the value to match a known #
    # field-name word, a "/"-path, or a "."-path with no secret-word        #
    # segment.                                                              #
    # ------------------------------------------------------------------ #

    def test_secret_key_short_real_secret_fails(self):
        """secretKey: hunter2 is a plain short secret, not a recognizable
        field-name word or a path -- must be flagged (was wrongly passed
        before this fix: 'hunter2' matched the old bare-alnum carve-out)."""
        exit_code, printed = self._run(
            ["infra/kubernetes/base/core.yaml"],
            {"infra/kubernetes/base/core.yaml": "secretKey: hunter2\n"},
        )
        self.assertEqual(exit_code, 1)
        self.assertTrue(any("secretKey" in line for line in printed), printed)

    def test_key_dsn_still_passes(self):
        """key: dsn is a recognized field-name word -- must still pass."""
        exit_code, _ = self._run(
            ["infra/kubernetes/base/core.yaml"],
            {"infra/kubernetes/base/core.yaml": "key: dsn\n"},
        )
        self.assertEqual(exit_code, 0)

    def test_secret_key_vault_style_path_still_passes(self):
        """secretKey: ztax/euc1-dev-01/database/migrate is a vault-style
        store path, not a secret value -- must still pass."""
        exit_code, _ = self._run(
            ["infra/kubernetes/base/core.yaml"],
            {
                "infra/kubernetes/base/core.yaml": (
                    "secretKey: ztax/euc1-dev-01/database/migrate\n"
                )
            },
        )
        self.assertEqual(exit_code, 0)

    def test_bare_key_field_name_with_real_secret_fails(self):
        """key: admin123 must now be flagged. key/secretKey/secretKeyRef are
        routed straight into _is_structural_key_reference() regardless of
        the secret_key_re keyword gate (which never matched bare "key" by
        itself), so a real secret value in one of these three field names
        is no longer skipped just because the field name isn't a generic
        secret-shaped keyword."""
        exit_code, printed = self._run(
            ["infra/kubernetes/base/core.yaml"],
            {"infra/kubernetes/base/core.yaml": "key: admin123\n"},
        )
        self.assertEqual(exit_code, 1)
        self.assertTrue(any("key" in line for line in printed), printed)

    # ------------------------------------------------------------------ #
    # Regression: the 3 planted-secret cases from the original detection   #
    # proof, re-run against the narrowed scope and tightened keyword list  #
    # to confirm detection was not weakened.                                #
    # ------------------------------------------------------------------ #

    def test_regression_literal_secret_still_caught(self):
        """ZTAX_API_SECRET=sk_live_abcdef1234567890realkey must still be flagged."""
        exit_code, printed = self._run(
            [".github/workflows/config.yml"],
            {".github/workflows/config.yml": "ZTAX_API_SECRET=sk_live_abcdef1234567890realkey\n"},
        )
        self.assertEqual(exit_code, 1)
        self.assertTrue(any("ZTAX_API_SECRET" in line for line in printed), printed)

    def test_regression_disguised_vault_suffix_still_caught(self):
        """DB_PASSWORD: actualSecret123vault://notreallyasecretithink must still
        be flagged (a safe-looking substring is not enough)."""
        exit_code, printed = self._run(
            [".github/workflows/config.yml"],
            {
                ".github/workflows/config.yml": (
                    "DB_PASSWORD: actualSecret123vault://notreallyasecretithink\n"
                )
            },
        )
        self.assertEqual(exit_code, 1)
        self.assertTrue(any("DB_PASSWORD" in line for line in printed), printed)

    def test_regression_legitimate_vault_reference_still_passes(self):
        """ZTAX_TEST_REF=vault://cells/eu-west-1/db must still pass."""
        exit_code, _ = self._run(
            [".github/workflows/config.yml"],
            {".github/workflows/config.yml": "ZTAX_TEST_REF=vault://cells/eu-west-1/db\n"},
        )
        self.assertEqual(exit_code, 0)


if __name__ == "__main__":
    unittest.main()
