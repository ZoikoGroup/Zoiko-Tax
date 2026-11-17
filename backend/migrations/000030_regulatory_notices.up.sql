-- Regulatory notices and appeals (Engineering Doc 18 W17, G-052; NAV-013).
--
-- One row per notice, assessment, audit, appeal or information request an
-- authority sent: who sent it, to which legal entity, under what reference,
-- when it arrived, and what it states. The row is immutable. Its state is its
-- event history: every authenticity finding, legal escalation, owner, attached
-- document, deadline confirmation or extension, response package, four-eye
-- approval, submission, outcome and appeal is a new row.
--
-- The statutory deadline is a ledger in the event rows. The RECEIVED event
-- holds the deadline as registered and is never rewritten; a date changes
-- only through a DEADLINE_RECORDED extension that carries evidence, and an
-- appeal opens its own next deadline (T18-17, SUP-16). The internal SLA is a
-- separate column and never a statutory date.
--
-- Links to returns, receipts, payments, ledger entries, rule versions and
-- correspondence are identifiers as given. Nothing here references or
-- changes a fiscal table.
--
-- legal_entity_id is nullable: a notice whose recipient is absent or
-- ambiguous is registered and goes to legal review (SUP-08).

CREATE TABLE ztax.regulatory_notice (
  tenant_id           uuid        NOT NULL REFERENCES ztax.tenant (tenant_id),
  notice_id           uuid        NOT NULL,
  notice_type         text        NOT NULL,
  authority           text        NOT NULL,
  jurisdiction        text        NOT NULL,
  legal_entity_id     uuid        NULL,
  entity_ambiguous    boolean     NOT NULL,
  authority_reference text        NOT NULL,
  received_at         timestamptz NOT NULL,
  period_start        date        NULL,
  period_end          date        NULL,
  amount              numeric     NULL,
  currency            text        NULL,
  linked_case_id      text        NULL,
  links               jsonb       NOT NULL,
  created_at          timestamptz NOT NULL,
  created_by          uuid        NOT NULL,
  PRIMARY KEY (tenant_id, notice_id),
  FOREIGN KEY (tenant_id, legal_entity_id) REFERENCES ztax.legal_entity (tenant_id, legal_entity_id),
  CONSTRAINT regulatory_notice_type_known CHECK (
    notice_type IN ('NOTICE', 'ASSESSMENT', 'AUDIT', 'APPEAL', 'INFORMATION_REQUEST')),
  CONSTRAINT regulatory_notice_jurisdiction_shape CHECK (jurisdiction ~ '^[A-Z]{2}(-[A-Z0-9]{1,3})?$'),
  CONSTRAINT regulatory_notice_period CHECK (
    (period_start IS NULL) = (period_end IS NULL) AND (period_end IS NULL OR period_end >= period_start)),
  CONSTRAINT regulatory_notice_amount_currency CHECK ((amount IS NULL) = (currency IS NULL)),
  CONSTRAINT regulatory_notice_links_array CHECK (jsonb_typeof(links) = 'array'),
  CONSTRAINT regulatory_notice_text_length CHECK (length(authority) BETWEEN 1 AND 128
    AND length(authority_reference) BETWEEN 1 AND 200
    AND (linked_case_id IS NULL OR length(linked_case_id) BETWEEN 1 AND 200))
);
CREATE INDEX regulatory_notice_received ON ztax.regulatory_notice (tenant_id, received_at DESC, notice_id);
CREATE INDEX regulatory_notice_reference ON ztax.regulatory_notice (tenant_id, authority, authority_reference);

