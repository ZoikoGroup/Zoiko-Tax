-- Regulatory change intelligence (Engineering Doc 03 §04 change_event and
-- impact_assessment, §06 source watch, WF-05, WF-09; Sidebar Navigation
-- Architecture WF-02 specialist rule review; ZTAX-AIGOV-001 §4, §12, §14).
--
--   regulatory_change            a change as reported from one source edition:
--                                authority, jurisdiction, citation, edition and
--                                the captured document's SHA-256, and its three
--                                clocks (published, effective, discovered),
--                                never assumed equal. One row per source
--                                edition and kind (dedupe_key); a multi-effect
--                                source is several rows sharing source_key.
--   regulatory_change_event      triage, holds, the start of assessment and the
--                                final disposition, in order. Append-only.
--   impact_assessment            a deterministic assessment of one change
--                                against the active content and the tenant's
--                                registrations and obligations: the items found
--                                with their basis, the inputs digest and the
--                                assessment digest. Immutable.
--   review_recommendation(_event) a recommendation bound to one assessment's
--                                digest, its AI provenance when AI-assisted,
--                                and its recommend / independent-review history.
--
-- Nothing here references a fiscal, ledger, document, filing or content
-- release table: an assessment is a read, and an accepted recommendation is a
-- hand-off reference (handoff_ref), never a published pack or a changed tax
-- result.
--
-- intake_key and intake_hash make intake idempotent on the Idempotency-Key.

CREATE TABLE ztax.regulatory_change (
  tenant_id         uuid        NOT NULL REFERENCES ztax.tenant (tenant_id),
  change_id         uuid        NOT NULL,
  jurisdiction      text        NOT NULL,
  kind              text        NOT NULL,
  title             text        NOT NULL,
  summary           text        NULL,
  source_authority  text        NOT NULL,
  source_citation   text        NOT NULL,
  source_url        text        NULL,
  source_edition    text        NOT NULL,
  source_sha256     text        NOT NULL,
  published_on      date        NOT NULL,
  effective_from    date        NOT NULL,
  discovered_at     timestamptz NOT NULL,
  detected_by       text        NOT NULL,
  monitor_ref       text        NULL,
  service_families  text[]      NOT NULL,
  tax_types         text[]      NOT NULL,
  source_key        text        NOT NULL,
  dedupe_key        text        NOT NULL,
  intake_key        text        NOT NULL,
  intake_hash       text        NOT NULL,
  created_at        timestamptz NOT NULL,
  created_by        uuid        NOT NULL,
  PRIMARY KEY (tenant_id, change_id),
  CONSTRAINT regulatory_change_kind_known CHECK (kind IN ('NEW_LEVY', 'RATE_CHANGE', 'REPEAL_SUNSET', 'BASE_CHANGE',
    'CLASS_CHANGE', 'JURISDICTION_CHANGE', 'BACKDATED_RULE', 'RIGHTS_WITHDRAWAL', 'SOURCE_DISPUTE')),
  CONSTRAINT regulatory_change_jurisdiction_shape CHECK (jurisdiction ~ '^[A-Z]{2}(-[A-Z0-9]{1,3})?$'),
  CONSTRAINT regulatory_change_detected_by CHECK ((detected_by = 'HUMAN' AND monitor_ref IS NULL)
    OR (detected_by = 'MONITOR' AND monitor_ref IS NOT NULL)),
  -- A backdated rule takes effect before it was published; the clocks are
  -- otherwise independent.
  CONSTRAINT regulatory_change_backdated CHECK (kind <> 'BACKDATED_RULE' OR effective_from < published_on),
  CONSTRAINT regulatory_change_digests CHECK (source_sha256 ~ '^[0-9a-f]{64}$'
    AND source_key ~ '^zt1:[0-9a-f]{64}$' AND dedupe_key ~ '^zt1:[0-9a-f]{64}$' AND intake_hash ~ '^zt1:[0-9a-f]{64}$'),
  CONSTRAINT regulatory_change_scope_bounded CHECK (cardinality(service_families) <= 24 AND cardinality(tax_types) <= 20),
  CONSTRAINT regulatory_change_text_length CHECK (length(title) BETWEEN 1 AND 200
    AND (summary IS NULL OR length(summary) BETWEEN 1 AND 4000)
    AND length(source_authority) BETWEEN 1 AND 200 AND length(source_citation) BETWEEN 1 AND 200
    AND length(source_edition) BETWEEN 1 AND 200 AND (source_url IS NULL OR length(source_url) BETWEEN 9 AND 2048)
    AND (monitor_ref IS NULL OR length(monitor_ref) BETWEEN 1 AND 200) AND length(intake_key) BETWEEN 1 AND 255)
);
CREATE UNIQUE INDEX regulatory_change_intake_once ON ztax.regulatory_change (tenant_id, intake_key);
CREATE UNIQUE INDEX regulatory_change_dedupe_once ON ztax.regulatory_change (tenant_id, dedupe_key);
CREATE INDEX regulatory_change_source ON ztax.regulatory_change (tenant_id, source_key);
CREATE INDEX regulatory_change_recent ON ztax.regulatory_change (tenant_id, created_at DESC, change_id);

