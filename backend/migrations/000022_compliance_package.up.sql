-- Compliance packages (ZTAX-COMP-001 §4, §5, §14; W4 lane M).
--
-- One obligation's executable filing: how it is worked, by whom, and what
-- the authority answered. The package row is immutable and references the
-- obligation decision it executes; it never restates or alters the
-- obligation's period, authority or responsible party (ZTAX-COMP-REQ-0001 to
-- -0003). Its state is its step history: every preparation version, review,
-- approval, submission attempt and acknowledgement is a new row, so a
-- filing's lineage is never rewritten (ZTAX-COMP-REQ-0129).
--
-- responsibility_id is the ResponsibilityDecision the obligation names. It is
-- nullable because obligations assessed from commits do not yet record one;
-- a package carries it whenever the obligation does (ZTAX-COMP-REQ-0002).
--
-- One package per obligation: instantiation is idempotent for the
-- obligation's business key, which is its legal period (ZTAX-COMP-REQ-0004).
--
-- The statutory due date is the obligation's and is never overwritten; the
-- internal targets are separate columns and may only be earlier
-- (ZTAX-COMP-REQ-0008, -0011).

CREATE TABLE ztax.compliance_package (
  tenant_id          uuid        NOT NULL REFERENCES ztax.tenant (tenant_id),
  package_id         uuid        NOT NULL,
  obligation_id      uuid        NOT NULL,
  obligation_key     text        NOT NULL,
  responsibility_id  uuid        NULL,
  legal_entity_id    uuid        NOT NULL,
  obligation_type    text        NOT NULL,
  jurisdiction_id    text        NOT NULL,
  period_start       date        NOT NULL,
  period_end         date        NOT NULL,
  operating_mode     text        NOT NULL,
  channel            text        NOT NULL,
  filing_country     text        NOT NULL,
  filing_authority   text        NOT NULL,
  statutory_due      timestamptz NOT NULL,
  legal_timezone     text        NOT NULL,
  prepare_due        timestamptz NULL,
  review_due         timestamptz NULL,
  submit_target      timestamptz NULL,
  created_at         timestamptz NOT NULL,
  created_by         uuid        NOT NULL,
  PRIMARY KEY (tenant_id, package_id),
  FOREIGN KEY (tenant_id, obligation_id) REFERENCES ztax.obligation (tenant_id, obligation_id),
  FOREIGN KEY (tenant_id, legal_entity_id) REFERENCES ztax.legal_entity (tenant_id, legal_entity_id),
  CONSTRAINT compliance_package_mode_known CHECK (operating_mode IN ('SOFTWARE_ONLY', 'ASSISTED', 'MANAGED_PREP',
    'MANAGED_FILE', 'FULL_MANAGED_COMPLIANCE', 'PARTNER_LED')),
  CONSTRAINT compliance_package_channel_known CHECK (channel IN ('DIRECT_API', 'EFILE_NETWORK', 'FILE_TRANSFER',
    'WEB_PORTAL_RPA', 'MANUAL_PORTAL', 'LOCAL_PARTNER', 'PAPER')),
  CONSTRAINT compliance_package_country_shape CHECK (filing_country ~ '^[A-Z]{2}$'),
  CONSTRAINT compliance_package_period CHECK (period_end >= period_start),
  -- Internal targets are ours and only ever earlier than the authority's.
  CONSTRAINT compliance_package_targets CHECK (
    (prepare_due IS NULL OR prepare_due <= statutory_due)
    AND (review_due IS NULL OR review_due <= statutory_due)
    AND (submit_target IS NULL OR submit_target <= statutory_due))
);
CREATE UNIQUE INDEX compliance_package_one_per_obligation ON ztax.compliance_package (tenant_id, obligation_key);
CREATE INDEX compliance_package_due ON ztax.compliance_package (tenant_id, statutory_due);

