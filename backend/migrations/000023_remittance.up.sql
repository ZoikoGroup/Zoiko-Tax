-- Remittance and funds control (ZTAX-REM-001 §4, §7, §10, §11; W4 lane M).
--
-- ZoikoTax never holds the money. Nothing here is a balance, a wallet or a
-- ledger of customer funds: a funding account and a payee account are
-- references to accounts at regulated institutions, held in a vault, and the
-- shape checks refuse anything that looks like an account number
-- (ZTAX-REM-REQ-0001, -0009, -0011).
--
-- Beneficiaries are versioned master data. A version is immutable; its state
-- is its events — verified by someone other than its author, suspended, or
-- superseded by a newer verified version (ZTAX-REM-REQ-0012, -0014).
--
-- A remittance is an immutable instruction. Every field a payment executes
-- on is in the row and in its instruction hash; approval binds the hash, and
-- what the provider reports is a step after it, never an edit
-- (ZTAX-REM-REQ-0026, -0033, -0036, -0041).

CREATE TABLE ztax.remittance_beneficiary (
  tenant_id        uuid        NOT NULL REFERENCES ztax.tenant (tenant_id),
  beneficiary_id   uuid        NOT NULL,
  supersedes       uuid        NULL,
  country_code     text        NOT NULL,
  authority        text        NOT NULL,
  legal_name       text        NOT NULL,
  payment_rail     text        NOT NULL,
  account_ref      text        NOT NULL,
  reference_rule   text        NULL,
  currencies       text[]      NOT NULL,
  source           text        NOT NULL,
  created_at       timestamptz NOT NULL,
  created_by       uuid        NOT NULL,
  PRIMARY KEY (tenant_id, beneficiary_id),
  FOREIGN KEY (tenant_id, supersedes) REFERENCES ztax.remittance_beneficiary (tenant_id, beneficiary_id),
  CONSTRAINT remittance_beneficiary_country_shape CHECK (country_code ~ '^[A-Z]{2}$'),
  CONSTRAINT remittance_beneficiary_rail_known CHECK (payment_rail IN ('ACH', 'WIRE', 'SEPA', 'RTGS',
    'LOCAL_TRANSFER', 'PORTAL', 'DIRECT_DEBIT', 'OTHER')),
  CONSTRAINT remittance_beneficiary_account_is_a_reference CHECK (account_ref
    ~ '^(vault|awssm|gcpsm|azkv|token)://[A-Za-z0-9._/-]{1,240}$'),
  CONSTRAINT remittance_beneficiary_currencies CHECK (cardinality(currencies) >= 1),
  CONSTRAINT remittance_beneficiary_text_length CHECK (length(legal_name) BETWEEN 1 AND 300
    AND length(source) BETWEEN 1 AND 2000 AND (reference_rule IS NULL OR length(reference_rule) BETWEEN 1 AND 200))
);
-- A version is superseded at most once.
CREATE UNIQUE INDEX remittance_beneficiary_supersedes_once ON ztax.remittance_beneficiary (tenant_id, supersedes)
  WHERE supersedes IS NOT NULL;

CREATE TABLE ztax.remittance_beneficiary_event (
  tenant_id        uuid        NOT NULL,
  beneficiary_id   uuid        NOT NULL,
  seq              integer     NOT NULL,
  kind             text        NOT NULL,
  evidence         text        NULL,
  recorded_at      timestamptz NOT NULL,
  recorded_by      uuid        NOT NULL,
  PRIMARY KEY (tenant_id, beneficiary_id, seq),
  FOREIGN KEY (tenant_id, beneficiary_id) REFERENCES ztax.remittance_beneficiary (tenant_id, beneficiary_id),
  CONSTRAINT remittance_beneficiary_event_kind_known CHECK (kind IN ('CREATED', 'VERIFIED', 'SUSPENDED', 'SUPERSEDED')),
  CONSTRAINT remittance_beneficiary_event_created_first CHECK ((kind = 'CREATED') = (seq = 1)),
  CONSTRAINT remittance_beneficiary_event_evidence_length CHECK (evidence IS NULL OR length(evidence) BETWEEN 1 AND 2000)
);

