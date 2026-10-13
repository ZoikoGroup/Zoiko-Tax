# AI Governance Gap Note

**Date:** 2026-10-09  
**Scope:** ZTAX-AIGOV-001 requirements (108 total)  
**Purpose:** Manual snapshot for lead review and prioritisation.

---

## Summary

| Group | Count | Meaning |
|:---|:---:|:---|
| **Linked to a test** | 17 | `verification_method: TEST` and `verification_ref` is set |
| **TEST method, no link yet** | 61 | Requires a test; ref is still null |
| **Non-TEST method** | 30 | Verification method: AUDIT / DEMONSTRATION / INSPECTION / ANALYSIS |
| **Total** | **108** | |

> Counts computed by read-only parse of `docs/requirements.yaml` on the date above.
> The three groups add up to 108. OK

---

## Linked to a test (17)

| Requirement | Short statement | Verification ref |
|:---|:---|:---|
| `ZTAX-AIGOV-REQ-0048` | RAG corpora MUST enforce source rights. | `intelligence/tests/test_privacy_source_rights.py::TestCheckCorpusSource::test_retrieval_denied_blocks_corpus` |
| `ZTAX-AIGOV-REQ-0050` | RAG retrieval MUST enforce tenant scope. | `intelligence/tests/test_ai_security_controls.py::TestAdversarialScenarios::test_adv_sec_002_cross_tenant_leakage_blocked` |
| `ZTAX-AIGOV-REQ-0053` | AI agents MUST use explicit tool allowlists. | `intelligence/tests/test_tool_broker.py::test_unknown_tool_is_refused` |
| `ZTAX-AIGOV-REQ-0055` | AI agents MUST NOT inherit ambient administrative privileges. | `intelligence/tests/test_tool_broker.py::test_privileged_action_class_cannot_be_registered` |
| `ZTAX-AIGOV-REQ-0056` | Side-effecting agent calls MUST be idempotency safe. | `intelligence/tests/test_tool_broker.py::test_mutate_call_without_token_is_refused` |
| `ZTAX-AIGOV-REQ-0057` | Agent step/tool/time/cost budgets MUST be bounded. | `intelligence/tests/test_tool_broker.py::test_steps_over_ceiling_is_refused` |
| `ZTAX-AIGOV-REQ-0064` | Improper-output-handling tests MUST be included where model output reaches code/query/tool layers. | `intelligence/tests/test_ai_security_controls.py::TestAdversarialScenarios::test_adv_sec_001_output_injection_via_prohibited_key` |
| `ZTAX-AIGOV-REQ-0066` | Unbounded-consumption controls MUST exist for production AI. | `intelligence/tests/test_tool_broker.py::test_token_cost_over_ceiling_is_refused` |
| `ZTAX-AIGOV-REQ-0069` | AI model routing MUST enforce allowed regions. | `intelligence/tests/test_resilience.py::test_cross_region_never_widened_to_us` |
| `ZTAX-AIGOV-REQ-0070` | AI fallback MUST NOT violate provider/data/region policy. | `intelligence/tests/test_resilience.py::test_secrets_data_class_is_refused_on_model_outage` |
| `ZTAX-AIGOV-REQ-0074` | Human review disposition MUST be auditable. | `intelligence/tests/test_human_review.py::test_full_audit_trail_item_then_approve` |
| `ZTAX-AIGOV-REQ-0079` | Production monitoring MUST measure abstention/coverage where applicable. | `intelligence/tests/test_drift.py::test_abstention_drift_detected` |
| `ZTAX-AIGOV-REQ-0082` | Production monitoring MUST include AI cost/consumption controls. | `intelligence/tests/test_capacity.py::test_consume_raises_on_token_limit_exceeded` |
| `ZTAX-AIGOV-REQ-0089` | AI provenance MUST record prompt/agent version. | `intelligence/tests/test_invocation_evidence.py::test_builder_sets_prompt_version_from_provenance` |
| `ZTAX-AIGOV-REQ-0090` | AI provenance MUST record retrieval evidence refs for grounded use cases. | `intelligence/tests/test_invocation_evidence.py::test_add_retrieval_ref_stores_citation_id` |
| `ZTAX-AIGOV-REQ-0091` | AI provenance MUST record tool calls for agentic workflows. | `intelligence/tests/test_invocation_evidence.py::test_add_tool_call_appends_record` |
| `ZTAX-AIGOV-REQ-0092` | AI provenance MUST record authority outcome (review/abstain/auto-adopt/block). | `intelligence/tests/test_invocation_evidence.py::test_builder_sets_authority_outcome_from_provenance` |