CREATE TABLE ztax.regulatory_notice_event (
  tenant_id            uuid        NOT NULL,
  notice_id            uuid        NOT NULL,
  seq                  integer     NOT NULL,
  kind                 text        NOT NULL,
  status_after         text        NOT NULL,
  note                 text        NULL,
  evidence             text        NULL,
  authority_ref        text        NULL,
  deadline_kind        text        NULL,
  due_date             date        NULL,
  due_timezone         text        NULL,
  deadline_source      text        NULL,
  deadline_verified    boolean     NULL,
  authenticity         text        NULL,
  escalation_reason    text        NULL,
  legal_entity_id      uuid        NULL,
  owner_id             uuid        NULL,
  sla_due              timestamptz NULL,
  attachment_sha256    text        NULL,
  attachment_name      text        NULL,
  attachment_media     text        NULL,
  response_version     integer     NULL,
  response_sha256      text        NULL,
  outcome              text        NULL,
  outcome_amount       numeric     NULL,
  outcome_currency     text        NULL,
  settled              boolean     NULL,
  settlement           text        NULL,
  hearing_date         date        NULL,
  recorded_at          timestamptz NOT NULL,
  recorded_by          uuid        NOT NULL,
  PRIMARY KEY (tenant_id, notice_id, seq),
  FOREIGN KEY (tenant_id, notice_id) REFERENCES ztax.regulatory_notice (tenant_id, notice_id),
  CONSTRAINT regulatory_notice_event_kind_known CHECK (kind IN ('RECEIVED', 'TRANSITIONED',
    'AUTHENTICITY_RECORDED', 'LEGAL_ESCALATED', 'ENTITY_RESOLVED', 'OWNER_ASSIGNED', 'ATTACHMENT_RECORDED',
    'DEADLINE_RECORDED', 'RESPONSE_PREPARED', 'RESPONSE_APPROVED', 'SUBMITTED', 'OUTCOME_RECORDED',
    'APPEAL_LODGED', 'CLOSED')),
  CONSTRAINT regulatory_notice_event_received_first CHECK ((kind = 'RECEIVED') = (seq = 1)),
  CONSTRAINT regulatory_notice_event_status_known CHECK (status_after IN ('RECEIVED', 'AUTHENTICITY_REVIEW',
    'LEGAL_REVIEW', 'RESPONSE_APPROVED', 'SUBMITTED', 'OUTCOME_RECORDED', 'APPEAL', 'CLOSED')),
  -- The deadline ledger: the intake's original, then confirmations,
  -- extensions and appeals. An extension always carries its evidence.
  CONSTRAINT regulatory_notice_event_deadline_named CHECK (
    (kind IN ('RECEIVED', 'DEADLINE_RECORDED', 'APPEAL_LODGED')) = (deadline_kind IS NOT NULL)),
  CONSTRAINT regulatory_notice_event_deadline_known CHECK (deadline_kind IS NULL
    OR (deadline_kind IN ('ORIGINAL', 'CONFIRMATION', 'EXTENSION', 'APPEAL')
        AND due_timezone IS NOT NULL AND deadline_source IS NOT NULL AND deadline_verified IS NOT NULL
        AND deadline_source IN ('STATED_ON_NOTICE', 'STATUTE', 'AUTHORITY_CONFIRMATION', 'AUTHORITY_PORTAL',
          'ESTIMATED', 'UNKNOWN'))),
  CONSTRAINT regulatory_notice_event_extension_evidenced CHECK (deadline_kind IS DISTINCT FROM 'EXTENSION'
    OR (due_date IS NOT NULL AND deadline_verified AND evidence IS NOT NULL
        AND (authority_ref IS NOT NULL OR attachment_sha256 IS NOT NULL))),
  CONSTRAINT regulatory_notice_event_authenticity_known CHECK (authenticity IS NULL
    OR authenticity IN ('UNKNOWN', 'VERIFIED', 'UNVERIFIABLE', 'NOT_AUTHENTIC')),
  CONSTRAINT regulatory_notice_event_escalation_known CHECK ((kind = 'LEGAL_ESCALATED') = (escalation_reason IS NOT NULL)
    AND (escalation_reason IS NULL OR escalation_reason IN ('DEADLINE_UNVERIFIED', 'AUTHENTICITY_UNRESOLVED',
      'ENTITY_UNRESOLVED', 'LEGAL_JUDGEMENT'))),
  CONSTRAINT regulatory_notice_event_attachment_shape CHECK ((attachment_sha256 IS NULL
      OR attachment_sha256 ~ '^[0-9a-f]{64}$')
    AND (response_sha256 IS NULL OR response_sha256 ~ '^[0-9a-f]{64}$')
    AND ((attachment_sha256 IS NULL) = (attachment_name IS NULL))
    AND ((attachment_sha256 IS NULL) = (attachment_media IS NULL))),
  -- A submission and an outcome carry the authority's receipt.
  CONSTRAINT regulatory_notice_event_receipt_kept CHECK (kind NOT IN ('SUBMITTED', 'OUTCOME_RECORDED')
    OR authority_ref IS NOT NULL),
  CONSTRAINT regulatory_notice_event_outcome_known CHECK ((kind = 'OUTCOME_RECORDED') = (outcome IS NOT NULL)
    AND (outcome IS NULL OR outcome IN ('ACCEPTED', 'ADJUSTED', 'REJECTED', 'WITHDRAWN'))),
  CONSTRAINT regulatory_notice_event_outcome_amount CHECK ((outcome_amount IS NULL) = (outcome_currency IS NULL)),
  -- Settled only on confirmation or a settled payment (T18-20).
  CONSTRAINT regulatory_notice_event_settlement CHECK ((settlement IS NULL
      OR settlement IN ('AUTHORITY_CONFIRMED', 'PAYMENT_SETTLED', 'PAYMENT_INSTRUCTION_PENDING'))
    AND (settled IS NOT TRUE OR settlement IN ('AUTHORITY_CONFIRMED', 'PAYMENT_SETTLED'))),
  CONSTRAINT regulatory_notice_event_text_length CHECK ((evidence IS NULL OR length(evidence) BETWEEN 1 AND 20000)
    AND (note IS NULL OR length(note) BETWEEN 1 AND 2000)
    AND (authority_ref IS NULL OR length(authority_ref) BETWEEN 1 AND 200)
    AND (attachment_name IS NULL OR length(attachment_name) BETWEEN 1 AND 255))
);
CREATE INDEX regulatory_notice_event_attachment ON ztax.regulatory_notice_event (tenant_id, attachment_sha256)
  WHERE attachment_sha256 IS NOT NULL;

REVOKE UPDATE, DELETE, TRUNCATE ON ztax.regulatory_notice FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.regulatory_notice_event FROM ztax_app;

ALTER TABLE ztax.regulatory_notice ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.regulatory_notice
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.regulatory_notice_event ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.regulatory_notice_event
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

COMMENT ON TABLE ztax.regulatory_notice IS
  'One regulatory notice, assessment, audit, appeal or information request as received. Immutable; its state is its event history.';
COMMENT ON TABLE ztax.regulatory_notice_event IS
  'Authenticity, escalations, owners, attachment evidence, the statutory deadline ledger, response approvals, submissions, outcomes and appeals, in order. Append-only.';
