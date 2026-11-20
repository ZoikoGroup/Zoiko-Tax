-- Global e-invoicing and continuous transaction controls (Engineering Doc 21,
-- G-030): country applicability, format validation and authority
-- submissions, on top of the committed fiscal documents of 000016.
--
-- An applicability record is one cell — legal entity, country, document
-- type, buyer kind — with versioned terms: the duty (REQUIRED, OPTIONAL,
-- PROHIBITED), control model, format profile and version, authority, legal
-- source and as-of date, effective dates and eligibility conditions. A
-- version is never edited; a change is the next version. The record's status
-- is the fold of appended events, and it is ACTIVE only after the G0–G4
-- sign-offs its duty needs, on its current version, by people other than its
-- authors, with evidence attached (by SHA-256; no file is stored).
--
-- A validation report is immutable: the document's input digest, the record
-- version, the ruleset version and every rule's result. A submission seals
-- the document projection, the record version and the passing report under
-- one payload digest; its status is the fold of appended events — PREPARED,
-- DISPATCHED, then the transport and authority answers a person recorded
-- with the authority's references and the SHA-256 of the raw receipt.
--
-- Nothing here edits a fiscal document. No personal data beyond who recorded
-- each step, and the file names of evidence documents.

CREATE TABLE ztax.ctc_applicability (
  tenant_id         uuid        NOT NULL REFERENCES ztax.tenant (tenant_id),
  applicability_id  uuid        NOT NULL,
  legal_entity_id   uuid        NOT NULL,
  country           text        NOT NULL,
  document_type     text        NOT NULL,
  buyer_kind        text        NOT NULL,
  created_at        timestamptz NOT NULL,
  created_by        uuid        NOT NULL,
  PRIMARY KEY (tenant_id, applicability_id),
  FOREIGN KEY (tenant_id, legal_entity_id) REFERENCES ztax.legal_entity (tenant_id, legal_entity_id),
  CONSTRAINT ctc_applicability_country_shape CHECK (country ~ '^[A-Z]{2}$'),
  CONSTRAINT ctc_applicability_document_type_known CHECK (document_type IN ('INVOICE', 'CREDIT_NOTE', 'DEBIT_NOTE',
    'REBILL', 'ADJUSTMENT')),
  CONSTRAINT ctc_applicability_buyer_kind_known CHECK (buyer_kind IN ('B2B', 'B2G', 'B2C'))
);
CREATE INDEX ctc_applicability_by_cell ON ztax.ctc_applicability (tenant_id, legal_entity_id, country, document_type, buyer_kind);

CREATE TABLE ztax.ctc_applicability_version (
  tenant_id         uuid        NOT NULL,
  applicability_id  uuid        NOT NULL,
  version           integer     NOT NULL,
  duty              text        NOT NULL,
  model             text        NOT NULL,
  profile           text        NOT NULL,
  format_version    text        NULL,
  authority         text        NOT NULL,
  legal_source      text        NOT NULL,
  source_as_of      date        NOT NULL,
  effective_from    date        NOT NULL,
  effective_to      date        NULL,
  conditions        text        NULL,
  digest            text        NOT NULL,
  recorded_at       timestamptz NOT NULL,
  recorded_by       uuid        NOT NULL,
  PRIMARY KEY (tenant_id, applicability_id, version),
  FOREIGN KEY (tenant_id, applicability_id) REFERENCES ztax.ctc_applicability (tenant_id, applicability_id),
  CONSTRAINT ctc_applicability_version_positive CHECK (version >= 1),
  CONSTRAINT ctc_applicability_version_duty_known CHECK (duty IN ('REQUIRED', 'OPTIONAL', 'PROHIBITED')),
  CONSTRAINT ctc_applicability_version_model_known CHECK (model IN ('CLEARANCE', 'REAL_TIME_REPORTING', 'POST_AUDIT',
    'NETWORK_EXCHANGE', 'NONE')),
  CONSTRAINT ctc_applicability_version_profile_known CHECK (profile IN ('PEPPOL_BIS_3', 'FR_EN16931', 'IT_FATTURAPA',
    'PL_KSEF_FA', 'BR_NFCOM_62', 'SA_ZATCA_UBL', 'IN_GST_IRP', 'NONE')),
  -- A prohibited duty transmits nothing; a dispatching one names how.
  CONSTRAINT ctc_applicability_version_prohibited_none CHECK ((duty = 'PROHIBITED') = (model = 'NONE' AND profile = 'NONE')),
  CONSTRAINT ctc_applicability_version_dates CHECK (effective_to IS NULL OR effective_to >= effective_from),
  CONSTRAINT ctc_applicability_version_digest_shape CHECK (digest ~ '^zt1:[0-9a-f]{64}$'),
  CONSTRAINT ctc_applicability_version_text_length CHECK (length(authority) BETWEEN 1 AND 200
    AND length(legal_source) BETWEEN 1 AND 2000
    AND (format_version IS NULL OR length(format_version) BETWEEN 1 AND 200)
    AND (conditions IS NULL OR length(conditions) BETWEEN 1 AND 2000))
);