CREATE TABLE ztax.regulatory_change_event (
  tenant_id      uuid        NOT NULL,
  change_id      uuid        NOT NULL,
  seq            integer     NOT NULL,
  kind           text        NOT NULL,
  status_after   text        NOT NULL,
  priority       text        NULL,
  disposition    text        NULL,
  hold_reason    text        NULL,
  reason         text        NULL,
  assessment_id  uuid        NULL,
  recorded_at    timestamptz NOT NULL,
  recorded_by    uuid        NOT NULL,
  PRIMARY KEY (tenant_id, change_id, seq),
  FOREIGN KEY (tenant_id, change_id) REFERENCES ztax.regulatory_change (tenant_id, change_id),
  CONSTRAINT regulatory_change_event_kind_known CHECK (kind IN ('RECORDED', 'TRIAGED', 'ASSESSMENT_STARTED', 'HELD',
    'RESUMED', 'CLOSED', 'NO_IMPACT')),
  CONSTRAINT regulatory_change_event_recorded_first CHECK ((kind = 'RECORDED') = (seq = 1)),
  CONSTRAINT regulatory_change_event_status_known CHECK (status_after IN ('NEW', 'TRIAGED', 'IN_IMPACT_ASSESSMENT',
    'ON_HOLD', 'CLOSED', 'NO_IMPACT')),
  CONSTRAINT regulatory_change_event_priority CHECK ((kind = 'TRIAGED') = (priority IS NOT NULL)
    AND (priority IS NULL OR priority IN ('URGENT', 'MATERIAL', 'ORDINARY'))),
  CONSTRAINT regulatory_change_event_disposition CHECK ((kind IN ('CLOSED', 'NO_IMPACT')) = (disposition IS NOT NULL)
    AND (kind <> 'CLOSED' OR disposition IN ('CHANGE_NEEDED', 'MONITOR'))
    AND (kind <> 'NO_IMPACT' OR disposition = 'NOT_APPLICABLE')),
  CONSTRAINT regulatory_change_event_hold CHECK ((kind = 'HELD') = (hold_reason IS NOT NULL)
    AND (hold_reason IS NULL OR hold_reason IN ('SOURCE_DISPUTED', 'COUNSEL_QUESTION', 'RIGHTS_HOLD', 'MISSING_FACTS',
      'MONITOR_FAILURE'))),
  -- IN_IMPACT_ASSESSMENT is reached only by recording an assessment.
  CONSTRAINT regulatory_change_event_assessment CHECK ((kind = 'ASSESSMENT_STARTED') = (assessment_id IS NOT NULL)),
  CONSTRAINT regulatory_change_event_rationale CHECK (kind IN ('RECORDED', 'ASSESSMENT_STARTED') OR reason IS NOT NULL),
  CONSTRAINT regulatory_change_event_text_length CHECK (reason IS NULL OR length(reason) BETWEEN 1 AND 2000)
);

