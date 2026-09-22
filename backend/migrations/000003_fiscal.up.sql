-- The fiscal core: decisions, documents, lines, accumulators, obligations,
-- classifications and the subledger.
--
-- Every table here is evidence-bearing and therefore append-only (ADR-0003
-- §2.1). The single exception is accumulator_snapshot, which ADR-0004 §2.2
-- names explicitly: it holds derived state, no evidence, and is rebuildable
-- from contribution_event.
--
-- Every fiscal amount is NUMERIC, bound to apd.Decimal through the registered
-- codec in internal/adapter/postgres (ADR-0008 §2.4). No amount is ever
-- double precision, and the conformance test asserts that no driver path
-- narrows one.

-- ---------------------------------------------------------------------------
-- tax_decision — the record of one determination
-- ---------------------------------------------------------------------------
CREATE TABLE ztax.tax_decision (
  tenant_id       uuid        NOT NULL REFERENCES ztax.tenant (tenant_id),
  decision_id     uuid        NOT NULL,

  -- ADR-0003 §2.2's temporal columns. recorded_at is decision time from the
  -- request envelope, never now(): a replay supplies the historical instant and
  -- must get the historical answer (ADR-0003 §2.5).
  business_key    text        NOT NULL,
  valid_from      timestamptz NOT NULL,
  valid_to        timestamptz NULL,
  recorded_at     timestamptz NOT NULL,
  supersedes_id   uuid        NULL,

  event_time      timestamptz NOT NULL,

  -- The outcome. AUTHORITATIVE is the only value that may be filed, and
  -- ADR-0016 §2.1's refusals are decisions recorded here rather than errors
  -- that record nothing.
  outcome         text        NOT NULL,
  reason_code     text        NULL,

  -- What produced it. Replay needs all of this and nothing outside it
  -- (ADR-0011 §2.8) — which is what makes this column list the operational
  -- definition of determinism for the estate.
  bundle_id       text        NOT NULL,
  bundle_digest   text        NOT NULL,
  ir_version      int         NOT NULL,
  canon_profile   text        NOT NULL,
  train_app       text        NOT NULL,
  train_content   text        NOT NULL,
  train_ai        text        NOT NULL,
  train_adapter   text        NOT NULL,
  train_infra     text        NOT NULL,
  train_schema    text        NOT NULL,
  train_migration text        NOT NULL,

  -- The canonical input and its digest, and the execution trace (ADR-0005
  -- §2.7). The trace is evidence, retained under statutory retention, and is
  -- distinct from an OpenTelemetry trace.
  input_digest    text        NOT NULL,
  input_canonical jsonb       NOT NULL,
  trace           jsonb       NOT NULL,

  currency        text        NOT NULL,
  total_tax       numeric     NOT NULL,
  taxable_base    numeric     NOT NULL,

  PRIMARY KEY (tenant_id, decision_id),
  CONSTRAINT tax_decision_outcome_known CHECK (
    outcome IN ('AUTHORITATIVE', 'ADVISORY', 'AMBIGUOUS', 'CONFLICTED', 'UNSUPPORTED', 'REVIEW_REQUIRED')),
  CONSTRAINT tax_decision_digest_shape CHECK (input_digest LIKE 'zt1:%'),
  CONSTRAINT tax_decision_bundle_digest_shape CHECK (bundle_digest LIKE 'zt1:%')
);

-- The as-of read of ADR-0003 §2.3: DISTINCT ON (business_key) ordered by
-- recorded_at DESC, with tenant_id leading because every read is tenant-scoped.
CREATE INDEX tax_decision_as_of
  ON ztax.tax_decision (tenant_id, business_key, recorded_at DESC);

CREATE INDEX tax_decision_by_event_time
  ON ztax.tax_decision (tenant_id, event_time DESC);

CREATE UNIQUE INDEX tax_decision_id_key ON ztax.tax_decision (decision_id);

