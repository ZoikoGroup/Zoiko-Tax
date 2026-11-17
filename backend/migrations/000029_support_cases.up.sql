-- Customer support cases (Engineering Doc 18 §5, W16 "Customer Support
-- Triage Queue", G-052).
--
-- A case routes and prioritises a customer's request; it carries no tax-rule
-- or financial authority. The decision, document, obligation and remittance
-- it names are read-only references — plain identifiers with no foreign key,
-- so nothing here can reach into a fiscal record (SUP-03, SUP-04, SUP-11).
--
-- A case is a header written once and an append-only history of events. Its
-- current classification, assignee, evidence and status are the fold of the
-- events; a revision is a new event, never an UPDATE.
--
-- Two clocks are kept apart: the support SLA (sla_due_at, from severity) and
-- any statutory deadline (statutory_due_at). An event may bring the SLA
-- earlier when severity is raised; nothing moves either later or restarts
-- it (SUP-06, SUP-07).

CREATE TABLE ztax.support_case (
  tenant_id         uuid        NOT NULL REFERENCES ztax.tenant (tenant_id),
  case_id           uuid        NOT NULL,
  case_type         text        NOT NULL,
  severity          text        NOT NULL,
  subject           text        NOT NULL,
  description       text        NOT NULL,
  product           text        NULL,
  legal_entity_id   uuid        NULL,
  fiscal_period     text        NULL,
  contract_ref      text        NULL,
  seller_ref        text        NULL,
  decision_id       uuid        NULL,
  document_id       uuid        NULL,
  obligation_id     uuid        NULL,
  remittance_id     uuid        NULL,
  statutory_due_at  timestamptz NULL,
  sla_due_at        timestamptz NOT NULL,
  assignee          uuid        NULL,
  evidence          text[]      NOT NULL,
  opened_at         timestamptz NOT NULL,
  opened_by         uuid        NOT NULL,
  PRIMARY KEY (tenant_id, case_id),
  FOREIGN KEY (tenant_id, legal_entity_id) REFERENCES ztax.legal_entity (tenant_id, legal_entity_id),
  CONSTRAINT support_case_type_known CHECK (case_type IN ('COVERAGE_ENTITLEMENT', 'INVOICE_EXPLANATION',
    'TAX_SPECIALIST', 'FILING', 'REMITTANCE', 'SECURITY', 'LEGAL')),
  CONSTRAINT support_case_severity_known CHECK (severity IN ('S1', 'S2', 'S3', 'S4')),
  CONSTRAINT support_case_period_shape CHECK (fiscal_period IS NULL OR fiscal_period ~ '^[0-9]{4}-(0[1-9]|1[0-2])$'),
  CONSTRAINT support_case_sla_after_opening CHECK (sla_due_at > opened_at),
  CONSTRAINT support_case_text_length CHECK (length(subject) BETWEEN 1 AND 200
    AND length(description) BETWEEN 1 AND 5000
    AND (product IS NULL OR length(product) BETWEEN 1 AND 200)
    AND (contract_ref IS NULL OR length(contract_ref) BETWEEN 1 AND 200)
    AND (seller_ref IS NULL OR length(seller_ref) BETWEEN 1 AND 200)),
  CONSTRAINT support_case_evidence_bounded CHECK (cardinality(evidence) <= 50)
);
-- The queue reads the most recently opened cases of one tenant.
CREATE INDEX support_case_recent ON ztax.support_case (tenant_id, opened_at DESC, case_id DESC);

CREATE TABLE ztax.support_case_event (
  tenant_id         uuid        NOT NULL,
  case_id           uuid        NOT NULL,
  seq               integer     NOT NULL,
  action            text        NOT NULL,
  status_after      text        NOT NULL,
  note              text        NULL,
  case_type         text        NULL,
  severity          text        NULL,
  legal_entity_id   uuid        NULL,
  fiscal_period     text        NULL,
  contract_ref      text        NULL,
  seller_ref        text        NULL,
  statutory_due_at  timestamptz NULL,
  sla_due_at        timestamptz NULL,
  assignee          uuid        NULL,
  escalate_to       text        NULL,
  evidence          text[]      NULL,
  missing_facts     text[]      NULL,
  reopens_seq       integer     NULL,
  recorded_at       timestamptz NOT NULL,
  recorded_by       uuid        NOT NULL,
  PRIMARY KEY (tenant_id, case_id, seq),
  FOREIGN KEY (tenant_id, case_id) REFERENCES ztax.support_case (tenant_id, case_id),
  FOREIGN KEY (tenant_id, legal_entity_id) REFERENCES ztax.legal_entity (tenant_id, legal_entity_id),
  CONSTRAINT support_case_event_action_known CHECK (action IN ('OPENED', 'VERIFY', 'TRIAGE', 'ASSIGN',
    'REQUEST_EVIDENCE', 'ADD_EVIDENCE', 'START_REVIEW', 'ESCALATE', 'RESPOND', 'RESOLVE', 'SIGN_OFF', 'CLOSE',
    'REOPEN', 'NOTE', 'REQUEST_LEGAL_HOLD')),
  CONSTRAINT support_case_event_opened_first CHECK ((action = 'OPENED') = (seq = 1)),
  CONSTRAINT support_case_event_status_known CHECK (status_after IN ('NEW', 'VERIFIED', 'TRIAGE_INCOMPLETE',
    'TRIAGED', 'IN_REVIEW', 'RESPONDED', 'RESOLVED', 'CLOSED')),
  CONSTRAINT support_case_event_type_known CHECK (case_type IS NULL OR case_type IN ('COVERAGE_ENTITLEMENT',
    'INVOICE_EXPLANATION', 'TAX_SPECIALIST', 'FILING', 'REMITTANCE', 'SECURITY', 'LEGAL')),
  CONSTRAINT support_case_event_severity_known CHECK (severity IS NULL OR severity IN ('S1', 'S2', 'S3', 'S4')),
  CONSTRAINT support_case_event_period_shape CHECK (fiscal_period IS NULL
    OR fiscal_period ~ '^[0-9]{4}-(0[1-9]|1[0-2])$'),
  CONSTRAINT support_case_event_escalation_known CHECK (escalate_to IS NULL OR escalate_to IN ('TAX_SPECIALIST',
    'LEGAL', 'SECURITY', 'FINANCE_OPERATIONS', 'ENGINEERING')),
  CONSTRAINT support_case_event_escalation_named CHECK ((action = 'ESCALATE') = (escalate_to IS NOT NULL)),
  -- A reopening names the resolution or closure it reverses (SUP-07).
  CONSTRAINT support_case_event_reopen_linked CHECK ((action = 'REOPEN') = (reopens_seq IS NOT NULL)),
  -- A response, a sign-off, a reopening, an escalation, an evidence or
  -- legal-hold request and a note each say what was done (SUP-07, SUP-14,
  -- T18-18).
  CONSTRAINT support_case_event_note_kept CHECK (action NOT IN ('RESPOND', 'SIGN_OFF', 'REOPEN', 'ESCALATE',
    'REQUEST_EVIDENCE', 'NOTE', 'REQUEST_LEGAL_HOLD') OR note IS NOT NULL),
  CONSTRAINT support_case_event_text_length CHECK ((note IS NULL OR length(note) BETWEEN 1 AND 2000)
    AND (contract_ref IS NULL OR length(contract_ref) BETWEEN 1 AND 200)
    AND (seller_ref IS NULL OR length(seller_ref) BETWEEN 1 AND 200))
);

REVOKE UPDATE, DELETE, TRUNCATE ON ztax.support_case FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.support_case_event FROM ztax_app;

ALTER TABLE ztax.support_case ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.support_case
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.support_case_event ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.support_case_event
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

COMMENT ON TABLE ztax.support_case IS
  'One customer support case as opened. Never updated; its revisions are support_case_event rows. Not a fiscal record.';
COMMENT ON TABLE ztax.support_case_event IS
  'Every act on a support case, in order, with its actor and time. Append-only; the single audited history of the case.';
