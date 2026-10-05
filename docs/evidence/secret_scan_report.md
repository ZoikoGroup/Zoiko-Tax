# Secret Scanning Report
Generated: Mon Oct  5 05:00:40 UTC 2026
Commit: 75049c00a46614546fd78d90c599a580cb9a1030

## Scope narrowing (this revision)

The previous revision of this report scanned every non-code, non-excluded
file in the repo and surfaced 17 violations, 13 of which were in
`backend/migrations/privacy_catalog.json` -- a privacy/data-classification
catalog, not a deployment manifest or environment config. The remaining 4
were in genuine infra/ manifests but were themselves false positives: k8s
field-name/reference values (`topologyKey`, `secretKeyRef`-style `key` /
`secretKey` fields) that name *where* a secret lives, not the secret itself.

Both problems are fixed in `docs/tools/check_secrets_manifest.py`:

1. **File selection is now an include-list, not an exclude-list.**
   ZTAX-SEC-REQ-0136 (`docs/requirements.yaml`), whose `source_authority` is
   ADR-0017 §2.4, states: *"No secret MUST be carried as a literal value in
   a configuration file, deployment manifest, or CI workflow at rest..."*
   `is_in_scope()` now scans only files that are actually one of those
   three things: dotenv files (`^\.env(\..+)?$`), `docker-compose*.yml`,
   Kubernetes manifests (`infra/kubernetes/**`), Kyverno admission-policy
   manifests (`policy/admission/**` -- these are deployment-gating
   manifests applied to the cluster, the same category as the Kubernetes
   manifests ADR-0017 names), Tofu/Terraform (`infra/tofu/**`), and CI
   workflows (`.github/workflows/**`). A data/schema catalog such as
   `backend/migrations/privacy_catalog.json` is none of these and is no
   longer scanned, regardless of what its field names look like.
2. **The keyword match no longer matches a bare `"key"` substring.**
   `secret_key_re` dropped `key` and added `private_key`/`access_key`
   explicitly, so `topologyKey` and a bare `key:` field no longer match at
   all. For `secretKey`/`secretKeyRef` (which still match via the `secret`
   substring, by design), a new structural carve-out
   (`_is_structural_key_reference`) skips the value only when it is
   field-name-shaped or a vault-style path (letters/digits/`._-/`, no
   spaces, no long unbroken random-looking run) -- e.g. `dsn` or
   `ztax/euc1-dev-01/database/migrate`. A real credential pasted into the
   same field (e.g. an AWS access key id, a long unbroken alphanumeric
   string) still fails the "looks random" check and is still flagged; see
   `test_secret_key_ref_with_real_credential_still_flagged`.

Neither change weakens detection of an actual literal secret -- see
"Verified detection capability" below.

## Scope
- Total files considered: 2080 (`git ls-files --cached --others
  --exclude-standard`)
- Excluded:
  - `.git/`, `node_modules/`, `vendor/` -- not source the team authors
    (1509 files)
  - `complete-info-docs/` and `docs/specs/` -- spec/reference text, not
    deployable config (16 files)
  - Everything that is not config/manifest/CI-workflow-shaped per
    `is_in_scope()` above -- includes `backend/migrations/**` (SQL
    migrations and the `privacy_catalog.json` data catalog), all source
    code, SDKs, build tooling, golden test fixtures, and documentation
    (508 files)
  - SANCTIONED_FILES allowlist (3 files):
    - `docker-compose.yml` -- local dev stack, placeholder values only
    - `backend/.env.local.example` -- designated dev-placeholder file per
      ADR-0017 §2.4
    - `.github/workflows/ci.yml` -- CI-only test credentials
- Total files scanned: 44 (config/manifest/CI-workflow files, minus the 3
  sanctioned)

## Manifest scan (ADR-0017 §2.4 / ZTAX-SEC-REQ-0136)

Command: `python docs/tools/check_secrets_manifest.py`
Exit code: 0

```
OK: no secret-shaped values found outside the sanctioned files (ADR-0017 §2.4).
```