CREATE TABLE ztax.impact_assessment (
  tenant_id           uuid        NOT NULL,
  assessment_id       uuid        NOT NULL,
  change_id           uuid        NOT NULL,
  bundle_id           text        NOT NULL,
  bundle_digest       text        NOT NULL,
  as_of               date        NOT NULL,
  severity            text        NOT NULL,
  retroactive         boolean     NOT NULL,
  items               jsonb       NOT NULL,
  inputs_digest       text        NOT NULL,
  assessment_digest   text        NOT NULL,
  components_read     integer     NOT NULL,
  registrations_read  integer     NOT NULL,
  obligations_read    integer     NOT NULL,
  created_at          timestamptz NOT NULL,
  created_by          uuid        NOT NULL,
  PRIMARY KEY (tenant_id, assessment_id),
  FOREIGN KEY (tenant_id, change_id) REFERENCES ztax.regulatory_change (tenant_id, change_id),
  CONSTRAINT impact_assessment_severity_known CHECK (severity IN ('LOW', 'MEDIUM', 'HIGH', 'CRITICAL')),
  CONSTRAINT impact_assessment_items_shape CHECK (jsonb_typeof(items) = 'array' AND jsonb_array_length(items) >= 1),
  CONSTRAINT impact_assessment_digests CHECK (inputs_digest ~ '^zt1:[0-9a-f]{64}$'
    AND assessment_digest ~ '^zt1:[0-9a-f]{64}$'),
  CONSTRAINT impact_assessment_counts CHECK (components_read >= 0 AND registrations_read >= 0 AND obligations_read >= 0),
  CONSTRAINT impact_assessment_text_length CHECK (length(bundle_id) BETWEEN 1 AND 200 AND length(bundle_digest) BETWEEN 1 AND 200)
);
CREATE INDEX impact_assessment_change ON ztax.impact_assessment (tenant_id, change_id, created_at DESC, assessment_id DESC);
CREATE INDEX impact_assessment_recent ON ztax.impact_assessment (tenant_id, created_at DESC, assessment_id DESC);

CREATE TABLE ztax.review_recommendation (
  tenant_id             uuid        NOT NULL,
  recommendation_id     uuid        NOT NULL,
  change_id             uuid        NOT NULL,
  assessment_id         uuid        NOT NULL,
  assessment_digest     text        NOT NULL,
  origin                text        NOT NULL,
  ai_use_case           text        NULL,
  ai_model_profile      text        NULL,
  ai_provider_profile   text        NULL,
  ai_prompt_profile     text        NULL,
  ai_train_version      text        NULL,
  ai_authority_outcome  text        NULL,
  ai_invocation_id      text        NULL,
  ai_draft              text        NULL,
  intake_key            text        NOT NULL,
  intake_hash           text        NOT NULL,
  created_at            timestamptz NOT NULL,
  created_by            uuid        NOT NULL,
  PRIMARY KEY (tenant_id, recommendation_id),
  FOREIGN KEY (tenant_id, change_id) REFERENCES ztax.regulatory_change (tenant_id, change_id),
  FOREIGN KEY (tenant_id, assessment_id) REFERENCES ztax.impact_assessment (tenant_id, assessment_id),
  -- An AI-assisted draft records its whole provenance, at authority A1
  -- PROPOSE; a human one records none (AIGOV-001 §4, §14).
  CONSTRAINT review_recommendation_origin CHECK ((origin = 'HUMAN' AND ai_use_case IS NULL AND ai_model_profile IS NULL
      AND ai_provider_profile IS NULL AND ai_prompt_profile IS NULL AND ai_train_version IS NULL
      AND ai_authority_outcome IS NULL AND ai_invocation_id IS NULL AND ai_draft IS NULL)
    OR (origin = 'AI_ASSISTED' AND ai_use_case IS NOT NULL AND ai_model_profile IS NOT NULL
      AND ai_provider_profile IS NOT NULL AND ai_prompt_profile IS NOT NULL AND ai_train_version IS NOT NULL
      AND ai_authority_outcome = 'A1' AND ai_draft IS NOT NULL)),
  CONSTRAINT review_recommendation_digests CHECK (assessment_digest ~ '^zt1:[0-9a-f]{64}$' AND intake_hash ~ '^zt1:[0-9a-f]{64}$'),
  CONSTRAINT review_recommendation_text_length CHECK (length(intake_key) BETWEEN 1 AND 255
    AND (ai_draft IS NULL OR length(ai_draft) BETWEEN 1 AND 4000))
);
CREATE UNIQUE INDEX review_recommendation_intake_once ON ztax.review_recommendation (tenant_id, intake_key);
CREATE INDEX review_recommendation_change ON ztax.review_recommendation (tenant_id, change_id, created_at DESC);
CREATE INDEX review_recommendation_recent ON ztax.review_recommendation (tenant_id, created_at DESC, recommendation_id DESC);

