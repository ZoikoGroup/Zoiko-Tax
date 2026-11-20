-- ERP journal bridge and subledger-to-GL reconciliation (LED-23 ERP Journal
-- Bridge, Export & Acknowledgment; LED-18 "compare GL balance"; R6 of
-- ZTAX-FIN-001 §17).
--
-- A bridge connects one member legal entity's Tax Control Subledger to one
-- customer general ledger under a company code. Its account mapping is
-- versioned: a version is never edited, and a change is the next version.
--
-- An export batch is sealed when it is prepared: the exact journal ID set,
-- every mapped line (each pointing at the TCSL journal line it carries) and
-- the mapping version, under one digest. Its transfer state is the fold of
-- appended events: PREPARED, then DISPATCHED (ZoikoTax's own act, written
-- with its outbox event), then what the ERP answered as a person recorded
-- it — ACKNOWLEDGED (transport receipt) kept apart from POSTED (the ERP's
-- posted journal), REJECTED, OUTCOME_UNKNOWN and STATUS_NOT_FOUND (a status
-- query that found nothing). Nothing here is ever an ERP answer ZoikoTax
-- made up.
--
-- A GL snapshot is the period activity an ERP reported on each GL account,
-- with the report it came from. A GL reconciliation compares it with the
-- subledger and keeps every item's exact variance, and whether the period
-- was held by journals not yet posted in the ERP.
--
-- No personal data: the rows are a provider's ledger aggregates and
-- references into its own systems. The only person named is whoever recorded
-- a step.

CREATE TABLE ztax.erp_bridge (
  tenant_id        uuid        NOT NULL REFERENCES ztax.tenant (tenant_id),
  bridge_id        uuid        NOT NULL,
  legal_entity_id  uuid        NOT NULL,
  name             text        NOT NULL,
  vendor           text        NOT NULL,
  company_code     text        NOT NULL,
  created_at       timestamptz NOT NULL,
  created_by       uuid        NOT NULL,
  PRIMARY KEY (tenant_id, bridge_id),
  FOREIGN KEY (tenant_id, legal_entity_id) REFERENCES ztax.legal_entity (tenant_id, legal_entity_id),
  CONSTRAINT erp_bridge_vendor_known CHECK (vendor IN ('SAP', 'ORACLE', 'NETSUITE', 'DYNAMICS', 'OTHER')),
  CONSTRAINT erp_bridge_company_code_shape CHECK (company_code ~ '^[A-Za-z0-9._-]{1,40}$'),
  CONSTRAINT erp_bridge_name_length CHECK (length(name) BETWEEN 1 AND 200)
);

CREATE TABLE ztax.erp_account_mapping (
  tenant_id      uuid        NOT NULL,
  bridge_id      uuid        NOT NULL,
  version        integer     NOT NULL,
  chart_version  text        NOT NULL,
  digest         text        NOT NULL,
  recorded_at    timestamptz NOT NULL,
  recorded_by    uuid        NOT NULL,
  PRIMARY KEY (tenant_id, bridge_id, version),
  FOREIGN KEY (tenant_id, bridge_id) REFERENCES ztax.erp_bridge (tenant_id, bridge_id),
  CONSTRAINT erp_account_mapping_version_positive CHECK (version >= 1),
  CONSTRAINT erp_account_mapping_chart_length CHECK (length(chart_version) BETWEEN 1 AND 200),
  CONSTRAINT erp_account_mapping_digest_shape CHECK (digest ~ '^zt1:[0-9a-f]{64}$')
);

CREATE TABLE ztax.erp_account_mapping_line (
  tenant_id     uuid    NOT NULL,
  bridge_id     uuid    NOT NULL,
  version       integer NOT NULL,
  tcsl_account  text    NOT NULL,
  gl_account    text    NOT NULL,
  PRIMARY KEY (tenant_id, bridge_id, version, tcsl_account),
  FOREIGN KEY (tenant_id, bridge_id, version) REFERENCES ztax.erp_account_mapping (tenant_id, bridge_id, version),
  CONSTRAINT erp_account_mapping_line_gl_shape CHECK (gl_account ~ '^[A-Za-z0-9._:/-]{1,60}$')
);