Zero violations against the current repo, including every new infra/
manifest from commit 990a7a2 ("infra: regional cell blueprint, manifests
and admission policy"). The 4 infra/ findings from the previous revision of
this report (`infra/kubernetes/base/ztax-core.yaml:60` `topologyKey`,
`infra/kubernetes/base/ztax-migrate-job.yaml:59` `key: dsn`,
`infra/kubernetes/overlays/euc1-dev-01/migrate-credential.yaml:36,38`
`secretKey: dsn` / `key: ztax/euc1-dev-01/database/migrate`) and the 13
`privacy_catalog.json` findings are all confirmed false positives and are
gone under the fixed detection logic and narrowed scope -- not suppressed
via `SANCTIONED_FILES`, and no real secret value was ever present in any of
them.

## Checked but not sanctioned

`.github/workflows/intelligence.yml` and `.github/workflows/release.yml`
were checked and found to contain zero secret-shaped values, so they were
deliberately not added to `SANCTIONED_FILES`:

- `intelligence.yml`: no occurrences of `password|secret|token|api_key|
  apikey|credential` (case-insensitive) anywhere in the file.
- `release.yml`: one occurrence, `password: ${{ secrets.GITHUB_TOKEN }}`
  (line 70), which is a GitHub Actions expression -- the entire value
  matches the scanner's safe-exact pattern for `${{ ... }}` expressions, so
  it correctly produces no violation.

## Git-history / diff scan (gitleaks, ZTAX-SEC-001 doctrine)

`gitleaks` is not installed in this environment (`gitleaks version` and
`command -v gitleaks` both fail with "command not found"). It could not be
run here, and no result is fabricated. Git-history scanning via gitleaks
runs in CI per `db2936e` ("ci: secret scanning (gitleaks + ADR-0017 manifest
check)"); this local environment is not equivalent to that CI runner and
this report does not claim gitleaks coverage was exercised locally.

## Verified detection capability

Detection was proven with 3 planted test cases, re-run against the fixed
scanner (temporarily, as an untracked file under `.github/workflows/`, then
removed -- `git status --short` confirms no stray file remains):

```
ZTAX_API_SECRET=sk_live_abcdef1234567890realkey
DB_PASSWORD: actualSecret123vault://notreallyasecretithink
ZTAX_TEST_REF=vault://cells/eu-west-1/db
```

Real output:

```
VIOLATION: .github/workflows/_plant_test_tmp.yml:1 -- Literal secret assigned to 'ZTAX_API_SECRET' (ADR-0017 §2.4 / ZTAX-SEC-REQ-0136)
VIOLATION: .github/workflows/_plant_test_tmp.yml:2 -- Literal secret assigned to 'DB_PASSWORD' (ADR-0017 §2.4 / ZTAX-SEC-REQ-0136)
```

- Literal secret (line 1, `ZTAX_API_SECRET=...`) -- caught.
- Disguised secret using a safe-looking substring, the `vault://`-suffix
  evasion (line 2, `DB_PASSWORD: actualSecret123vault://...`) -- caught
  (the entire value, not a substring, must match the safe pattern).
- Legitimate `vault://` reference (line 3, `ZTAX_TEST_REF=vault://...`) --
  correctly produced no violation (no line-3 entry above).

The same 3 cases are codified as permanent regression tests in
`docs/tools/tests/test_check_secrets_manifest.py`:
`test_regression_literal_secret_still_caught`,
`test_regression_disguised_vault_suffix_still_caught`,
`test_regression_legitimate_vault_reference_still_passes`. Additional new
tests cover the two fixed false-positive classes:
`test_data_catalog_outside_scope_not_flagged`,
`test_topology_key_not_flagged`,
`test_secret_key_ref_structural_reference_not_flagged`, and
`test_secret_key_ref_with_real_credential_still_flagged` (proves the
structural carve-out doesn't swallow a real credential pasted into the same
field).

Full suite: `python -m unittest discover -s docs/tools/tests` -- 53 tests
(46 pre-existing + 7 new), all pass.

## Known limitations

- Binary files are not scanned for secret content.
- Only files recognized as configuration/manifest/CI-workflow-shaped (see
  "Scope narrowing" above) are scanned; a secret pasted into a file outside
  that include-list (e.g. a source file, a doc, a data catalog) is not
  caught by this check.
- This check does not cover git history -- that's gitleaks' job.