---

## TEST-method requirements with no link (61)

### (a) No control built yet -- 7 requirements

Writing a test for any of these would first require designing and implementing a new
control, which needs lead/architectural approval before any coding starts.

Confirmed by grep of `intelligence/src/ztax_gateway/`: no relevant implementation
found for any of the seven.

| Requirement | Statement |
|:---|:---|
| `ZTAX-AIGOV-REQ-0059` | Agent memory MUST be tenant/purpose/retention scoped. |
| `ZTAX-AIGOV-REQ-0061` | Prompt injection testing MUST be included for untrusted-input AI use cases. |
| `ZTAX-AIGOV-REQ-0062` | Retrieval/data poisoning tests MUST be included for RAG/content pipelines. |
| `ZTAX-AIGOV-REQ-0063` | Sensitive-information disclosure tests MUST be included. |
| `ZTAX-AIGOV-REQ-0097` | AI RegulatoryProfile MUST be effective-dated. |
| `ZTAX-AIGOV-REQ-0099` | Customer-facing AI transparency MUST follow jurisdiction/product profile. |
| `ZTAX-AIGOV-REQ-0101` | AI-generated forecasts/anomalies MUST be labelled appropriately. |

Note on REQ-0059: `AgentMemoryPolicy` is declared as an enum in `production_registries.py`
and is a field on `AgentProfile`, but there is no runtime enforcement of
tenant/purpose/retention scoping -- no enforcement gate, no test. This counts as
no control built.

### (b) A control exists but no clear test -- 3 requirements

The underlying mechanism is partially present but no single test clearly and fully
exercises the requirement. A targeted test could be written without major new design work.

| Requirement | Statement | What exists | What is missing |
|:---|:---|:---|:---|
| `ZTAX-AIGOV-REQ-0027` | Provider aliases such as `latest` MUST NOT govern material behaviour without qualification controls. | `ProviderRegistry` and `qualified_providers()` exist; providers have a `qualified` flag. | No test that a `latest`-alias or unversioned provider reference is refused or normalised at registration or routing. |
| `ZTAX-AIGOV-REQ-0047` | AI MUST NOT treat discovery web snippets as production legal authority. | `KnowledgeBase` and `TenantScopedIndex` enforce source-rights and tenant scope. | No test that a source marked as a web snippet or discovery-only source is excluded or down-ranked when constructing a governed explanation. |
| `ZTAX-AIGOV-REQ-0051` | Superseded legal sources MUST be excluded or explicitly historical-context tagged. | `KnowledgeBase.build()` accepts source documents; `CorpusSource` carries metadata. | No test that a superseded source is excluded from retrieval results or is tagged to prevent authoritative use. |

### (c) Reviewed and deliberately not linked -- 51 requirements

Each requirement below had a candidate link in the working copy of
`docs/requirements.yaml`. After reading the test body, one of the three
judgements below was made. No test has been changed; only `verification_ref`
was restored to null.

#### (c-i) Gateway does not enforce the control -- 2 requirements

| Requirement | Statement | Why not linked |
|:---|:---|:---|
| `ZTAX-AIGOV-REQ-0068` | AI processing MUST resolve approved data purpose before invocation. | The Python Gateway does not enforce a data-purpose lookup. The candidate test (`test_governance_gate_refuses_unregistered_use_case`) proves unknown-use-case refusal, not data-purpose resolution.Linking would overstate the coverage. |
| `ZTAX-AIGOV-REQ-0071` | Precise situs/CDR/network data MUST NOT enter general-purpose models absent explicit approved use case. | The Python Gateway does not enforce data-class restrictions at the level the requirement demands. The candidate test (`test_secrets_data_class_is_refused_regardless_of_failure_kind`) proves that `data_class=SECRETS` is refused on outage -- a resilience guard, not a situs/CDR/network-data gating decision. |