CREATE TABLE ztax.ctc_applicability_event (
  tenant_id         uuid        NOT NULL,
  applicability_id  uuid        NOT NULL,
  seq               integer     NOT NULL,
  kind              text        NOT NULL,
  status_after      text        NOT NULL,
  version           integer     NULL,
  gate              text        NULL,
  signoff_ref       text        NULL,
  reason            text        NULL,
  evidence_sha256   text        NULL,
  evidence_name     text        NULL,
  evidence_media    text        NULL,
  evidence_bytes    bigint      NULL,
  recorded_at       timestamptz NOT NULL,
  recorded_by       uuid        NOT NULL,
  PRIMARY KEY (tenant_id, applicability_id, seq),
  FOREIGN KEY (tenant_id, applicability_id) REFERENCES ztax.ctc_applicability (tenant_id, applicability_id),
  CONSTRAINT ctc_applicability_event_kind_known CHECK (kind IN ('CREATED', 'REVISED', 'EVIDENCE_ADDED', 'SUBMITTED',
    'RETURNED', 'GATE_SIGNED', 'ACTIVATED', 'SUSPENDED', 'RESUMED', 'RETIRED')),
  CONSTRAINT ctc_applicability_event_status_known CHECK (status_after IN ('DRAFT', 'IN_REVIEW', 'ACTIVE', 'SUSPENDED',
    'RETIRED')),
  CONSTRAINT ctc_applicability_event_created_first CHECK ((kind = 'CREATED') = (seq = 1)),
  -- A sign-off and an activation are on an exact version.
  CONSTRAINT ctc_applicability_event_versioned CHECK (kind NOT IN ('CREATED', 'REVISED', 'GATE_SIGNED', 'ACTIVATED')
    OR version IS NOT NULL),
  CONSTRAINT ctc_applicability_event_gate CHECK ((kind = 'GATE_SIGNED') = (gate IS NOT NULL AND signoff_ref IS NOT NULL)),
  CONSTRAINT ctc_applicability_event_gate_known CHECK (gate IS NULL OR gate IN ('G0_SCOPE_AUTHORITY',
    'G1_LEGAL_CONTENT_RIGHTS', 'G2_TECHNICAL_QA', 'G3_SECURITY_OPERATIONS', 'G4_CONTROLLED_PRODUCTION')),
  CONSTRAINT ctc_applicability_event_evidence CHECK ((kind = 'EVIDENCE_ADDED') = (evidence_sha256 IS NOT NULL
    AND evidence_name IS NOT NULL AND evidence_media IS NOT NULL AND evidence_bytes IS NOT NULL)),
  CONSTRAINT ctc_applicability_event_evidence_shape CHECK (evidence_sha256 IS NULL OR evidence_sha256 ~ '^[0-9a-f]{64}$'),
  -- A return, a suspension, a resumption and a retirement say why.
  CONSTRAINT ctc_applicability_event_explained CHECK (kind NOT IN ('RETURNED', 'SUSPENDED', 'RESUMED', 'RETIRED')
    OR reason IS NOT NULL),
  CONSTRAINT ctc_applicability_event_text_length CHECK (
    (signoff_ref IS NULL OR length(signoff_ref) BETWEEN 1 AND 200)
    AND (reason IS NULL OR length(reason) BETWEEN 1 AND 2000)
    AND (evidence_name IS NULL OR length(evidence_name) BETWEEN 1 AND 255))
);
-- A gate is signed once per version.
CREATE UNIQUE INDEX ctc_applicability_event_gate_once ON ztax.ctc_applicability_event (tenant_id, applicability_id, version, gate)
  WHERE kind = 'GATE_SIGNED';