-- ---------------------------------------------------------------------------
-- fiscal_document and fiscal_line
-- ---------------------------------------------------------------------------
CREATE TABLE ztax.fiscal_document (
  tenant_id     uuid        NOT NULL REFERENCES ztax.tenant (tenant_id),
  document_id   uuid        NOT NULL,
  decision_id   uuid        NOT NULL,

  business_key  text        NOT NULL,
  valid_from    timestamptz NOT NULL,
  valid_to      timestamptz NULL,
  recorded_at   timestamptz NOT NULL,
  supersedes_id uuid        NULL,

  document_type text        NOT NULL,
  -- external_ref is ADR-0012 §2.5: never a key, never parsed, never assumed
  -- unique. Stored as three columns so nothing is tempted to concatenate them.
  ext_source_system text    NULL,
  ext_namespace     text    NULL,
  ext_value         text    NULL,

  currency      text        NOT NULL,
  net_total     numeric     NOT NULL,
  tax_total     numeric     NOT NULL,
  gross_total   numeric     NOT NULL,

  event_time    timestamptz NOT NULL,

  PRIMARY KEY (tenant_id, document_id),
  CONSTRAINT fiscal_document_type_known CHECK (
    document_type IN ('INVOICE', 'CREDIT_NOTE', 'ADJUSTMENT', 'REFUND'))
);

CREATE INDEX fiscal_document_as_of
  ON ztax.fiscal_document (tenant_id, business_key, recorded_at DESC);

-- ADR-0012 §2.5: uniqueness on an external reference, where required, is a
-- tenant-scoped constraint on the triple. A collision is a data-quality finding
-- rather than a crash, so this is an index the application consults, not a
-- constraint that aborts a transaction.
CREATE INDEX fiscal_document_by_external_ref
  ON ztax.fiscal_document (tenant_id, ext_source_system, ext_namespace, ext_value)
  WHERE ext_value IS NOT NULL;

CREATE TABLE ztax.fiscal_line (
  tenant_id     uuid        NOT NULL,
  line_id       uuid        NOT NULL,
  document_id   uuid        NOT NULL,
  -- ordinal is part of the canonical input and is what allocation ties break
  -- on (ADR-0002 §2.6). It is dense and starts at zero.
  ordinal       int         NOT NULL,

  item_ref      text        NULL,
  classification_id uuid    NULL,

  quantity      numeric     NOT NULL,
  unit          text        NOT NULL,
  currency      text        NOT NULL,
  net_amount    numeric     NOT NULL,
  tax_amount    numeric     NOT NULL,

  -- The rounding policy that produced tax_amount, recorded by name
  -- (ADR-0002 §5.1 control 3). A decision that cannot name its rounding does
  -- not replay, so these are NOT NULL.
  rounding_mode  text       NOT NULL,
  rounding_scale int        NOT NULL,
  rounding_basis text       NOT NULL,

  PRIMARY KEY (tenant_id, line_id),
  FOREIGN KEY (tenant_id, document_id) REFERENCES ztax.fiscal_document (tenant_id, document_id),
  CONSTRAINT fiscal_line_rounding_mode_known CHECK (
    rounding_mode IN ('HALF_UP', 'HALF_EVEN', 'HALF_DOWN', 'DOWN', 'UP', 'CEILING', 'FLOOR')),
  CONSTRAINT fiscal_line_rounding_basis_known CHECK (
    rounding_basis IN ('LINE', 'DOCUMENT', 'TAX_COMPONENT', 'JURISDICTION_TOTAL')),
  CONSTRAINT fiscal_line_rounding_scale_range CHECK (rounding_scale BETWEEN 0 AND 12),
  CONSTRAINT fiscal_line_ordinal_non_negative CHECK (ordinal >= 0)
);

CREATE UNIQUE INDEX fiscal_line_ordinal_key
  ON ztax.fiscal_line (tenant_id, document_id, ordinal);

-- ---------------------------------------------------------------------------
-- classification
-- ---------------------------------------------------------------------------
CREATE TABLE ztax.classification (
  tenant_id        uuid        NOT NULL REFERENCES ztax.tenant (tenant_id),
  classification_id uuid       NOT NULL,

  business_key     text        NOT NULL,
  valid_from       timestamptz NOT NULL,
  valid_to         timestamptz NULL,
  recorded_at      timestamptz NOT NULL,
  supersedes_id    uuid        NULL,

  item_ref         text        NOT NULL,
  -- ontology_id is a content identifier: a stable, human-meaningful string
  -- under a registered grammar, not a UUID (ADR-0012 §2.4).
  ontology_id      text        NULL,
  outcome          text        NOT NULL,
  reason_code      text        NULL,

  -- ADR-0006 §2.6 and ADR-0019 C2: an AI suggestion and a determined
  -- classification are structurally different things. source records which this
  -- is, and an AI-sourced classification may not reach an authoritative path
  -- without a human confirmation recorded in confirmed_by.
  source           text        NOT NULL,
  confidence       numeric     NULL,
  confirmed_by     uuid        NULL,
  confirmed_at     timestamptz NULL,

  PRIMARY KEY (tenant_id, classification_id),
  CONSTRAINT classification_outcome_known CHECK (
    outcome IN ('CLASSIFIED', 'AMBIGUOUS', 'UNSUPPORTED', 'REVIEW_REQUIRED')),
  CONSTRAINT classification_source_known CHECK (
    source IN ('RULE', 'AI_SUGGESTED', 'HUMAN')),
  -- An AI-sourced classification that claims to be confirmed must name who
  -- confirmed it. The database will not let that pair come apart.
  CONSTRAINT classification_confirmation_complete CHECK (
    (confirmed_by IS NULL) = (confirmed_at IS NULL))
);