CREATE TABLE ztax.erp_export_batch (
  tenant_id        uuid        NOT NULL,
  batch_id         uuid        NOT NULL,
  bridge_id        uuid        NOT NULL,
  legal_entity_id  uuid        NOT NULL,
  legal_period     text        NOT NULL,
  currency         text        NOT NULL,
  posting_date     date        NOT NULL,
  company_code     text        NOT NULL,
  mapping_version  integer     NOT NULL,
  chart_version    text        NOT NULL,
  mapping_digest   text        NOT NULL,
  total_debits     numeric     NOT NULL,
  total_credits    numeric     NOT NULL,
  batch_digest     text        NOT NULL,
  prepared_at      timestamptz NOT NULL,
  prepared_by      uuid        NOT NULL,
  PRIMARY KEY (tenant_id, batch_id),
  FOREIGN KEY (tenant_id, bridge_id, mapping_version) REFERENCES ztax.erp_account_mapping (tenant_id, bridge_id, version),
  FOREIGN KEY (tenant_id, legal_entity_id) REFERENCES ztax.legal_entity (tenant_id, legal_entity_id),
  CONSTRAINT erp_export_batch_period_shape CHECK (legal_period ~ '^[0-9]{4}-(0[1-9]|1[0-2])$'),
  CONSTRAINT erp_export_batch_currency_shape CHECK (currency ~ '^[A-Z]{3}$'),
  CONSTRAINT erp_export_batch_digest_shape CHECK (batch_digest ~ '^zt1:[0-9a-f]{64}$'),
  -- Every journal balances, so the batch does.
  CONSTRAINT erp_export_batch_balanced CHECK (total_debits = total_credits AND total_debits > 0)
);
CREATE INDEX erp_export_batch_by_period ON ztax.erp_export_batch (tenant_id, bridge_id, legal_period, currency, prepared_at);

CREATE TABLE ztax.erp_export_line (
  tenant_id        uuid    NOT NULL,
  batch_id         uuid    NOT NULL,
  line_no          integer NOT NULL,
  journal_id       uuid    NOT NULL,
  journal_line_no  integer NOT NULL,
  tcsl_account     text    NOT NULL,
  gl_account       text    NOT NULL,
  side             text    NOT NULL,
  amount           numeric NOT NULL,
  authority        text    NULL,
  jurisdiction     text    NULL,
  PRIMARY KEY (tenant_id, batch_id, line_no),
  FOREIGN KEY (tenant_id, batch_id) REFERENCES ztax.erp_export_batch (tenant_id, batch_id),
  -- Each exported line is a posted TCSL journal line, carried once per batch.
  FOREIGN KEY (tenant_id, journal_id, journal_line_no) REFERENCES ztax.tcsl_journal_line (tenant_id, journal_id, line_no),
  CONSTRAINT erp_export_line_side_known CHECK (side IN ('DEBIT', 'CREDIT')),
  CONSTRAINT erp_export_line_amount_positive CHECK (amount > 0)
);
CREATE UNIQUE INDEX erp_export_line_once ON ztax.erp_export_line (tenant_id, batch_id, journal_id, journal_line_no);

CREATE TABLE ztax.erp_export_event (
  tenant_id           uuid        NOT NULL,
  batch_id            uuid        NOT NULL,
  seq                 integer     NOT NULL,
  kind                text        NOT NULL,
  status_after        text        NOT NULL,
  attempt_id          uuid        NULL,
  remote_correlation  text        NULL,
  erp_reference       text        NULL,
  response_digest     text        NULL,
  detail              text        NULL,
  recorded_at         timestamptz NOT NULL,
  recorded_by         uuid        NOT NULL,
  PRIMARY KEY (tenant_id, batch_id, seq),
  FOREIGN KEY (tenant_id, batch_id) REFERENCES ztax.erp_export_batch (tenant_id, batch_id),
  CONSTRAINT erp_export_event_kind_known CHECK (kind IN ('PREPARED', 'DISPATCHED', 'ACKNOWLEDGED', 'POSTED', 'REJECTED',
    'OUTCOME_UNKNOWN', 'STATUS_NOT_FOUND', 'WITHDRAWN')),
  CONSTRAINT erp_export_event_status_known CHECK (status_after IN ('PREPARED', 'TRANSMITTED', 'ACKNOWLEDGED', 'ERP_POSTED',
    'REJECTED', 'OUTCOME_UNKNOWN', 'WITHDRAWN')),
  CONSTRAINT erp_export_event_prepared_first CHECK ((kind = 'PREPARED') = (seq = 1)),
  -- Every event but the first and a withdrawal is about one attempt.
  CONSTRAINT erp_export_event_attempt CHECK ((kind IN ('PREPARED', 'WITHDRAWN')) = (attempt_id IS NULL)),
  -- A posting names the ERP's posted journal; a receipt and a status query
  -- name the ERP's own reference; a rejection, an unknown outcome and a
  -- withdrawal say why.
  CONSTRAINT erp_export_event_posted_reference CHECK (kind <> 'POSTED' OR erp_reference IS NOT NULL),
  CONSTRAINT erp_export_event_correlated CHECK (kind NOT IN ('ACKNOWLEDGED', 'STATUS_NOT_FOUND') OR remote_correlation IS NOT NULL),
  CONSTRAINT erp_export_event_explained CHECK (kind NOT IN ('REJECTED', 'OUTCOME_UNKNOWN', 'WITHDRAWN') OR detail IS NOT NULL),
  CONSTRAINT erp_export_event_text_length CHECK (
    (remote_correlation IS NULL OR length(remote_correlation) BETWEEN 1 AND 200)
    AND (erp_reference IS NULL OR length(erp_reference) BETWEEN 1 AND 200)
    AND (response_digest IS NULL OR length(response_digest) BETWEEN 1 AND 200)
    AND (detail IS NULL OR length(detail) BETWEEN 1 AND 2000))
);
-- An attempt is made once.
CREATE UNIQUE INDEX erp_export_event_attempt_once ON ztax.erp_export_event (tenant_id, attempt_id) WHERE kind = 'DISPATCHED';

