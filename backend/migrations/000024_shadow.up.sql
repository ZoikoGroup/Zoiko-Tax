-- Shadow assurance (ZTAX-SHD-001 §4 to §16; W4 lane M).
--
-- Shadow is non-impact: these tables are written by comparison runs and
-- read by people, and nothing here references a fiscal record in a way that
-- could change it. A case names the decision it was paired with; the
-- decision is unaware of it (ZTAX-SHD-REQ-0001).
--
-- A run is immutable once executed: its policy, its input hash and its
-- metrics are written with it, and its cases with it. A discrepancy's state
-- is its event history.

CREATE TABLE ztax.shadow_comparator (
  tenant_id        uuid        NOT NULL REFERENCES ztax.tenant (tenant_id),
  comparator_id    uuid        NOT NULL,
  name             text        NOT NULL,
  comparator_type  text        NOT NULL,
  provider         text        NOT NULL,
  version          text        NOT NULL,
  environment      text        NULL,
  limitations      text        NULL,
  created_at       timestamptz NOT NULL,
  created_by       uuid        NOT NULL,
  PRIMARY KEY (tenant_id, comparator_id),
  CONSTRAINT shadow_comparator_type_known CHECK (comparator_type IN ('COMMERCIAL_TAX_ENGINE', 'CUSTOMER_LEGACY_ENGINE',
    'HISTORICAL_BILLED_RESULT', 'AUTHORITY_CALCULATOR', 'QUALIFIED_REFERENCE_IMPLEMENTATION', 'GOLDEN_VECTOR',
    'FILING_RESULT', 'MANUAL_TAX_REVIEW', 'PRIOR_ZOIKO_RELEASE')),
  CONSTRAINT shadow_comparator_text_length CHECK (length(name) BETWEEN 1 AND 200 AND length(provider) BETWEEN 1 AND 200
    AND length(version) BETWEEN 1 AND 100 AND (limitations IS NULL OR length(limitations) BETWEEN 1 AND 2000)
    AND (environment IS NULL OR length(environment) BETWEEN 1 AND 100))
);

CREATE TABLE ztax.shadow_run (
  tenant_id          uuid        NOT NULL REFERENCES ztax.tenant (tenant_id),
  run_id             uuid        NOT NULL,
  comparator_id      uuid        NOT NULL,
  mode               text        NOT NULL,
  window_from        timestamptz NOT NULL,
  window_to          timestamptz NOT NULL,
  currency           text        NOT NULL,
  component_map      jsonb       NOT NULL,
  tolerance          numeric     NOT NULL,
  materiality        numeric     NOT NULL,
  input_hash         text        NOT NULL,
  cases              integer     NOT NULL,
  paired             integer     NOT NULL,
  exact_matches      integer     NOT NULL,
  equivalent_matches integer     NOT NULL,
  monetary_variances integer     NOT NULL,
  category_variances integer     NOT NULL,
  population_variances integer   NOT NULL,
  uncomparable       integer     NOT NULL,
  material           integer     NOT NULL,
  compared_value     numeric     NOT NULL,
  absolute_variance  numeric     NOT NULL,
  created_at         timestamptz NOT NULL,
  created_by         uuid        NOT NULL,
  PRIMARY KEY (tenant_id, run_id),
  FOREIGN KEY (tenant_id, comparator_id) REFERENCES ztax.shadow_comparator (tenant_id, comparator_id),
  CONSTRAINT shadow_run_mode_known CHECK (mode IN ('DUAL_DETERMINATION', 'HISTORICAL_REPLAY', 'LIVE_SHADOW', 'RELEASE_DIFFERENTIAL')),
  CONSTRAINT shadow_run_window CHECK (window_to > window_from),
  CONSTRAINT shadow_run_currency_shape CHECK (currency ~ '^[A-Z]{3}$'),
  CONSTRAINT shadow_run_policy CHECK (tolerance >= 0 AND materiality >= 0),
  CONSTRAINT shadow_run_counts CHECK (cases >= 0 AND paired <= cases AND material <= cases
    AND exact_matches + equivalent_matches + monetary_variances + category_variances + population_variances + uncomparable = cases)
);
CREATE INDEX shadow_run_created ON ztax.shadow_run (tenant_id, created_at DESC);