CREATE INDEX classification_as_of
  ON ztax.classification (tenant_id, business_key, recorded_at DESC);
CREATE INDEX classification_by_item
  ON ztax.classification (tenant_id, item_ref, recorded_at DESC);

-- ---------------------------------------------------------------------------
-- jurisdiction — resolved geography
-- ---------------------------------------------------------------------------
CREATE TABLE ztax.jurisdiction (
  -- Jurisdictions are reference content, not tenant data: the boundary of
  -- California is the same fact for every tenant in the cell. There is
  -- therefore no tenant_id here, and this table is the one read that is not
  -- tenant-scoped. It holds no customer data, which is what makes that safe.
  jurisdiction_id text        PRIMARY KEY,
  parent_id       text        NULL REFERENCES ztax.jurisdiction (jurisdiction_id),
  level           text        NOT NULL,
  name            text        NOT NULL,
  country_code    text        NOT NULL,
  -- The civil timezone, kept beside the boundary because ADR-0003 §2.6 resolves
  -- a legal effective date against it.
  timezone        text        NOT NULL,
  boundary        geography(MULTIPOLYGON, 4326) NULL,
  effective_from  date        NOT NULL,
  effective_to    date        NULL,
  CONSTRAINT jurisdiction_level_known CHECK (
    level IN ('COUNTRY', 'STATE', 'COUNTY', 'CITY', 'DISTRICT', 'SPECIAL'))
);

CREATE INDEX jurisdiction_boundary_gix ON ztax.jurisdiction USING GIST (boundary);
CREATE INDEX jurisdiction_by_country ON ztax.jurisdiction (country_code, level);

-- ---------------------------------------------------------------------------
-- accumulator — ADR-0004
-- ---------------------------------------------------------------------------
CREATE TABLE ztax.contribution_event (
  tenant_id          uuid        NOT NULL REFERENCES ztax.tenant (tenant_id),
  accumulator_key    text        NOT NULL,
  seq                bigint      NOT NULL,
  source_decision_id uuid        NOT NULL,
  amount             numeric     NOT NULL,
  currency           text        NOT NULL,
  event_time         timestamptz NOT NULL,
  recorded_at        timestamptz NOT NULL,
  PRIMARY KEY (tenant_id, accumulator_key, seq),
  -- ADR-0004 §2.4: the inner idempotence layer. One decision contributes to one
  -- accumulator at most once, whatever happens above.
  CONSTRAINT contribution_event_once_per_decision
    UNIQUE (tenant_id, accumulator_key, source_decision_id)
);

CREATE TABLE ztax.accumulator_snapshot (
  tenant_id          uuid        NOT NULL REFERENCES ztax.tenant (tenant_id),
  accumulator_key    text        NOT NULL,
  running_total      numeric     NOT NULL,
  currency           text        NOT NULL,
  last_seq           bigint      NOT NULL,
  crossed_thresholds jsonb       NOT NULL DEFAULT '[]'::jsonb,
  updated_at         timestamptz NOT NULL,
  PRIMARY KEY (tenant_id, accumulator_key)
);

CREATE TABLE ztax.threshold_crossing (
  tenant_id          uuid        NOT NULL REFERENCES ztax.tenant (tenant_id),
  accumulator_key    text        NOT NULL,
  threshold_id       text        NOT NULL,
  crossed_at_seq     bigint      NOT NULL,
  source_decision_id uuid        NOT NULL,
  running_total      numeric     NOT NULL,
  currency           text        NOT NULL,
  recorded_at        timestamptz NOT NULL,
  PRIMARY KEY (tenant_id, accumulator_key, threshold_id)
);

-- ADR-0004 §2.2 and ADR-0003's carve-out: the snapshot is derived state and is
-- the one fiscal-adjacent table that may be updated in place.
GRANT UPDATE ON ztax.accumulator_snapshot TO ztax_app;