CREATE TABLE ztax.erp_gl_snapshot (
  tenant_id      uuid        NOT NULL,
  snapshot_id    uuid        NOT NULL,
  bridge_id      uuid        NOT NULL,
  legal_period   text        NOT NULL,
  currency       text        NOT NULL,
  as_of          timestamptz NOT NULL,
  source_ref     text        NOT NULL,
  source_digest  text        NULL,
  digest         text        NOT NULL,
  recorded_at    timestamptz NOT NULL,
  recorded_by    uuid        NOT NULL,
  PRIMARY KEY (tenant_id, snapshot_id),
  FOREIGN KEY (tenant_id, bridge_id) REFERENCES ztax.erp_bridge (tenant_id, bridge_id),
  CONSTRAINT erp_gl_snapshot_period_shape CHECK (legal_period ~ '^[0-9]{4}-(0[1-9]|1[0-2])$'),
  CONSTRAINT erp_gl_snapshot_currency_shape CHECK (currency ~ '^[A-Z]{3}$'),
  CONSTRAINT erp_gl_snapshot_digest_shape CHECK (digest ~ '^zt1:[0-9a-f]{64}$'),
  CONSTRAINT erp_gl_snapshot_text_length CHECK (length(source_ref) BETWEEN 1 AND 200
    AND (source_digest IS NULL OR length(source_digest) BETWEEN 1 AND 200))
);
CREATE INDEX erp_gl_snapshot_by_period ON ztax.erp_gl_snapshot (tenant_id, bridge_id, legal_period, recorded_at);

CREATE TABLE ztax.erp_gl_snapshot_line (
  tenant_id    uuid    NOT NULL,
  snapshot_id  uuid    NOT NULL,
  gl_account   text    NOT NULL,
  debits       numeric NOT NULL,
  credits      numeric NOT NULL,
  PRIMARY KEY (tenant_id, snapshot_id, gl_account),
  FOREIGN KEY (tenant_id, snapshot_id) REFERENCES ztax.erp_gl_snapshot (tenant_id, snapshot_id),
  CONSTRAINT erp_gl_snapshot_line_gl_shape CHECK (gl_account ~ '^[A-Za-z0-9._:/-]{1,60}$'),
  CONSTRAINT erp_gl_snapshot_line_unsigned CHECK (debits >= 0 AND credits >= 0)
);

