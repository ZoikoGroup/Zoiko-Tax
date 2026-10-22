-- Migration and historical conversion (ZTAX-MIG-001 §5 to §16, §26; W4 lane
-- M).
--
-- Preserve before transform: a source snapshot is stored as received, with
-- its hash, count and declared control totals, and is never edited. A run
-- converts one snapshot under one mapping version AS_KNOWN — the amounts the
-- source recorded, never recalculated — and keeps each record's source
-- identifier beside the canonical identity it was given (the crosswalk,
-- ZTAX-MIG-001 §9). What it could not carry is an exception with a
-- disposition, never a default bucket (§15).
--
-- Converted history lives here, apart from tax_decision: a legacy result
-- explains history; it does not become a ZoikoTax decision (§1).

CREATE TABLE ztax.migration_program (
  tenant_id        uuid        NOT NULL REFERENCES ztax.tenant (tenant_id),
  program_id       uuid        NOT NULL,
  name             text        NOT NULL,
  migration_type   text        NOT NULL,
  legal_entity_id  uuid        NOT NULL,
  period_from      date        NOT NULL,
  period_to        date        NOT NULL,
  amount_scale     integer     NOT NULL,
  created_at       timestamptz NOT NULL,
  created_by       uuid        NOT NULL,
  PRIMARY KEY (tenant_id, program_id),
  FOREIGN KEY (tenant_id, legal_entity_id) REFERENCES ztax.legal_entity (tenant_id, legal_entity_id),
  CONSTRAINT migration_program_type_known CHECK (migration_type IN ('GREENFIELD_ONBOARD', 'CONFIGURATION_MIGRATION',
    'HISTORICAL_DECISION_MIGRATION', 'FULL_LEDGER_MIGRATION', 'INCUMBENT_ENGINE_REPLACEMENT', 'BSS_ERP_TRANSFORMATION',
    'COUNTRY_PACK_EXPANSION', 'MERGER_ACQUISITION', 'DATA_CENTER_REGION_MOVE', 'EXIT_EXPORT')),
  CONSTRAINT migration_program_period CHECK (period_to > period_from),
  CONSTRAINT migration_program_scale CHECK (amount_scale BETWEEN 0 AND 6),
  CONSTRAINT migration_program_name_length CHECK (length(name) BETWEEN 1 AND 200)
);

CREATE TABLE ztax.migration_snapshot (
  tenant_id        uuid        NOT NULL,
  snapshot_id      uuid        NOT NULL,
  program_id       uuid        NOT NULL,
  source_system    text        NOT NULL,
  source_version   text        NULL,
  method           text        NOT NULL,
  watermark        text        NULL,
  records          jsonb       NOT NULL,
  record_count     integer     NOT NULL,
  control_totals   jsonb       NOT NULL,
  snapshot_hash    text        NOT NULL,
  created_at       timestamptz NOT NULL,
  created_by       uuid        NOT NULL,
  PRIMARY KEY (tenant_id, snapshot_id),
  FOREIGN KEY (tenant_id, program_id) REFERENCES ztax.migration_program (tenant_id, program_id),
  CONSTRAINT migration_snapshot_method_known CHECK (method IN ('API', 'DB_EXPORT', 'OBJECT_EXPORT', 'FILE',
    'PROVIDER_EXPORT', 'CONTROLLED_MANUAL')),
  CONSTRAINT migration_snapshot_count CHECK (record_count >= 1)
);

CREATE TABLE ztax.migration_mapping (
  tenant_id        uuid        NOT NULL,
  mapping_id       uuid        NOT NULL,
  program_id       uuid        NOT NULL,
  version          integer     NOT NULL,
  codes            jsonb       NOT NULL,
  created_at       timestamptz NOT NULL,
  created_by       uuid        NOT NULL,
  PRIMARY KEY (tenant_id, mapping_id),
  FOREIGN KEY (tenant_id, program_id) REFERENCES ztax.migration_program (tenant_id, program_id),
  CONSTRAINT migration_mapping_version CHECK (version >= 1)
);
CREATE UNIQUE INDEX migration_mapping_version_once ON ztax.migration_mapping (tenant_id, program_id, version);

CREATE TABLE ztax.migration_run (
  tenant_id          uuid        NOT NULL,
  run_id             uuid        NOT NULL,
  program_id         uuid        NOT NULL,
  snapshot_id        uuid        NOT NULL,
  mapping_id         uuid        NOT NULL,
  mode               text        NOT NULL,
  source_count       integer     NOT NULL,
  converted_count    integer     NOT NULL,
  excepted_count     integer     NOT NULL,
  crosswalk_complete boolean     NOT NULL,
  totals             jsonb       NOT NULL,
  created_at         timestamptz NOT NULL,
  created_by         uuid        NOT NULL,
  PRIMARY KEY (tenant_id, run_id),
  FOREIGN KEY (tenant_id, program_id) REFERENCES ztax.migration_program (tenant_id, program_id),
  FOREIGN KEY (tenant_id, snapshot_id) REFERENCES ztax.migration_snapshot (tenant_id, snapshot_id),
  FOREIGN KEY (tenant_id, mapping_id) REFERENCES ztax.migration_mapping (tenant_id, mapping_id),
  CONSTRAINT migration_run_mode_known CHECK (mode IN ('AS_KNOWN')),
  CONSTRAINT migration_run_counts CHECK (source_count >= 0 AND converted_count >= 0 AND excepted_count >= 0)
);