CREATE TABLE ztax.ctc_validation (
  tenant_id              uuid        NOT NULL,
  validation_id          uuid        NOT NULL,
  document_id            uuid        NOT NULL,
  applicability_id       uuid        NOT NULL,
  applicability_version  integer     NOT NULL,
  profile                text        NOT NULL,
  format_version         text        NULL,
  ruleset                text        NOT NULL,
  input_digest           text        NOT NULL,
  outcome                text        NOT NULL,
  report_digest          text        NOT NULL,
  validated_at           timestamptz NOT NULL,
  validated_by           uuid        NOT NULL,
  PRIMARY KEY (tenant_id, validation_id),
  FOREIGN KEY (tenant_id, document_id) REFERENCES ztax.fiscal_document (tenant_id, document_id),
  FOREIGN KEY (tenant_id, applicability_id, applicability_version)
    REFERENCES ztax.ctc_applicability_version (tenant_id, applicability_id, version),
  CONSTRAINT ctc_validation_outcome_known CHECK (outcome IN ('PASSED', 'FAILED')),
  CONSTRAINT ctc_validation_digest_shape CHECK (input_digest ~ '^zt1:[0-9a-f]{64}$' AND report_digest ~ '^zt1:[0-9a-f]{64}$'),
  CONSTRAINT ctc_validation_ruleset_length CHECK (length(ruleset) BETWEEN 1 AND 200)
);
-- Validation is deterministic: one report per document state, record version
-- and ruleset.
CREATE UNIQUE INDEX ctc_validation_once ON ztax.ctc_validation (tenant_id, document_id, applicability_id,
  applicability_version, ruleset, input_digest);
CREATE INDEX ctc_validation_by_document ON ztax.ctc_validation (tenant_id, document_id, validated_at);

CREATE TABLE ztax.ctc_validation_result (
  tenant_id      uuid    NOT NULL,
  validation_id  uuid    NOT NULL,
  ordinal        integer NOT NULL,
  rule_id        text    NOT NULL,
  stage          text    NOT NULL,
  severity       text    NOT NULL,
  outcome        text    NOT NULL,
  reason_code    text    NULL,
  business_term  text    NULL,
  detail         text    NULL,
  PRIMARY KEY (tenant_id, validation_id, ordinal),
  FOREIGN KEY (tenant_id, validation_id) REFERENCES ztax.ctc_validation (tenant_id, validation_id),
  CONSTRAINT ctc_validation_result_stage_known CHECK (stage IN ('APPLICABILITY', 'SEMANTIC', 'REGIME', 'SYNTAX')),
  CONSTRAINT ctc_validation_result_severity_known CHECK (severity IN ('FATAL', 'WARNING')),
  CONSTRAINT ctc_validation_result_outcome_known CHECK (outcome IN ('PASS', 'FAIL', 'NOT_EVALUATED')),
  -- A rule that did not pass says which reason.
  CONSTRAINT ctc_validation_result_reasoned CHECK ((outcome = 'PASS') = (reason_code IS NULL))
);