CREATE TABLE ztax.remittance (
  tenant_id          uuid        NOT NULL REFERENCES ztax.tenant (tenant_id),
  remittance_id      uuid        NOT NULL,
  obligation_id      uuid        NOT NULL,
  obligation_key     text        NOT NULL,
  legal_entity_id    uuid        NOT NULL,
  authority          text        NOT NULL,
  period_start       date        NOT NULL,
  period_end         date        NOT NULL,
  payment_due        timestamptz NOT NULL,
  legal_timezone     text        NOT NULL,
  currency           text        NOT NULL,
  amount             numeric     NOT NULL,
  beneficiary_id     uuid        NOT NULL,
  funding_ref        text        NOT NULL,
  payment_reference  text        NOT NULL,
  execution_date     date        NOT NULL,
  operating_mode     text        NOT NULL,
  supersedes         uuid        NULL,
  instruction_hash   text        NOT NULL,
  created_at         timestamptz NOT NULL,
  created_by         uuid        NOT NULL,
  PRIMARY KEY (tenant_id, remittance_id),
  FOREIGN KEY (tenant_id, obligation_id) REFERENCES ztax.obligation (tenant_id, obligation_id),
  FOREIGN KEY (tenant_id, legal_entity_id) REFERENCES ztax.legal_entity (tenant_id, legal_entity_id),
  FOREIGN KEY (tenant_id, beneficiary_id) REFERENCES ztax.remittance_beneficiary (tenant_id, beneficiary_id),
  FOREIGN KEY (tenant_id, supersedes) REFERENCES ztax.remittance (tenant_id, remittance_id),
  CONSTRAINT remittance_amount_positive CHECK (amount > 0),
  CONSTRAINT remittance_currency_shape CHECK (currency ~ '^[A-Z]{3}$'),
  CONSTRAINT remittance_mode_known CHECK (operating_mode IN ('CUSTOMER_EXECUTES', 'BANK_API_INITIATION',
    'TREASURY_PLATFORM', 'DIRECT_DEBIT_AUTHORITY', 'LOCAL_PARTNER', 'MANAGED_ORCHESTRATION', 'OFFLINE_MANUAL')),
  CONSTRAINT remittance_funding_is_a_reference CHECK (funding_ref
    ~ '^(vault|awssm|gcpsm|azkv|token)://[A-Za-z0-9._/-]{1,240}$'),
  CONSTRAINT remittance_reference_length CHECK (length(payment_reference) BETWEEN 1 AND 140)
);
-- One first instruction per obligation, and each instruction replaced at
-- most once: a duplicate payment needs a distinct, linked source
-- (ZTAX-REM-REQ-0007, -0061).
CREATE UNIQUE INDEX remittance_one_root_per_obligation ON ztax.remittance (tenant_id, obligation_key)
  WHERE supersedes IS NULL;
CREATE UNIQUE INDEX remittance_superseded_once ON ztax.remittance (tenant_id, supersedes)
  WHERE supersedes IS NOT NULL;
CREATE INDEX remittance_due ON ztax.remittance (tenant_id, payment_due);