#### (c-ii) Test proves only part of the requirement -- held for a decision on the link standard -- 15 requirements

| Requirement | Statement | Candidate test | Why held |
|:---|:---|:---|:---|
| `ZTAX-AIGOV-REQ-0012` | AI MUST NOT bypass tenant, security, privacy, source-right or residency controls. | `test_server.py::test_a_call_from_another_region_is_refused` | Proves residency only; tenant, security, privacy, and source-right bypass are untested by this test. |
| `ZTAX-AIGOV-REQ-0036` | Evaluation MUST include adversarial/malformed input where applicable. | `test_evaluation_adversarial.py::test_builtin_suite_all_pass_via_full_run` | Runs the built-in adversarial suite (ADV-001 to ADV-005); however, malformed-input coverage is partial and the requirement is about evaluation policy rather than a single suite run. |
| `ZTAX-AIGOV-REQ-0039` | Gold datasets MUST be versioned. | `test_production_registries.py::TestDatasetProfile::test_empty_version_raises` | Proves that `DatasetProfile` enforces a non-empty version field, but does not prove that the version is immutable or tracked across updates -- both implied by "versioned". |
| `ZTAX-AIGOV-REQ-0044` | Evaluation evidence MUST pin exact model/prompt/agent/tool configuration. | `test_evaluation_evidence.py::test_record_fields_from_provenance` | Proves model and prompt profile are recorded; tool and agent configuration pinning is not asserted. |
| `ZTAX-AIGOV-REQ-0045` | Citation-based governed explanations MUST resolve citations to registered sources/evidence. | `test_citation.py::test_citation_verify_all_returns_true_for_intact_citation` | Proves SHA integrity of chunks; does not assert the citation resolves to a registered source. |
| `ZTAX-AIGOV-REQ-0054` | AI tool calls MUST be independently authorized. | `test_tool_broker.py::test_a_registered_call_is_authorised` | Shows a registered call passes authorisation; does not prove the path is a separate, independent gate. |
| `ZTAX-AIGOV-REQ-0058` | Agents MUST NOT mint/extend their own privileges or credentials. | `test_tool_broker.py::test_maximum_permitted_action_class_is_commit` | Checks a constant (`MAX_PERMITTED_ACTION_CLASS is COMMIT`); no live attempt to escalate privileges. Structural, not behavioural. |
| `ZTAX-AIGOV-REQ-0060` | Tool output MUST be schema-validated before acting on it. | `test_ai_security_controls.py::TestAdversarialScenarios::test_adv_sec_004_extra_key_blocked_under_strict_schema` | Proves `OutputSchemaValidator` rejects an extra key in isolation; does not prove validation is wired before any action is taken on tool output in the invocation path. |
| `ZTAX-AIGOV-REQ-0073` | Human review requirements MUST be encoded in workflow, not only documentation. | `test_human_review.py::test_submit_with_unknown_use_case_is_refused` | Proves use-case validation fires at submission; does not prove that the full set of human-review requirements is encoded in workflow. |
| `ZTAX-AIGOV-REQ-0075` | AI proposals MUST remain immutable when reviewer edits create final human decision. | `test_human_review.py::test_promote_approved_item_creates_promoted_record` | Proves the promotion flow works; does not assert that original proposal fields are unchanged after reviewer edits. |
| `ZTAX-AIGOV-REQ-0077` | AI MAY NOT count as one of two required human approvers. | `test_invocation_evidence.py::test_design_rule_4_auto_accept_at_a3_is_refused` | Proves auto-accept at A3 is refused, which enforces the spirit of the rule, but does not prove a two-approver count is tracked anywhere. |
| `ZTAX-AIGOV-REQ-0078` | Production monitoring MUST measure use-case quality indicators. | `test_observability.py::test_record_signals_returns_all_eight_families` | Proves eight signal families are emitted; does not verify that use-case-specific quality indicators (accuracy, abstention, coverage) are among them. |
| `ZTAX-AIGOV-REQ-0083` | T2/T3 monitoring MUST have thresholds for degrade/suspend/revalidate. | `test_drift.py::test_multi_drift_when_multiple_thresholds_breached` | Proves multiple thresholds trigger `MULTI_DRIFT`; does not test degrade/suspend/revalidate state transitions specific to T2/T3 tiers. |
| `ZTAX-AIGOV-REQ-0088` | AI provenance MUST record model/provider/version where available. | `test_invocation_evidence.py::test_builder_sets_provider_from_provenance` | Proves provider is recorded; version is not asserted separately. |
| `ZTAX-AIGOV-REQ-0100` | AI explanations MUST distinguish authoritative deterministic result from AI narrative. | `test_explanation.py::test_result_is_not_decision_type` | Proves `ExplanationResult` is a distinct type from `Decision` and `ReviewItem`; does not assert that the content correctly labels AI narrative vs. authoritative result. |