CREATE TABLE ztax.compliance_step (
  tenant_id          uuid        NOT NULL,
  package_id         uuid        NOT NULL,
  seq                integer     NOT NULL,
  kind               text        NOT NULL,
  status_after       text        NOT NULL,
  version            integer     NULL,
  payload_hash       text        NULL,
  control_total      numeric     NULL,
  currency           text        NULL,
  review_decision    text        NULL,
  submission_id      uuid        NULL,
  channel            text        NULL,
  acknowledgement    text        NULL,
  resolution         text        NULL,
  authority_ref      text        NULL,
  evidence           text        NULL,
  note               text        NULL,
  recorded_at        timestamptz NOT NULL,
  recorded_by        uuid        NOT NULL,
  PRIMARY KEY (tenant_id, package_id, seq),
  FOREIGN KEY (tenant_id, package_id) REFERENCES ztax.compliance_package (tenant_id, package_id),
  CONSTRAINT compliance_step_kind_known CHECK (kind IN ('CREATED', 'PREPARED', 'REVIEWED', 'APPROVED',
    'SUBMISSION_STARTED', 'ACKNOWLEDGED', 'UNCERTAIN_RESOLVED', 'CANCELLED')),
  CONSTRAINT compliance_step_created_first CHECK ((kind = 'CREATED') = (seq = 1)),
  CONSTRAINT compliance_step_status_known CHECK (status_after IN ('READY', 'IN_REVIEW', 'APPROVED', 'TRANSMITTING',
    'UNCERTAIN_SUBMISSION', 'FILED', 'ACCEPTED', 'REJECTED', 'CANCELLED')),
  -- Every act on a prepared version names the version and its hash.
  CONSTRAINT compliance_step_version_named CHECK (kind IN ('CREATED', 'CANCELLED')
    OR (version IS NOT NULL AND version >= 1 AND payload_hash IS NOT NULL)),
  CONSTRAINT compliance_step_review_named CHECK ((kind = 'REVIEWED') = (review_decision IS NOT NULL)),
  CONSTRAINT compliance_step_review_known CHECK (review_decision IS NULL
    OR review_decision IN ('ACCEPT', 'REJECT', 'RETURN_FOR_CHANGE', 'ESCALATE')),
  CONSTRAINT compliance_step_attempt_named CHECK (kind NOT IN ('SUBMISSION_STARTED', 'ACKNOWLEDGED', 'UNCERTAIN_RESOLVED')
    OR (submission_id IS NOT NULL AND channel IS NOT NULL)),
  CONSTRAINT compliance_step_ack_named CHECK ((kind = 'ACKNOWLEDGED') = (acknowledgement IS NOT NULL)),
  CONSTRAINT compliance_step_ack_known CHECK (acknowledgement IS NULL OR acknowledgement IN ('RECEIVED', 'ACCEPTED',
    'ACCEPTED_WITH_WARNINGS', 'REJECTED_VALIDATION', 'REJECTED_AUTHORIZATION', 'REJECTED_DUPLICATE',
    'PENDING_AUTHORITY', 'UNKNOWN')),
  CONSTRAINT compliance_step_resolution_named CHECK ((kind = 'UNCERTAIN_RESOLVED') = (resolution IS NOT NULL)),
  CONSTRAINT compliance_step_resolution_known CHECK (resolution IS NULL OR resolution IN ('CONFIRMED_FILED',
    'CONFIRMED_NOT_FILED', 'DUPLICATE_DETECTED', 'MANUAL_INVESTIGATION')),
  -- The authority's answer is kept as received (ZTAX-COMP-REQ-0063).
  CONSTRAINT compliance_step_evidence_kept CHECK (kind NOT IN ('ACKNOWLEDGED', 'UNCERTAIN_RESOLVED') OR evidence IS NOT NULL),
  CONSTRAINT compliance_step_text_length CHECK ((evidence IS NULL OR length(evidence) BETWEEN 1 AND 20000)
    AND (note IS NULL OR length(note) BETWEEN 1 AND 2000)
    AND (authority_ref IS NULL OR length(authority_ref) BETWEEN 1 AND 200))
);
-- A submission attempt is started once.
CREATE UNIQUE INDEX compliance_step_attempt_once ON ztax.compliance_step (tenant_id, submission_id)
  WHERE kind = 'SUBMISSION_STARTED';

REVOKE UPDATE, DELETE, TRUNCATE ON ztax.compliance_package FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.compliance_step FROM ztax_app;

ALTER TABLE ztax.compliance_package ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.compliance_package
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.compliance_step ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.compliance_step
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

COMMENT ON TABLE ztax.compliance_package IS
  'One obligation''s compliance package. Immutable; its state is its step history.';
COMMENT ON TABLE ztax.compliance_step IS
  'Preparations, reviews, approvals, submission attempts and acknowledgements, in order. Append-only.';
