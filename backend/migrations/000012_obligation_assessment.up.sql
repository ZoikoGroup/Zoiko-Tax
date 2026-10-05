-- Obligations assessed from committed decisions (W2 lane I).
--
-- 000003 created ztax.obligation with the columns of an instance: period, due
-- date, status, amount. An obligation decision also records who owes it, to
-- which authority, under which definition version and which signed content
-- (ZTAX-OBL-REQ-0018, -0024), so those columns are added here. They are
-- nullable because the table predates them; every row this release writes
-- sets them.
ALTER TABLE ztax.obligation
  ADD COLUMN legal_entity_id    uuid NULL,
  ADD COLUMN authority          text NULL,
  ADD COLUMN definition_id      text NULL,
  ADD COLUMN definition_version text NULL,
  ADD COLUMN bundle_id          text NULL,
  ADD COLUMN bundle_digest      text NULL,
  ADD COLUMN duty               text NULL,
  -- The legal calendar's timezone. The period and due dates are civil dates
  -- in it, and "overdue" is a question asked of that calendar's today.
  ADD COLUMN legal_timezone     text NULL,
  ADD COLUMN reason_code        text NULL,
  -- The user whose act wrote the row; NULL for a row the system wrote on a
  -- commit.
  ADD COLUMN recorded_by        uuid NULL,
  ADD CONSTRAINT obligation_legal_entity_fk
    FOREIGN KEY (tenant_id, legal_entity_id) REFERENCES ztax.legal_entity (tenant_id, legal_entity_id),
  ADD CONSTRAINT obligation_supersedes_fk
    FOREIGN KEY (tenant_id, supersedes_id) REFERENCES ztax.obligation (tenant_id, obligation_id),
  ADD CONSTRAINT obligation_bundle_digest_shape CHECK (bundle_digest IS NULL OR bundle_digest LIKE 'zt1:%');

-- An obligation's history is a chain: one root per business key, and each row
-- superseded at most once. Two writers that both read the same current row
-- cannot both append after it; the second is refused rather than forking the
-- history into two "current" states.
CREATE UNIQUE INDEX obligation_one_root ON ztax.obligation (tenant_id, business_key) WHERE supersedes_id IS NULL;
CREATE UNIQUE INDEX obligation_superseded_once ON ztax.obligation (tenant_id, supersedes_id) WHERE supersedes_id IS NOT NULL;

-- ---------------------------------------------------------------------------
-- obligation_contribution — what each decision assessed into an obligation
-- ---------------------------------------------------------------------------
-- The assessed amount is the sum of these rows, never a figure carried forward
-- from one obligation row to the next: a retried commit cannot count twice
-- (the key refuses it), and a correction withdraws exactly what the corrected
-- decision added.
CREATE TABLE ztax.obligation_contribution (
  tenant_id     uuid        NOT NULL REFERENCES ztax.tenant (tenant_id),
  business_key  text        NOT NULL,
  decision_id   uuid        NOT NULL,
  kind          text        NOT NULL,
  -- Signed: a withdrawal is the negation of the assessment it withdraws.
  amount        numeric     NOT NULL,
  currency      text        NOT NULL,
  recorded_at   timestamptz NOT NULL,
  PRIMARY KEY (tenant_id, business_key, decision_id, kind),
  FOREIGN KEY (tenant_id, decision_id) REFERENCES ztax.tax_decision (tenant_id, decision_id),
  CONSTRAINT obligation_contribution_kind_known CHECK (kind IN ('ASSESS', 'WITHDRAW')),
  CONSTRAINT obligation_contribution_currency_shape CHECK (currency ~ '^[A-Z]{3}$')
);

REVOKE UPDATE, DELETE, TRUNCATE ON ztax.obligation_contribution FROM ztax_app;

COMMENT ON TABLE ztax.obligation_contribution IS
  'Per-decision assessments into an obligation. Append-only; the assessed amount is their sum.';