#### (c-iii) Test exercises a different behaviour entirely -- 34 requirements

| Requirement | Statement | Candidate test | Why dropped |
|:---|:---|:---|:---|
| `ZTAX-AIGOV-REQ-0003` | Every AIUseCase MUST declare an authority level A0-A5. | `test_resilience.py::test_governed_router_permits_a_registered_use_case` | Tests routing fallback; does not assert authority level declaration or validation. |
| `ZTAX-AIGOV-REQ-0004` | Every AIUseCase MUST declare an internal risk tier T0-T4. | `test_invocation_evidence.py::test_governance_gate_refuses_unregistered_use_case` | Proves unknown-use-case refusal; says nothing about risk tier declaration. |
| `ZTAX-AIGOV-REQ-0005` | Zoiko internal risk tier MUST remain separate from jurisdiction statutory AI classification. | `test_server.py::test_a5_is_refused_before_the_runtime_sees_the_payload` | Proves A5 is blocked; says nothing about separation of internal vs. statutory classification. |
| `ZTAX-AIGOV-REQ-0006` | AI MUST NOT autonomously create or amend an authoritative tax/legal rule. | `test_resilience.py::test_governed_router_refuses_a_killed_use_case` | Kill-switch test; completely different concern from autonomous rule creation. |
| `ZTAX-AIGOV-REQ-0007` | AI MUST NOT autonomously promote a rule/country pack to production. | `test_resilience.py::test_governed_router_refuses_when_global_kill_is_engaged` | Global kill-switch test; nothing about autonomous country-pack promotion. |
| `ZTAX-AIGOV-REQ-0008` | AI MUST NOT autonomously authorize or sign a tax/regulatory filing. | `test_resilience.py::test_governance_gate_runs_before_a_fallback_is_chosen` | Proves governance gate ordering; nothing about filing authorization. |
| `ZTAX-AIGOV-REQ-0009` | AI MUST NOT autonomously authorize remittance or movement of funds. | `test_production_registries.py::TestPromptProfile::test_happy_path` | Constructs a `PromptProfile`; completely unrelated to fund authorization. |
| `ZTAX-AIGOV-REQ-0010` | AI MUST NOT approve customer POA/representation authority. | `test_resilience.py::test_data_class_restricted_provider_not_selected_for_wrong_class` | Provider/data-class routing test; no connection to customer POA. |
| `ZTAX-AIGOV-REQ-0011` | AI MUST NOT delete/alter sealed evidence or legal holds. | `test_resilience.py::test_governed_router_refuses_an_unregistered_use_case` | Unknown-use-case refusal; no relation to sealed evidence integrity. |
| `ZTAX-AIGOV-REQ-0013` | AI outputs MUST carry machine-readable authority/outcome state. | `test_resilience.py::test_data_class_restricted_provider_not_selected_for_wrong_class` | Provider/data-class routing test; does not verify output authority-state. |
| `ZTAX-AIGOV-REQ-0014` | Prose confidence assertion MUST NOT imply authority. | `test_resilience.py::test_integration_deprecated_models_excluded_from_fallback` | Model exclusion test; unrelated to prose confidence/authority labelling. |
| `ZTAX-AIGOV-REQ-0015` | Constrained automation MUST use a pre-approved bounded action set. | `test_resilience.py::test_cross_region_never_widened_to_us` | Region-lock test; nothing about bounded action sets. |
| `ZTAX-AIGOV-REQ-0016` | Constrained automation MUST have independent deterministic validation. | `test_resilience.py::test_only_qualified_providers_selected_as_fallback` | Provider qualification test; no connection to deterministic validation. |
| `ZTAX-AIGOV-REQ-0017` | Constrained automation MUST abstain on conflict/unsupported/threshold failure. | `test_server.py::test_serve_refuses_to_listen_without_mtls` | mTLS server config test; completely unrelated. |
| `ZTAX-AIGOV-REQ-0019` | A3 auto-adoption MUST record AI provenance and guard decision. | `test_resilience.py::test_weight_record_sha256_wrong_length_refused` | Weight-record integrity test; nothing to do with A3 auto-adoption provenance. |
| `ZTAX-AIGOV-REQ-0020` | AI forecasts MUST be labelled non-authoritative. | `test_resilience.py::test_weight_record_is_production_safe_only_for_clean` | Weight scan safety test; does not test forecast labelling. |
| `ZTAX-AIGOV-REQ-0021` | AI-generated candidate obligations MUST not become authoritative without governed adoption. | `test_resilience.py::test_weight_record_proprietary_licence_accepted` | Licence format test; entirely unrelated. |
| `ZTAX-AIGOV-REQ-0023` | Prompt templates/system instructions MUST be versioned for material use cases. | `test_resilience.py::test_qualified_providers_returns_only_qualified` | Provider qualification filter; no connection to prompt template versioning. |
| `ZTAX-AIGOV-REQ-0024` | Agent configurations/tool policies MUST be versioned. | `test_resilience.py::test_models_for_provider_excludes_deprecated_by_default` | Model deprecation test; no connection to agent/tool-policy versioning. |
| `ZTAX-AIGOV-REQ-0025` | AI releases MUST have an AIReleaseManifest. | `test_resilience.py::test_registry_provider_count` | Provider count assertion; completely unrelated to AIReleaseManifest. |
| `ZTAX-AIGOV-REQ-0034` | Evaluation MUST include material jurisdiction/language/product slices where applicable. | `test_evaluation_quality.py::test_metric_engine_returns_metric_report` | Tests that the engine produces a report object; says nothing about slice coverage. |
| `ZTAX-AIGOV-REQ-0035` | Evaluation MUST include ambiguous/conflicting/unsupported cases. | `test_evaluation_quality.py::test_metric_engine_accuracy_in_valid_range` | Range-validity test; says nothing about including ambiguous cases. |
| `ZTAX-AIGOV-REQ-0037` | Evaluation MUST distinguish accuracy from coverage/abstention. | `test_evaluation_quality.py::test_metric_report_per_class_contains_per_class_metric_instances` | Tests that per-class breakdown objects exist; does not verify abstention is tracked separately from accuracy. |
| `ZTAX-AIGOV-REQ-0038` | Confidence-driven automation MUST evaluate calibration. | `test_evaluation_quality.py::test_model_comparator_returns_comparison_report` | Tests that the comparator returns an object; says nothing about calibration. |
| `ZTAX-AIGOV-REQ-0046` | Unresolvable material citation MUST cause abstention/failure of governed explanation. | `test_rag.py::test_search_before_build_raises` | Construction ordering guard; does not test that a broken citation causes abstention. |
| `ZTAX-AIGOV-REQ-0049` | RAG corpora MUST enforce privacy/data residency. | `test_privacy_source_rights.py::TestCustomerOptOut::test_opted_out_raises_customer_opt_out` | Tests customer opt-out (a privacy right); says nothing about data residency enforcement on the corpus. |
| `ZTAX-AIGOV-REQ-0052` | Retrieved untrusted content MUST NOT be treated as tool/system instruction. | `test_ai_security_controls.py::TestAdversarialScenarios::test_adv_sec_001_output_injection_via_prohibited_key` | Tests output-schema validation (prohibited keys in model output); the requirement is about prompt-injection via retrieved RAG content -- a different layer. |
| `ZTAX-AIGOV-REQ-0065` | Excessive-agency tests MUST be included for agentic use cases. | `test_tool_broker.py::test_caller_missing_a_required_scope_is_refused` | Tests scope-based access control (authorisation); not an excessive-agency scenario. |
| `ZTAX-AIGOV-REQ-0076` | Human reviewer override/rejection rates MUST be monitorable. | `test_human_review.py::test_pending_count_decreases_after_approve` | Tests queue count arithmetic; no override/rejection rate monitoring infrastructure. |
| `ZTAX-AIGOV-REQ-0080` | Production monitoring MUST detect input/behavior drift appropriate to use case. | `test_drift.py::test_check_nominal_with_healthy_signals` | Tests the nominal (no-drift) path; does not prove drift detection fires. |
| `ZTAX-AIGOV-REQ-0081` | Production monitoring MUST monitor model/provider errors and quotas. | `test_observability.py::test_model_ops_invocation_count_is_one` | Tests an invocation count signal; does not verify error or quota monitoring. |
| `ZTAX-AIGOV-REQ-0084` | AI incidents MUST identify affected AIReleaseManifest versions. | `test_production_registries.py::TestBuildManifest::test_manifest_id_auto_generated` | Tests that an ID is generated; does not test that incidents are linked to manifest versions. |
| `ZTAX-AIGOV-REQ-0107` | No T4 prohibited authority use may be enabled through feature flags or customer configuration. | `test_production_gates.py::test_g19_fail_manifest_exceeds_uc_ceiling` | Tests that a manifest exceeds the UseCase authority ceiling (Gate G19); REQ-0107 is about T4 prohibition via flags -- a different concern. |
| `ZTAX-AIGOV-REQ-0108` | No production AI release may proceed with unresolved mandatory AI gate. | `test_production_gates.py::test_failed_gates_not_empty_when_failure` | REQ-0108 is a sweeping testing-policy requirement (80+ sub-statements). A single gate-runner test (G01 ownership validation) cannot meaningfully verify it. |