CREATE TABLE ztax.ctc_submission (
  tenant_id              uuid        NOT NULL,
  submission_id          uuid        NOT NULL,
  document_id            uuid        NOT NULL,
  document_number        text        NULL,
  document_type          text        NOT NULL,
  legal_entity_id        uuid        NOT NULL,
  country                text        NOT NULL,
  buyer_kind             text        NOT NULL,
  applicability_id       uuid        NOT NULL,
  applicability_version  integer     NOT NULL,
  model                  text        NOT NULL,
  profile                text        NOT NULL,
  format_version         text        NULL,
  validation_id          uuid        NOT NULL,
  currency               text        NOT NULL,
  gross_total            numeric     NOT NULL,
  payload_digest         text        NOT NULL,
  prepared_at            timestamptz NOT NULL,
  prepared_by            uuid        NOT NULL,
  PRIMARY KEY (tenant_id, submission_id),
  FOREIGN KEY (tenant_id, document_id) REFERENCES ztax.fiscal_document (tenant_id, document_id),
  FOREIGN KEY (tenant_id, legal_entity_id) REFERENCES ztax.legal_entity (tenant_id, legal_entity_id),
  FOREIGN KEY (tenant_id, applicability_id, applicability_version)
    REFERENCES ztax.ctc_applicability_version (tenant_id, applicability_id, version),
  FOREIGN KEY (tenant_id, validation_id) REFERENCES ztax.ctc_validation (tenant_id, validation_id),
  CONSTRAINT ctc_submission_country_shape CHECK (country ~ '^[A-Z]{2}$'),
  CONSTRAINT ctc_submission_currency_shape CHECK (currency ~ '^[A-Z]{3}$'),
  CONSTRAINT ctc_submission_model_dispatches CHECK (model IN ('CLEARANCE', 'REAL_TIME_REPORTING', 'POST_AUDIT',
    'NETWORK_EXCHANGE')),
  CONSTRAINT ctc_submission_digest_shape CHECK (payload_digest ~ '^zt1:[0-9a-f]{64}$')
);
CREATE INDEX ctc_submission_by_document ON ztax.ctc_submission (tenant_id, document_id, applicability_id, prepared_at);
CREATE INDEX ctc_submission_by_time ON ztax.ctc_submission (tenant_id, prepared_at);