CREATE TABLE ztax.remittance_step (
  tenant_id          uuid        NOT NULL,
  remittance_id      uuid        NOT NULL,
  seq                integer     NOT NULL,
  kind               text        NOT NULL,
  status_after       text        NOT NULL,
  instruction_hash   text        NULL,
  submission_id      uuid        NULL,
  evidence_kind      text        NULL,
  resolution         text        NULL,
  provider_ref       text        NULL,
  raw                text        NULL,
  note               text        NULL,
  recorded_at        timestamptz NOT NULL,
  recorded_by        uuid        NOT NULL,
  PRIMARY KEY (tenant_id, remittance_id, seq),
  FOREIGN KEY (tenant_id, remittance_id) REFERENCES ztax.remittance (tenant_id, remittance_id),
  CONSTRAINT remittance_step_kind_known CHECK (kind IN ('CREATED', 'APPROVED', 'SUBMITTED', 'PROVIDER_STATUS',
    'UNCERTAIN_RESOLVED', 'RETURNED', 'CANCELLED')),
  CONSTRAINT remittance_step_created_first CHECK ((kind = 'CREATED') = (seq = 1)),
  CONSTRAINT remittance_step_status_known CHECK (status_after IN ('APPROVAL_REQUIRED', 'APPROVED', 'SUBMITTED',
    'ACCEPTED_BY_PROVIDER', 'PROCESSING', 'UNCERTAIN_PAYMENT', 'SETTLED', 'REJECTED', 'RETURNED', 'CANCELLED')),
  CONSTRAINT remittance_step_hash_bound CHECK (kind NOT IN ('APPROVED', 'SUBMITTED') OR instruction_hash IS NOT NULL),
  CONSTRAINT remittance_step_submission_named CHECK ((kind = 'SUBMITTED') <= (submission_id IS NOT NULL)),
  CONSTRAINT remittance_step_evidence_known CHECK (evidence_kind IS NULL OR evidence_kind IN ('PROVIDER_ACCEPTANCE',
    'PROCESSING_REFERENCE', 'ACCOUNT_DEBIT', 'BENEFICIARY_CREDIT', 'AUTHORITY_RECEIPT', 'BANK_STATEMENT',
    'PROVIDER_SETTLED', 'MANUAL_EVIDENCE', 'NONE')),
  -- Settlement is proved by settlement evidence, never by acceptance or a
  -- debit (ZTAX-REM-REQ-0043, -0044).
  CONSTRAINT remittance_step_settlement_evidenced CHECK (status_after <> 'SETTLED' OR kind NOT IN ('PROVIDER_STATUS', 'UNCERTAIN_RESOLVED')
    OR (evidence_kind IN ('BENEFICIARY_CREDIT', 'AUTHORITY_RECEIPT', 'BANK_STATEMENT', 'PROVIDER_SETTLED', 'MANUAL_EVIDENCE')
        AND provider_ref IS NOT NULL)),
  CONSTRAINT remittance_step_resolution_named CHECK ((kind = 'UNCERTAIN_RESOLVED') = (resolution IS NOT NULL)),
  CONSTRAINT remittance_step_resolution_known CHECK (resolution IS NULL OR resolution IN ('CONFIRMED_SETTLED',
    'CONFIRMED_NOT_SENT', 'CONFIRMED_REJECTED', 'RETURNED', 'DUPLICATE_SUSPECTED', 'MANUAL_INVESTIGATION')),
  CONSTRAINT remittance_step_raw_kept CHECK (kind NOT IN ('PROVIDER_STATUS', 'UNCERTAIN_RESOLVED', 'RETURNED') OR raw IS NOT NULL),
  CONSTRAINT remittance_step_text_length CHECK ((raw IS NULL OR length(raw) BETWEEN 1 AND 20000)
    AND (note IS NULL OR length(note) BETWEEN 1 AND 2000)
    AND (provider_ref IS NULL OR length(provider_ref) BETWEEN 1 AND 200))
);
CREATE UNIQUE INDEX remittance_step_submission_once ON ztax.remittance_step (tenant_id, submission_id)
  WHERE kind = 'SUBMITTED';

REVOKE UPDATE, DELETE, TRUNCATE ON ztax.remittance_beneficiary FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.remittance_beneficiary_event FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.remittance FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.remittance_step FROM ztax_app;

ALTER TABLE ztax.remittance_beneficiary ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.remittance_beneficiary
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.remittance_beneficiary_event ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.remittance_beneficiary_event
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.remittance ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.remittance
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.remittance_step ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.remittance_step
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

COMMENT ON TABLE ztax.remittance_beneficiary IS
  'One version of an authority''s payee details. Immutable; verified by someone other than its author.';
COMMENT ON TABLE ztax.remittance IS
  'One immutable payment instruction, hashed. ZoikoTax instructs; it never holds the funds.';
COMMENT ON TABLE ztax.remittance_step IS
  'Approval, submission, provider statuses, resolutions and returns, in order. Append-only.';