---

## Non-TEST method requirements (30)

These requirements name AUDIT, DEMONSTRATION, INSPECTION, or ANALYSIS as their verification method, not a test.
They are not expected to have a `verification_ref` until an audit cycle or design
review documents them.

| Requirement | Method | Statement |
|:---|:---:|:---|
| `ZTAX-AIGOV-REQ-0001` | AUDIT | Every production AI capability MUST have a registered AIUseCase. |
| `ZTAX-AIGOV-REQ-0002` | AUDIT | Every AIUseCase MUST have a named business owner and system owner. |
| `ZTAX-AIGOV-REQ-0018` | DEMONSTRATION | Constrained automation MUST have an independent kill switch. |
| `ZTAX-AIGOV-REQ-0022` | AUDIT | AI model/provider versions MUST be registered for material production use. |
| `ZTAX-AIGOV-REQ-0026` | AUDIT | Material AI releases MUST link to evaluation results. |
| `ZTAX-AIGOV-REQ-0028` | AUDIT | Provider/model deprecation MUST be monitored. |
| `ZTAX-AIGOV-REQ-0029` | AUDIT | Material provider/model change MUST trigger tier-appropriate re-evaluation. |
| `ZTAX-AIGOV-REQ-0030` | AUDIT | Threshold changes affecting automation/abstention MUST be governed model-risk changes. |
| `ZTAX-AIGOV-REQ-0031` | AUDIT | Prompt changes with material behavioral impact MUST be governed releases. |
| `ZTAX-AIGOV-REQ-0032` | AUDIT | T3 production release MUST have independent validation. |
| `ZTAX-AIGOV-REQ-0033` | INSPECTION | Evaluation thresholds MUST be use-case specific. |
| `ZTAX-AIGOV-REQ-0040` | AUDIT | Gold datasets for tax/legal use MUST have source provenance. |
| `ZTAX-AIGOV-REQ-0041` | AUDIT | Gold labels for material tax/legal cases MUST use qualified reviewer standards. |
| `ZTAX-AIGOV-REQ-0042` | INSPECTION | Release evaluation data SHOULD be isolated from training/development data. |
| `ZTAX-AIGOV-REQ-0043` | INSPECTION | Public benchmark scores MUST NOT be sole production evidence. |
| `ZTAX-AIGOV-REQ-0067` | AUDIT | External model-provider training on customer/licensed content MUST default OFF. |
| `ZTAX-AIGOV-REQ-0072` | AUDIT | AI eval datasets with customer personal data MUST receive production-like data controls. |
| `ZTAX-AIGOV-REQ-0085` | DEMONSTRATION | Zoiko MUST be able to suspend an AIUseCase independently of deterministic tax service. |
| `ZTAX-AIGOV-REQ-0086` | AUDIT | AI incident remediation MUST update tests/evaluations where applicable. |
| `ZTAX-AIGOV-REQ-0087` | INSPECTION | AI incident severity MUST be separable from technical/security/privacy/content/legal severity. |
| `ZTAX-AIGOV-REQ-0093` | INSPECTION | Zoiko MUST NOT require private hidden chain-of-thought as audit evidence. |
| `ZTAX-AIGOV-REQ-0094` | AUDIT | Third-party model providers MUST have due-diligence ProviderProfiles. |
| `ZTAX-AIGOV-REQ-0095` | DEMONSTRATION | Critical AI use cases MUST have an approved fallback/degraded strategy. |
| `ZTAX-AIGOV-REQ-0096` | ANALYSIS | Material provider concentration risk MUST be reviewed. |
| `ZTAX-AIGOV-REQ-0098` | AUDIT | EU AI Act roles/categories MUST be assessed per use case where EU scope may apply. |
| `ZTAX-AIGOV-REQ-0102` | AUDIT | AI literacy/training MUST be role-specific for builders/reviewers/operators. |
| `ZTAX-AIGOV-REQ-0103` | AUDIT | T2/T3 AI use cases MUST receive periodic governance review. |
| `ZTAX-AIGOV-REQ-0104` | AUDIT | T2/T3 AI use cases MUST complete an AI risk/impact assessment before production. |
| `ZTAX-AIGOV-REQ-0105` | AUDIT | Material AI change MUST trigger impact assessment re-review as applicable. |
| `ZTAX-AIGOV-REQ-0106` | AUDIT | AI governance exceptions MUST be time-bounded, owned and approved. |