CREATE TABLE ztax.shadow_case (
  tenant_id          uuid        NOT NULL,
  run_id             uuid        NOT NULL,
  seq                integer     NOT NULL,
  case_ref           text        NOT NULL,
  decision_id        uuid        NULL,
  pairing            text        NOT NULL,
  state              text        NOT NULL,
  comparator_total   numeric     NULL,
  zoiko_total        numeric     NULL,
  variance           numeric     NULL,
  material           boolean     NOT NULL,
  components         jsonb       NOT NULL,
  PRIMARY KEY (tenant_id, run_id, seq),
  FOREIGN KEY (tenant_id, run_id) REFERENCES ztax.shadow_run (tenant_id, run_id),
  CONSTRAINT shadow_case_pairing_known CHECK (pairing IN ('EXACT_ID', 'UNPAIRED_COMPARATOR', 'UNPAIRED_ZOIKO')),
  CONSTRAINT shadow_case_state_known CHECK (state IN ('EXACT_MATCH', 'EQUIVALENT_MATCH', 'MONETARY_VARIANCE',
    'CATEGORY_VARIANCE', 'POPULATION_VARIANCE', 'UNCOMPARABLE')),
  -- An unpaired record is a population difference, never an amount one.
  CONSTRAINT shadow_case_unpaired CHECK ((pairing = 'EXACT_ID') OR state = 'POPULATION_VARIANCE'),
  CONSTRAINT shadow_case_ref_length CHECK (length(case_ref) BETWEEN 1 AND 200)
);

CREATE TABLE ztax.shadow_discrepancy (
  tenant_id          uuid        NOT NULL,
  discrepancy_id     uuid        NOT NULL,
  run_id             uuid        NOT NULL,
  case_seq           integer     NOT NULL,
  PRIMARY KEY (tenant_id, discrepancy_id),
  FOREIGN KEY (tenant_id, run_id, case_seq) REFERENCES ztax.shadow_case (tenant_id, run_id, seq)
);
CREATE UNIQUE INDEX shadow_discrepancy_one_per_case ON ztax.shadow_discrepancy (tenant_id, run_id, case_seq);

CREATE TABLE ztax.shadow_discrepancy_event (
  tenant_id          uuid        NOT NULL,
  discrepancy_id     uuid        NOT NULL,
  seq                integer     NOT NULL,
  state              text        NOT NULL,
  severity           text        NULL,
  root_cause         text        NULL,
  note               text        NOT NULL,
  recorded_at        timestamptz NOT NULL,
  recorded_by        uuid        NOT NULL,
  PRIMARY KEY (tenant_id, discrepancy_id, seq),
  FOREIGN KEY (tenant_id, discrepancy_id) REFERENCES ztax.shadow_discrepancy (tenant_id, discrepancy_id),
  CONSTRAINT shadow_discrepancy_event_new_first CHECK ((seq = 1) <= (state = 'NEW')),
  CONSTRAINT shadow_discrepancy_event_state_known CHECK (state IN ('NEW', 'TRIAGED', 'DATA_REQUIRED', 'ANALYSIS',
    'SME_REVIEW', 'COMPARATOR_QUERY', 'ZOIKO_DEFECT_CONFIRMED', 'COMPARATOR_ISSUE_CONFIRMED',
    'EXPECTED_DIFFERENCE_APPROVED', 'LEGAL_AMBIGUITY', 'FIX_PENDING', 'RETEST_REQUIRED', 'RESOLVED', 'ACCEPTED_RISK', 'CLOSED')),
  CONSTRAINT shadow_discrepancy_event_severity_known CHECK (severity IS NULL
    OR severity IN ('SHD-SEV-1', 'SHD-SEV-2', 'SHD-SEV-3', 'SHD-SEV-4')),
  CONSTRAINT shadow_discrepancy_event_note_length CHECK (length(note) BETWEEN 1 AND 2000)
);

REVOKE UPDATE, DELETE, TRUNCATE ON ztax.shadow_comparator FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.shadow_run FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.shadow_case FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.shadow_discrepancy FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.shadow_discrepancy_event FROM ztax_app;

ALTER TABLE ztax.shadow_comparator ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.shadow_comparator
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.shadow_run ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.shadow_run
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.shadow_case ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.shadow_case
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.shadow_discrepancy ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.shadow_discrepancy
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.shadow_discrepancy_event ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.shadow_discrepancy_event
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

COMMENT ON TABLE ztax.shadow_run IS
  'One executed comparison against a comparator: policy, input hash and metrics. Immutable; non-impact.';
COMMENT ON TABLE ztax.shadow_case IS
  'One compared transaction, with its component comparison. Written with its run.';
COMMENT ON TABLE ztax.shadow_discrepancy_event IS
  'A material discrepancy''s investigation, in order. Append-only.';
