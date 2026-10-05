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
    # ------------------------------------------------------------------ #

    def test_vault_reference_is_safe(self):
        """ZTAX_TEST_REF=vault://cells/eu-west-1/db must pass."""
        exit_code, _ = self._run(
            ["config.yml"],
            {"config.yml": "ZTAX_TEST_REF=vault://cells/eu-west-1/db\n"},
        )
        self.assertEqual(exit_code, 0)

    def test_shell_substitution_is_safe(self):
        """DB_PASSWORD=${DB_PASSWORD} must pass."""
        exit_code, _ = self._run(
            ["config.yml"],
            {"config.yml": "DB_PASSWORD=${DB_PASSWORD}\n"},
        )
        self.assertEqual(exit_code, 0)

    def test_ci_only_is_safe(self):
        """POSTGRES_PASSWORD: ci-only must pass."""
        exit_code, _ = self._run(
            ["config.yml"],
            {"config.yml": "POSTGRES_PASSWORD: ci-only\n"},
        )
        self.assertEqual(exit_code, 0)

    def test_local_dev_only_is_safe(self):
        """POSTGRES_PASSWORD: local-dev-only must pass."""
        exit_code, _ = self._run(
            ["config.yml"],
            {"config.yml": "POSTGRES_PASSWORD: local-dev-only\n"},
        )
        self.assertEqual(exit_code, 0)

    def test_gha_expression_is_safe(self):
        """password: ${{ secrets.GITHUB_TOKEN }} must pass (GHA expression)."""
        exit_code, _ = self._run(
            ["config.yml"],
            {"config.yml": "password: ${{ secrets.GITHUB_TOKEN }}\n"},
        )
        self.assertEqual(exit_code, 0)

    def test_local_reference_is_safe(self):
        """ZTAX_DB=local://postgres must pass."""
        exit_code, _ = self._run(
            ["config.yml"],
            {"config.yml": "ZTAX_DB=local://postgres\n"},
        )
        self.assertEqual(exit_code, 0)

    # ------------------------------------------------------------------ #
    # Literal secrets — must exit 1                                         #
    # ------------------------------------------------------------------ #

    def test_literal_password_fails(self):
        """password: hunter2realpass must be flagged (lowercase key, yaml form)."""
        exit_code, printed = self._run(
            ["config.yml"],
            {"config.yml": "password: hunter2realpass\n"},
        )
        self.assertEqual(exit_code, 1)
        self.assertTrue(any("password" in line for line in printed), printed)

    def test_api_key_camelcase_fails(self):
        """apiKey: AKIAIOSFODNN7EXAMPLE must be flagged (camelCase key)."""
        exit_code, printed = self._run(
            ["config.yml"],
            {"config.yml": "apiKey: AKIAIOSFODNN7EXAMPLE\n"},
        )
        self.assertEqual(exit_code, 1)
        self.assertTrue(any("apiKey" in line for line in printed), printed)

    def test_dotted_value_with_secret_word_fails(self):
        """DB_PASSWORD=my.real.secret.value must be flagged (dotted value, secret word)."""
        exit_code, printed = self._run(
            ["config.yml"],
            {"config.yml": "DB_PASSWORD=my.real.secret.value\n"},
        )
        self.assertEqual(exit_code, 1)
        self.assertTrue(any("DB_PASSWORD" in line for line in printed), printed)

    def test_token_with_trailing_comma_fails(self):
        """AUTH_TOKEN=ghp_abc..., must be flagged (trailing comma stripped first)."""
        exit_code, printed = self._run(
            ["config.yml"],
            {"config.yml": "AUTH_TOKEN=ghp_abcdefghijklmnopqrstuvwxyz0123456789,\n"},
        )
        self.assertEqual(exit_code, 1)
        self.assertTrue(any("AUTH_TOKEN" in line for line in printed), printed)

    def test_literal_secret_uppercase(self):
        """SECRET_KEY=12345 must be flagged."""
        exit_code, _ = self._run(
            ["config.yml"],
            {"config.yml": "SECRET_KEY=12345\n"},
        )
        self.assertEqual(exit_code, 1)

    def test_disguised_secret_with_safe_substring_fails(self):
        """DB_PASSWORD: actualSecret123vault://suffix must fail (not a pure vault:// reference)."""
        exit_code, printed = self._run(
            ["config.yml"],
            {"config.yml": "DB_PASSWORD: actualSecret123vault://notreallyasecretithink\n"},
        )
        self.assertEqual(exit_code, 1)
        self.assertTrue(any("DB_PASSWORD" in line for line in printed), printed)

    # ------------------------------------------------------------------ #
    # URI / connection-string checks                                        #
    # ------------------------------------------------------------------ #

    def test_literal_connection_string_fails(self):
        """postgres://user:literal_pass@host must be flagged."""
        exit_code, _ = self._run(
            ["config.json"],
            {"config.json": '{"db": "postgres://user:literal_pass@host/db"}\n'},
        )
        self.assertEqual(exit_code, 1)

    def test_safe_local_connection_string_passes(self):
        """postgres://user:local-dev-only@host must pass."""
        exit_code, _ = self._run(
            ["config.yml"],
            {"config.yml": "DB_URL=postgres://user:local-dev-only@postgres:5432/ztax\n"},
        )
        self.assertEqual(exit_code, 0)


if __name__ == "__main__":
    unittest.main()
