<!--
  SCRATCH DRAFT: K6 design note
  STATUS: Working draft for the lead and the domain owner.
  Every recommendation here is a SUGGESTION until the owners agree.
  The schema, the repo tool and the signing belong to other people.
-->

# K6: release manifest and metrics for the gateway

## 1. Purpose

A release manifest names the one approved combination of model, prompt, checks and settings that may serve live calls (`contracts/schemas/ai/ai-release-manifest.schema.json:4`). Its version is meant to be in every evidence record (same line). Metrics are needed so an operator can see refusals, slow calls and write failures before a customer does; today nothing sends them (section 4).

## 2. What the manifest must contain (suggestion)

| Field | Meaning | Where it comes from today | Exists? |
|---|---|---|---|
| model id and version | The pinned model | `model_profile_id`, `resolved_model_id` (`production_registries.py:1002-1005`); live demo route names only a profile (`config/registry.live-demo.json:17`) | partly (no resolved id in config: NOT FOUND) |
| provider profile | Provider used | `config/registry.live-demo.json:18` | partly (not in the schema) |
| prompt profile id | Which prompt | `prompt_profile_id` (`ai-release-manifest.schema.json:39`) | yes |
| prompt hash | SHA-256 of the prompt | Computed, never read from the file (`prompt_profile_file.py:5-6`, `production_registries.py:348`); set on a record at `server.py:737` | partly (not in the schema) |
| ai train version | Train the Gateway runs | `server.py:235`, `gateway_factory.py:81,138` | partly (not in the schema) |
| registry and routing version | Version of the use-case routes | No version key in `config/registry.live-demo.json` | no |
| adversarial gate (21 cases) | All 21 cases pass | `ReleaseGate().check()` (`evaluation_adversarial.py:1526-1566`) | partly (runs, result not stored) |
| harness report and grade | Report, `evidence_grade`, verdict | `tools/evaluate_gateway.py:171-179,485-496,887` | partly (not stored with a release) |
| thresholds file and status | Which thresholds, signed or not | `gold/thresholds.v0.draft.json:4,6-9` (DRAFT, `signed_by` null) | partly |
| approved ontology list version | Version of the code list | `list_version` (`config/approved_ontology.live-demo.json:2`) | partly (synthetic) |
| signed decisions and signer | D-01, D-05, D-06, D-09, D-10 and who signed | D-01 text and signer not written (`_drafts/S5_hosting_paper_draft.md:13`); D-05 not in the repo (`EVIDENCE.md:64`); D-10 not signed (`_drafts/S7_reviewer_storage_note.md:5`); D-06: NOT FOUND | no |
| evidence sink setting | File sink on or off | `ENV_PATH` (`evidence_file_sink.py:83`) | partly (an env var, not recorded) |
| build or commit id | Code that was built | NOT FOUND in `server.py` or `tools/evaluate_gateway.py`; the repo tool takes `--commit` (`docs/tools/build_release_manifest.py:294`) | no |
| date | When it was approved | `approved_at` (`ai-release-manifest.schema.json:49`) | yes |

The prompt hash and ids are fine as manifest fields: they are hashes and names, not prompt text.

## 3. What the schema allows, and the repo tool

- The schema has 18 required fields (`ai-release-manifest.schema.json:8-27`) and `additionalProperties: false` (line 7). Its comment says the fields are exactly `AIReleaseManifest.as_dict()` (line 5, `production_registries.py:1078-1099`).
- The manifest hash is SHA-256 over the other fields (`ai-release-manifest.schema.json:5`).
- It has no place for: provider profile, prompt hash, ai train version, gate result, harness grade, thresholds status, ontology list version, decisions, sink setting, commit. **Each needs a schema change**, or a second document that points at the manifest by `manifest_hash`. Which one is the owners' choice.
- The repo tool builds a different thing, a `ReleaseCertificationManifest` (`docs/tools/build_release_manifest.py:1-2`): version, commit, traceability, waivers, quarantine, defects, `rollback_ref` (lines 194-205). It has no AI fields. It does hold a commit id and a tamper-evident hash (lines 90-99), which this note could reuse.
- Lifecycle states exist (`release_lifecycle.py:119`), and only some may serve live traffic (`release_lifecycle.py:270`). Nothing there reads the gate result.