---

## Open questions

1. **Link standard.** What must a test assert to count as a link? Options include:
   (a) the test must prove the whole requirement statement; (b) the test must prove
   the primary enforcement behaviour. A decision is needed before the 15 held
   borderline requirements (section c-ii) can be resolved.

2. **Who approves edits to AIGOV rows?** `docs/requirements.yaml` currently has no
   stated owner for the `ZTAX-AIGOV-001` block. Who has authority to change
   `verification_method`, `statement`, or `owner` fields on AIGOV requirements?

3. **Controls in group (a).** Seven requirements have no control at all
   (REQ-0059, 0061, 0062, 0063, 0097, 0099, 0101). Each needs a new control to be
   designed and built before any test can be written. Who decides the implementation
   priority and approves the design before coding starts?

4. **Python Gateway data-class and data-purpose enforcement.** REQ-0068
   (data-purpose) and REQ-0071 (situs/CDR data class) are left null because the
   Python Gateway does not enforce them. Should the Gateway be extended to enforce
   these, or are they handled by another layer? The answer determines whether a test
   ref is ever possible here.

5. **Kill switch at the Python Gateway layer.** Several requirements (REQ-0006,
   REQ-0007, REQ-0008) describe autonomous-action prohibitions. The kill-switch
   mechanism exists (`governance.py`) but the tests that were linked to these
   requirements exercise the kill switch itself, not the prohibited behaviours.
   Should targeted tests be written that attempt the prohibited action and assert
   that the governance gate blocks them?