CREATE TABLE ztax.ctc_submission_event (
  tenant_id        uuid        NOT NULL,
  submission_id    uuid        NOT NULL,
  seq              integer     NOT NULL,
  kind             text        NOT NULL,
  status_after     text        NOT NULL,
  attempt_id       uuid        NULL,
  transport_ref    text        NULL,
  authority_ref    text        NULL,
  authority_code   text        NULL,
  evidence_sha256  text        NULL,
  detail           text        NULL,
  recorded_at      timestamptz NOT NULL,
  recorded_by      uuid        NOT NULL,
  PRIMARY KEY (tenant_id, submission_id, seq),
  FOREIGN KEY (tenant_id, submission_id) REFERENCES ztax.ctc_submission (tenant_id, submission_id),
  CONSTRAINT ctc_submission_event_kind_known CHECK (kind IN ('PREPARED', 'DISPATCHED', 'TRANSPORT_RECEIVED',
    'AUTHORITY_ACCEPTED', 'AUTHORITY_REJECTED', 'OUTCOME_UNKNOWN', 'STATUS_NOT_FOUND', 'WITHDRAWN')),
  CONSTRAINT ctc_submission_event_status_known CHECK (status_after IN ('PREPARED', 'SENT', 'TRANSPORT_RECEIVED',
    'AUTHORITY_ACCEPTED', 'AUTHORITY_REJECTED', 'UNCERTAIN_EXTERNAL_STATE', 'WITHDRAWN')),
  CONSTRAINT ctc_submission_event_prepared_first CHECK ((kind = 'PREPARED') = (seq = 1)),
  -- Every event but the first and a withdrawal is about one attempt.
  CONSTRAINT ctc_submission_event_attempt CHECK ((kind IN ('PREPARED', 'WITHDRAWN')) = (attempt_id IS NULL)),
  -- An acceptance names the authority's reference; an authority outcome
  -- carries the digest of the raw receipt; a rejection keeps the authority's
  -- own code; a receipt and an enquiry name the transport reference.
  CONSTRAINT ctc_submission_event_accepted_reference CHECK (kind <> 'AUTHORITY_ACCEPTED' OR authority_ref IS NOT NULL),
  CONSTRAINT ctc_submission_event_rejected_code CHECK (kind <> 'AUTHORITY_REJECTED' OR authority_code IS NOT NULL),
  CONSTRAINT ctc_submission_event_outcome_evidence CHECK (kind NOT IN ('AUTHORITY_ACCEPTED', 'AUTHORITY_REJECTED')
    OR evidence_sha256 IS NOT NULL),
  CONSTRAINT ctc_submission_event_transport CHECK (kind NOT IN ('TRANSPORT_RECEIVED', 'STATUS_NOT_FOUND')
    OR transport_ref IS NOT NULL),
  CONSTRAINT ctc_submission_event_explained CHECK (kind NOT IN ('AUTHORITY_REJECTED', 'OUTCOME_UNKNOWN',
    'STATUS_NOT_FOUND', 'WITHDRAWN') OR detail IS NOT NULL),
  CONSTRAINT ctc_submission_event_evidence_shape CHECK (evidence_sha256 IS NULL OR evidence_sha256 ~ '^[0-9a-f]{64}$'),
  CONSTRAINT ctc_submission_event_text_length CHECK (
    (transport_ref IS NULL OR length(transport_ref) BETWEEN 1 AND 200)
    AND (authority_ref IS NULL OR length(authority_ref) BETWEEN 1 AND 200)
    AND (authority_code IS NULL OR length(authority_code) BETWEEN 1 AND 200)
    AND (detail IS NULL OR length(detail) BETWEEN 1 AND 2000))
);
-- An attempt is made once; an authority outcome is recorded once.
CREATE UNIQUE INDEX ctc_submission_event_attempt_once ON ztax.ctc_submission_event (tenant_id, attempt_id)
  WHERE kind = 'DISPATCHED';
CREATE UNIQUE INDEX ctc_submission_event_outcome_once ON ztax.ctc_submission_event (tenant_id, submission_id)
  WHERE kind IN ('AUTHORITY_ACCEPTED', 'AUTHORITY_REJECTED');

REVOKE UPDATE, DELETE, TRUNCATE ON ztax.ctc_applicability FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.ctc_applicability_version FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.ctc_applicability_event FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.ctc_validation FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.ctc_validation_result FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.ctc_submission FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.ctc_submission_event FROM ztax_app;

ALTER TABLE ztax.ctc_applicability ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.ctc_applicability
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.ctc_applicability_version ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.ctc_applicability_version
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.ctc_applicability_event ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.ctc_applicability_event
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.ctc_validation ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.ctc_validation
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.ctc_validation_result ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.ctc_validation_result
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.ctc_submission ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.ctc_submission
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.ctc_submission_event ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.ctc_submission_event
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

COMMENT ON TABLE ztax.ctc_applicability IS
  'A country applicability record: one legal entity, country, document type and buyer kind (Doc 21 W01). Append-only.';
COMMENT ON TABLE ztax.ctc_applicability_version IS
  'One version of a record''s terms: duty, model, format profile, authority, legal source and effective dates. Never edited; a change is the next version.';
COMMENT ON TABLE ztax.ctc_applicability_event IS
  'Evidence, review, G0-G4 sign-offs on an exact version, activation, suspension and retirement of a record, in order. Append-only.';
COMMENT ON TABLE ztax.ctc_validation IS
  'An immutable format validation report: document input digest, record version and ruleset version (Doc 21 W06).';
COMMENT ON TABLE ztax.ctc_submission IS
  'A sealed authority submission of a fiscal document under a record version and a passing validation (Doc 21 W14). Append-only.';
COMMENT ON TABLE ztax.ctc_submission_event IS
  'Preparation, dispatch attempts and the transport and authority answers a person recorded, in order. Transport receipt and authority outcome are distinct events. Append-only.';