-- ---------------------------------------------------------------------------
-- obligation
-- ---------------------------------------------------------------------------
CREATE TABLE ztax.obligation (
  tenant_id      uuid        NOT NULL REFERENCES ztax.tenant (tenant_id),
  obligation_id  uuid        NOT NULL,

  business_key   text        NOT NULL,
  valid_from     timestamptz NOT NULL,
  valid_to       timestamptz NULL,
  recorded_at    timestamptz NOT NULL,
  supersedes_id  uuid        NULL,

  jurisdiction_id text       NOT NULL,
  obligation_type text       NOT NULL,
  period_start   date        NOT NULL,
  period_end     date        NOT NULL,
  due_date       date        NOT NULL,
  status         text        NOT NULL,

  currency       text        NOT NULL,
  assessed_amount numeric    NULL,

  PRIMARY KEY (tenant_id, obligation_id),
  CONSTRAINT obligation_status_known CHECK (
    status IN ('OPEN', 'READY', 'FILED', 'ACCEPTED', 'REJECTED', 'UNCERTAIN', 'CLOSED')),
  CONSTRAINT obligation_period_ordered CHECK (period_end >= period_start)
);

CREATE INDEX obligation_as_of ON ztax.obligation (tenant_id, business_key, recorded_at DESC);
CREATE INDEX obligation_by_due ON ztax.obligation (tenant_id, status, due_date);

-- ---------------------------------------------------------------------------
-- submission_attempt — the authority boundary
-- ---------------------------------------------------------------------------
-- ADR-0016 §2.2: UNCERTAIN_SUBMISSION is a state of this record, never a 5xx.
-- A 5xx invites a retry, and a retry on an uncertain submission is the Critical
-- risk in the register.
CREATE TABLE ztax.submission_attempt (
  tenant_id      uuid        NOT NULL REFERENCES ztax.tenant (tenant_id),
  attempt_id     uuid        NOT NULL,
  obligation_id  uuid        NOT NULL,
  adapter_id     text        NOT NULL,
  state          text        NOT NULL,
  -- The authority's own reference, verbatim and never parsed (ADR-0012 §2.5).
  authority_ref  text        NULL,
  request_digest text        NOT NULL,
  started_at     timestamptz NOT NULL,
  settled_at     timestamptz NULL,
  reason_code    text        NULL,
  PRIMARY KEY (tenant_id, attempt_id),
  FOREIGN KEY (tenant_id, obligation_id) REFERENCES ztax.obligation (tenant_id, obligation_id),
  CONSTRAINT submission_attempt_state_known CHECK (
    state IN ('PREPARED', 'IN_FLIGHT', 'ACCEPTED', 'REJECTED', 'UNCERTAIN_SUBMISSION'))
);

CREATE INDEX submission_attempt_by_obligation
  ON ztax.submission_attempt (tenant_id, obligation_id, started_at DESC);

-- An attempt moves through its states in place; the evidence of each transition
-- is the outbox event it emitted. This is a deliberate exception and it is
-- narrow: only the settlement columns may change.
GRANT UPDATE (state, authority_ref, settled_at, reason_code) ON ztax.submission_attempt TO ztax_app;

-- ---------------------------------------------------------------------------
-- ledger_entry — the fiscal subledger
-- ---------------------------------------------------------------------------
-- Double-entry, append-only. Nothing here is ever corrected in place: a
-- correction is a reversing entry plus a new one, which is what an accountant
-- expects and what ADR-0003 §2.1 requires anyway.
CREATE TABLE ztax.ledger_entry (
  tenant_id     uuid        NOT NULL REFERENCES ztax.tenant (tenant_id),
  entry_id      uuid        NOT NULL,
  -- Entries are grouped into balanced transactions. Every entry in one
  -- transaction_key must sum to zero, asserted by the application inside the
  -- transaction that writes them.
  transaction_key text      NOT NULL,
  source_decision_id uuid   NULL,
  account       text        NOT NULL,
  jurisdiction_id text      NULL,
  currency      text        NOT NULL,
  -- Signed: positive is a debit, negative a credit. One signed column rather
  -- than two unsigned ones, so that "does this transaction balance" is a SUM
  -- rather than a difference of two SUMs.
  amount        numeric     NOT NULL,
  event_time    timestamptz NOT NULL,
  recorded_at   timestamptz NOT NULL,
  reverses_id   uuid        NULL,
  PRIMARY KEY (tenant_id, entry_id)
);

CREATE INDEX ledger_entry_by_transaction ON ztax.ledger_entry (tenant_id, transaction_key);
CREATE INDEX ledger_entry_by_account ON ztax.ledger_entry (tenant_id, account, event_time DESC);