CREATE TABLE ztax.erp_gl_reconciliation (
  tenant_id              uuid        NOT NULL,
  reconciliation_id      uuid        NOT NULL,
  bridge_id              uuid        NOT NULL,
  snapshot_id            uuid        NOT NULL,
  snapshot_digest        text        NOT NULL,
  legal_entity_id        uuid        NOT NULL,
  legal_period           text        NOT NULL,
  currency               text        NOT NULL,
  mapping_version        integer     NOT NULL,
  outcome                text        NOT NULL,
  -- REASON:count per hold, in reason order.
  holds                  text[]      NOT NULL,
  journal_count          integer     NOT NULL,
  posted_journal_count   integer     NOT NULL,
  population_digest      text        NOT NULL,
  ran_at                 timestamptz NOT NULL,
  ran_by                 uuid        NULL,
  PRIMARY KEY (tenant_id, reconciliation_id),
  FOREIGN KEY (tenant_id, snapshot_id) REFERENCES ztax.erp_gl_snapshot (tenant_id, snapshot_id),
  FOREIGN KEY (tenant_id, bridge_id, mapping_version) REFERENCES ztax.erp_account_mapping (tenant_id, bridge_id, version),
  FOREIGN KEY (tenant_id, legal_entity_id) REFERENCES ztax.legal_entity (tenant_id, legal_entity_id),
  CONSTRAINT erp_gl_reconciliation_outcome_known CHECK (outcome IN ('RECONCILED', 'VARIANCES', 'HELD')),
  -- A held reconciliation says why; one that is not, has nothing holding it.
  CONSTRAINT erp_gl_reconciliation_held_explained CHECK ((outcome = 'HELD') = (cardinality(holds) > 0)),
  CONSTRAINT erp_gl_reconciliation_counts CHECK (posted_journal_count BETWEEN 0 AND journal_count),
  CONSTRAINT erp_gl_reconciliation_period_shape CHECK (legal_period ~ '^[0-9]{4}-(0[1-9]|1[0-2])$'),
  CONSTRAINT erp_gl_reconciliation_digest_shape CHECK (population_digest ~ '^zt1:[0-9a-f]{64}$'
    AND snapshot_digest ~ '^zt1:[0-9a-f]{64}$')
);
CREATE INDEX erp_gl_reconciliation_by_period ON ztax.erp_gl_reconciliation (tenant_id, bridge_id, legal_period, ran_at);

CREATE TABLE ztax.erp_gl_reconciliation_item (
  tenant_id          uuid    NOT NULL,
  reconciliation_id  uuid    NOT NULL,
  ordinal            integer NOT NULL,
  match_key          text    NOT NULL,
  gl_account         text    NULL,
  tcsl_accounts      text[]  NOT NULL,
  expected           numeric NULL,
  observed           numeric NULL,
  variance           numeric NULL,
  status             text    NOT NULL,
  detail             text    NULL,
  PRIMARY KEY (tenant_id, reconciliation_id, ordinal),
  FOREIGN KEY (tenant_id, reconciliation_id) REFERENCES ztax.erp_gl_reconciliation (tenant_id, reconciliation_id),
  CONSTRAINT erp_gl_reconciliation_item_status_known CHECK (status IN ('MATCHED', 'TOLERANCE_MATCH', 'UNMATCHED', 'PARTIAL',
    'DUPLICATE', 'MISSING', 'CONFLICTED', 'PENDING', 'EXPLAINED', 'RESOLVED')),
  -- The variance is kept whenever both sides are known, whatever it is.
  CONSTRAINT erp_gl_reconciliation_item_variance CHECK (
    (expected IS NOT NULL AND observed IS NOT NULL) = (variance IS NOT NULL))
);

REVOKE UPDATE, DELETE, TRUNCATE ON ztax.erp_bridge FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.erp_account_mapping FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.erp_account_mapping_line FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.erp_export_batch FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.erp_export_line FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.erp_export_event FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.erp_gl_snapshot FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.erp_gl_snapshot_line FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.erp_gl_reconciliation FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.erp_gl_reconciliation_item FROM ztax_app;

ALTER TABLE ztax.erp_bridge ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.erp_bridge
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.erp_account_mapping ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.erp_account_mapping
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.erp_account_mapping_line ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.erp_account_mapping_line
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.erp_export_batch ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.erp_export_batch
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.erp_export_line ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.erp_export_line
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.erp_export_event ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.erp_export_event
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.erp_gl_snapshot ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.erp_gl_snapshot
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.erp_gl_snapshot_line ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.erp_gl_snapshot_line
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.erp_gl_reconciliation ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.erp_gl_reconciliation
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.erp_gl_reconciliation_item ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.erp_gl_reconciliation_item
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

COMMENT ON TABLE ztax.erp_bridge IS
  'A member legal entity''s subledger connected to one customer general ledger. Append-only.';
COMMENT ON TABLE ztax.erp_account_mapping IS
  'One version of a bridge''s TCSL-to-GL account mapping. Never edited; a change is the next version.';
COMMENT ON TABLE ztax.erp_export_batch IS
  'A sealed ERP export batch: exact journal set, mapping version and digest. Append-only.';
COMMENT ON TABLE ztax.erp_export_event IS
  'Preparation, dispatch attempts and the ERP''s recorded answers for a batch, in order. Transport receipt and GL posting are distinct events. Append-only.';
COMMENT ON TABLE ztax.erp_gl_snapshot IS
  'GL period activity an ERP reported, with the report it came from. Append-only.';
COMMENT ON TABLE ztax.erp_gl_reconciliation IS
  'One subledger-to-GL reconciliation (R6): exact variances per GL account, and what held the period. Append-only; a re-run is a new record.';
