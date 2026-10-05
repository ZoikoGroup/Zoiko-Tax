-- Legal entities and the Tax Control Subledger (W2 lanes I and J).
--
-- A journal carries the legal entity it posts for (ZTAX-FIN-REQ-0039), and an
-- obligation the entity that owes it (ZTAX-OBL-REQ-0018). The estate had no
-- legal entity at all, so this migration adds the minimum: a tenant's legal
-- entities, one of them its default. Every existing tenant is given a default
-- entity named after it, so a commit for a tenant provisioned before this
-- migration has somewhere to post; new tenants get theirs at provisioning.

-- ---------------------------------------------------------------------------
-- legal_entity
-- ---------------------------------------------------------------------------
CREATE TABLE ztax.legal_entity (
  tenant_id       uuid        NOT NULL REFERENCES ztax.tenant (tenant_id),
  legal_entity_id uuid        NOT NULL,
  name            text        NOT NULL,
  -- ISO 3166-1 alpha-2, when known. NULL is "not recorded", never a default
  -- country: an entity's establishment is a fact somebody states.
  country_code    text        NULL,
  is_default      boolean     NOT NULL,
  created_at      timestamptz NOT NULL,
  PRIMARY KEY (tenant_id, legal_entity_id),
  CONSTRAINT legal_entity_name_present CHECK (name <> ''),
  CONSTRAINT legal_entity_country_shape CHECK (country_code IS NULL OR country_code ~ '^[A-Z]{2}$')
);

-- At most one default per tenant.
CREATE UNIQUE INDEX legal_entity_one_default ON ztax.legal_entity (tenant_id) WHERE is_default;

INSERT INTO ztax.legal_entity (tenant_id, legal_entity_id, name, country_code, is_default, created_at)
SELECT t.tenant_id, gen_random_uuid(), t.display_name, NULL, true, now()
FROM   ztax.tenant t;

-- Entities are added, not edited; a rename is out of scope until the legal
-- entity lifecycle (DOM-001) is specified.
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.legal_entity FROM ztax_app;

-- ---------------------------------------------------------------------------
-- tcsl_journal and tcsl_journal_line — ZTAX-FIN-001 §10-§11
-- ---------------------------------------------------------------------------
CREATE TABLE ztax.tcsl_journal (
  tenant_id        uuid        NOT NULL REFERENCES ztax.tenant (tenant_id),
  journal_id       uuid        NOT NULL,
  legal_entity_id  uuid        NOT NULL,
  journal_type     text        NOT NULL,
  source_kind      text        NOT NULL,
  source_id        text        NOT NULL,
  posting_date     timestamptz NOT NULL,
  legal_period     text        NOT NULL,
  currency         text        NOT NULL,
  reversal_of      uuid        NULL,
  profile_id       text        NOT NULL,
  profile_version  text        NOT NULL,
  amendment        boolean     NOT NULL,
  migration_source text        NULL,
  cutover          timestamptz NULL,
  recorded_at      timestamptz NOT NULL,
  PRIMARY KEY (tenant_id, journal_id),
  FOREIGN KEY (tenant_id, legal_entity_id) REFERENCES ztax.legal_entity (tenant_id, legal_entity_id),
  FOREIGN KEY (tenant_id, reversal_of) REFERENCES ztax.tcsl_journal (tenant_id, journal_id),
  CONSTRAINT tcsl_journal_type_known CHECK (journal_type IN (
    'INVOICE', 'CREDIT', 'REFUND', 'LIABILITY_ACCRUAL', 'RETURN', 'REMITTANCE', 'FX', 'ROUNDING',
    'MIGRATION', 'ADJUSTMENT')),
  CONSTRAINT tcsl_journal_currency_shape CHECK (currency ~ '^[A-Z]{3}$'),
  CONSTRAINT tcsl_journal_migration_provenance CHECK (
    (journal_type = 'MIGRATION') = (migration_source IS NOT NULL AND cutover IS NOT NULL))
);

-- One source event posts once per profile in one currency: a retried commit
-- cannot post twice (ZTAX-FIN-REQ-0087, -0117). A reversal is a different
-- source event.
CREATE UNIQUE INDEX tcsl_journal_once_per_source
  ON ztax.tcsl_journal (tenant_id, source_kind, source_id, profile_id, currency);

CREATE TABLE ztax.tcsl_journal_line (
  tenant_id     uuid    NOT NULL,
  journal_id    uuid    NOT NULL,
  line_no       int     NOT NULL,
  account       text    NOT NULL,
  side          text    NOT NULL,
  amount        numeric NOT NULL,
  currency      text    NOT NULL,
  authority     text    NULL,
  jurisdiction  text    NULL,
  decision_id   uuid    NULL,
  document_id   uuid    NULL,
  obligation_id uuid    NULL,
  PRIMARY KEY (tenant_id, journal_id, line_no),
  FOREIGN KEY (tenant_id, journal_id) REFERENCES ztax.tcsl_journal (tenant_id, journal_id),
  CONSTRAINT tcsl_line_side_known CHECK (side IN ('DEBIT', 'CREDIT')),
  -- Positive amounts; the side carries the direction (ZTAX-FIN-REQ-0044).
  CONSTRAINT tcsl_line_amount_positive CHECK (amount > 0),
  CONSTRAINT tcsl_line_account_known CHECK (account IN (
    'TAX_COLLECTED_LIABILITY', 'TAX_ACCRUED_LIABILITY', 'TAX_RECOVERABLE', 'TAX_RECEIVABLE_CONTROL',
    'TAX_CASH_CLEARING', 'TAX_RETURN_CLEARING', 'TAX_REMITTANCE_CLEARING', 'TAX_ADJUSTMENT_CONTROL',
    'FX_CONTROL', 'ROUNDING_CONTROL', 'SUSPENSE_EXCEPTION', 'CUSTOMER_GL_BRIDGE')),
  CONSTRAINT tcsl_line_currency_shape CHECK (currency ~ '^[A-Z]{3}$')
);

CREATE INDEX tcsl_line_by_account ON ztax.tcsl_journal_line (tenant_id, account, currency);

-- Posted journals are append-only (ZTAX-FIN-REQ-0036, -0038). A correction is
-- a reversal journal, never an edit.
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.tcsl_journal FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.tcsl_journal_line FROM ztax_app;

COMMENT ON TABLE ztax.legal_entity IS
  'A tenant''s statutory legal entities (ZTAX-OBL-REQ-0018). Distinct from tenant, account and brand.';
COMMENT ON TABLE ztax.tcsl_journal IS
  'Tax Control Subledger journals (ZTAX-FIN-001 §10). Balanced per currency, append-only; not the customer GL.';