CREATE TABLE ztax.review_recommendation_event (
  tenant_id          uuid        NOT NULL,
  recommendation_id  uuid        NOT NULL,
  seq                integer     NOT NULL,
  kind               text        NOT NULL,
  status_after       text        NOT NULL,
  action             text        NULL,
  rationale          text        NULL,
  targets            jsonb       NULL,
  reason             text        NULL,
  handoff_ref        text        NULL,
  recorded_at        timestamptz NOT NULL,
  recorded_by        uuid        NOT NULL,
  PRIMARY KEY (tenant_id, recommendation_id, seq),
  FOREIGN KEY (tenant_id, recommendation_id) REFERENCES ztax.review_recommendation (tenant_id, recommendation_id),
  CONSTRAINT review_recommendation_event_kind_known CHECK (kind IN ('DRAFTED', 'REVISED', 'RECOMMENDED',
    'CHANGES_REQUESTED', 'ACCEPTED', 'REJECTED', 'WITHDRAWN')),
  CONSTRAINT review_recommendation_event_drafted_first CHECK ((kind = 'DRAFTED') = (seq = 1)),
  CONSTRAINT review_recommendation_event_status_known CHECK (status_after IN ('DRAFT', 'RECOMMENDED',
    'CHANGES_REQUESTED', 'ACCEPTED', 'REJECTED', 'WITHDRAWN')),
  CONSTRAINT review_recommendation_event_content CHECK ((kind IN ('DRAFTED', 'REVISED')) = (action IS NOT NULL)
    AND ((action IS NULL) = (rationale IS NULL)) AND ((action IS NULL) = (targets IS NULL))
    AND (action IS NULL OR action IN ('UPDATE_RULE', 'ADD_RULE', 'RETIRE_RULE', 'NOTIFY_CUSTOMERS',
      'RETROSPECTIVE_REASSESSMENT', 'MONITOR', 'NO_ACTION'))
    AND (targets IS NULL OR jsonb_typeof(targets) = 'array')),
  -- ACCEPTED is reached only with a hand-off reference, and only ACCEPTED
  -- carries one.
  CONSTRAINT review_recommendation_event_handoff CHECK ((kind = 'ACCEPTED') = (handoff_ref IS NOT NULL)
    AND (status_after <> 'ACCEPTED' OR kind = 'ACCEPTED')),
  CONSTRAINT review_recommendation_event_reason_given CHECK (kind IN ('DRAFTED', 'REVISED') OR reason IS NOT NULL),
  CONSTRAINT review_recommendation_event_text_length CHECK ((reason IS NULL OR length(reason) BETWEEN 1 AND 2000)
    AND (rationale IS NULL OR length(rationale) BETWEEN 1 AND 2000)
    AND (handoff_ref IS NULL OR length(handoff_ref) BETWEEN 1 AND 200))
);

REVOKE UPDATE, DELETE, TRUNCATE ON ztax.regulatory_change FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.regulatory_change_event FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.impact_assessment FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.review_recommendation FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.review_recommendation_event FROM ztax_app;

ALTER TABLE ztax.regulatory_change ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.regulatory_change
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.regulatory_change_event ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.regulatory_change_event
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.impact_assessment ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.impact_assessment
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.review_recommendation ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.review_recommendation
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.review_recommendation_event ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.review_recommendation_event
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

COMMENT ON TABLE ztax.regulatory_change IS
  'A regulatory change as reported from one source edition: authority, jurisdiction, citation, edition, document digest and its published, effective and discovered clocks. Immutable; its status is its event history.';
COMMENT ON TABLE ztax.regulatory_change_event IS
  'Triage, holds, start of impact assessment and final disposition of a regulatory change, in order. Append-only.';
COMMENT ON TABLE ztax.impact_assessment IS
  'A deterministic impact assessment of a change against the active content and the tenant''s registrations and obligations, with each item''s basis and the inputs and assessment digests. Immutable; writes nothing else.';
COMMENT ON TABLE ztax.review_recommendation IS
  'A review recommendation bound to one impact assessment''s digest, with AI provenance when AI-assisted. Immutable; its status is its event history. Acceptance is a hand-off reference only.';
COMMENT ON TABLE ztax.review_recommendation_event IS
  'Drafts, revisions, specialist recommendation and independent review of a recommendation, in order. Append-only.';