CREATE TABLE ztax.migration_record (
  tenant_id        uuid        NOT NULL,
  run_id           uuid        NOT NULL,
  seq              integer     NOT NULL,
  source_id        text        NOT NULL,
  canonical_id     uuid        NOT NULL,
  document_date    date        NOT NULL,
  currency         text        NOT NULL,
  total            numeric     NOT NULL,
  components       jsonb       NOT NULL,
  PRIMARY KEY (tenant_id, run_id, seq),
  FOREIGN KEY (tenant_id, run_id) REFERENCES ztax.migration_run (tenant_id, run_id),
  CONSTRAINT migration_record_currency_shape CHECK (currency ~ '^[A-Z]{3}$')
);
CREATE UNIQUE INDEX migration_record_canonical_once ON ztax.migration_record (tenant_id, canonical_id);
CREATE UNIQUE INDEX migration_record_source_once ON ztax.migration_record (tenant_id, run_id, source_id);

CREATE TABLE ztax.migration_exception (
  tenant_id        uuid        NOT NULL,
  exception_id     uuid        NOT NULL,
  run_id           uuid        NOT NULL,
  seq              integer     NOT NULL,
  kind             text        NOT NULL,
  severity         text        NOT NULL,
  source_id        text        NULL,
  detail           text        NOT NULL,
  PRIMARY KEY (tenant_id, exception_id),
  FOREIGN KEY (tenant_id, run_id) REFERENCES ztax.migration_run (tenant_id, run_id),
  CONSTRAINT migration_exception_kind_known CHECK (kind IN ('UNMAPPED_CODE', 'DUPLICATE_SOURCE', 'PRECISION_LOSS',
    'DATE_CONFLICT', 'CURRENCY_CONFLICT', 'CONTROL_TOTAL_MISMATCH', 'MISSING_SOURCE', 'INVALID_RELATION')),
  CONSTRAINT migration_exception_severity_known CHECK (severity IN ('HIGH', 'MEDIUM', 'LOW'))
);
CREATE UNIQUE INDEX migration_exception_seq ON ztax.migration_exception (tenant_id, run_id, seq);

-- One decision per exception, and it is final.
CREATE TABLE ztax.migration_exception_decision (
  tenant_id        uuid        NOT NULL,
  exception_id     uuid        NOT NULL,
  disposition      text        NOT NULL,
  note             text        NOT NULL,
  decided_at       timestamptz NOT NULL,
  decided_by       uuid        NOT NULL,
  PRIMARY KEY (tenant_id, exception_id),
  FOREIGN KEY (tenant_id, exception_id) REFERENCES ztax.migration_exception (tenant_id, exception_id),
  CONSTRAINT migration_exception_decision_known CHECK (disposition IN ('RESOLVED', 'ACCEPTED_RESIDUAL')),
  CONSTRAINT migration_exception_decision_note CHECK (length(note) BETWEEN 1 AND 2000)
);

CREATE TABLE ztax.migration_cutover_event (
  tenant_id        uuid        NOT NULL,
  program_id       uuid        NOT NULL,
  seq              integer     NOT NULL,
  state            text        NOT NULL,
  note             text        NOT NULL,
  recorded_at      timestamptz NOT NULL,
  recorded_by      uuid        NOT NULL,
  PRIMARY KEY (tenant_id, program_id, seq),
  FOREIGN KEY (tenant_id, program_id) REFERENCES ztax.migration_program (tenant_id, program_id),
  CONSTRAINT migration_cutover_state_known CHECK (state IN ('PRECHECK', 'FREEZE_ACTIVE', 'FINAL_DELTA', 'RECONCILING',
    'READY_TO_SWITCH', 'ROUTING_SWITCH', 'VALIDATING_LIVE', 'CUTOVER_COMPLETE', 'ABORTED', 'ROLLBACK_ACTIVE',
    'ROLLED_BACK', 'FAILED_MANUAL_RECOVERY')),
  CONSTRAINT migration_cutover_note CHECK (length(note) BETWEEN 1 AND 2000)
);

REVOKE UPDATE, DELETE, TRUNCATE ON ztax.migration_program FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.migration_snapshot FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.migration_mapping FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.migration_run FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.migration_record FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.migration_exception FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.migration_exception_decision FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.migration_cutover_event FROM ztax_app;

DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['migration_program', 'migration_snapshot', 'migration_mapping', 'migration_run',
    'migration_record', 'migration_exception', 'migration_exception_decision', 'migration_cutover_event'] LOOP
    EXECUTE format('ALTER TABLE ztax.%I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format($p$CREATE POLICY tenant_isolation ON ztax.%I
      USING (current_setting('ztax.scope', true) = 'cell'
             OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
      WITH CHECK (current_setting('ztax.scope', true) = 'cell'
             OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)$p$, t);
  END LOOP;
END $$;

COMMENT ON TABLE ztax.migration_snapshot IS
  'An immutable source extraction: records as received, hash, count and declared control totals.';
COMMENT ON TABLE ztax.migration_record IS
  'History carried AS_KNOWN, with the crosswalk from source id to canonical id. Not a ZoikoTax decision.';