## 4. Metrics

**Emitted today: NOT FOUND.** `record_signals` derives signals from a record (`observability.py:645`) and it is called inside `observability.py` (`ObservabilityPipeline.observe`, `observability.py:830`), but nothing outside `observability.py` calls that class (searched `src/` and `tools/`; `server.py` and `tools/` do not use it). Exporter: NOT FOUND; the file says one comes later (`observability.py:49-50`). The label rule is already written: no prompt, completion or source text (`observability.py:28-29,169-173`).

Suggested metrics. Labels come from fixed sets only.

| Name | Meaning | Labels |
|---|---|---|
| `calls_total` | Calls by outcome | `invocation_outcome`, `use_case` (`observability.py:332-337`) |
| `refusals_total` | Refusals by reason | `reason` (guardrail verdict, `observability.py:507-512`) |
| `latency_ms` | Time per call | `use_case`, `provider` (`observability.py:354`) |
| `runtime_timeouts_total` | Runtime timed out | `use_case` (`server.py:621`) |
| `runtime_slots_in_use` | Slots held, of 8 | none (`server.py:119,252,569`) |
| `evidence_write_failures_total` | File copy failed | none (`evidence_file_sink.py:322`); oversize drops are separate (`:327`) |
| `answer_check_total` | Guard verdicts | `verdict` (`invocation_check_event.py:75-78`) |
| `tokens_total` | Tokens used | `direction` = input or output (`observability.py:360-375`) |

Never a label: prompt text, answer text, code values, subject refs. Tenant ids: on hold, see question 5. The existing label `ai_release_manifest_id` (`observability.py:349`) is allowed.

## 5. When a release is blocked (suggestion)

Block unless all of these are present and passing:
1. The manifest is sealed and `verify_hash` holds (`production_registries.py:1101`).
2. All 21 adversarial cases pass (`evaluation_adversarial.py:1534`). **This is the demo release check only.** The production gate also needs the 54-case catalogue and a red-team pass. The ADD01 section 9 text is not in this repo; I only found it cited (`gold/README.md:148`, `gold/thresholds.v0.draft.json:41-46`), so what it exactly requires is NOT FOUND.
3. The harness grade is `REVIEWED` and the verdict is not `NOT_EVALUATED` (`tools/evaluate_gateway.py:485-496,887`).
4. The thresholds file is signed (`gold/thresholds.v0.draft.json:6-9`).
5. The approved list, prompt hash, train version and commit id are in the manifest.
6. The needed decisions are signed.

**Today this rule blocks everything.** Thresholds are DRAFT with no signer (`thresholds.v0.draft.json:4,6-9`). The harness always reports `NOT_EVALUATED` while thresholds are undefined (`tools/evaluate_gateway.py:179`). The 54 slots are all EMPTY (`gold/README.md:156`). The gold cases are synthetic, so the grade is `PIPELINE_CHECK` (`tools/evaluate_gateway.py:485-496`).

## 6. Questions for the lead and the domain owner

1. Who owns release signing now? (one name)
2. Where is the signed manifest stored? (one name or place)
3. Which suite is the production gate: the 21 cases, or 21 plus the 54-case catalogue plus a red-team pass? (answer "21" or "all")
4. Does the manifest get a schema change for the extra fields, or a second document? (answer "schema" or "second")
5. Is the tenant id allowed in the manifest or in any metric label? (yes or no; on hold, the red-team case ADV-010 forbids it in any record or log line, `evaluation_adversarial.py:889-894`)
6. Who signs the thresholds file? (one name)
7. Is the ADD01 section 9 text available to engineering? (yes or no)
8. What is decision D-06? (one name for its owner)
9. May a release go out with `PIPELINE_CHECK` grade for the demo only? (yes or no)

## 7. Out of scope

- Signing, key handling and where signatures are kept.
- Storage of the manifest.
- Dashboards, alert rules and an exporter.
- Changing the schema, the repo tool or any code.
- The real thresholds and the real catalogue content.
