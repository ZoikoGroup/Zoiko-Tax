-- Decision evidence and period seals — the W1 exit gate's "one decision type
-- replays exactly" and the period-seal prototype (ADR-0003, ADR-0011).
--
-- A decision is two canonical evidence objects in the regional evidence store:
-- the envelope (what it was allowed to depend on) and the result (what it
-- concluded, with the trace). tax_decision is the index that names them by
-- digest. The objects are authoritative; these rows are checked against them on
-- every replay rather than believed.

-- ---------------------------------------------------------------------------
-- tax_decision — the evidence it names
-- ---------------------------------------------------------------------------
-- No row has ever been written to tax_decision: nothing in the cell wrote one
-- before this migration. NOT NULL can therefore be added without a default,
-- and a default would be worse than useless here — a made-up digest is a
-- decision that names evidence which does not exist.
ALTER TABLE ztax.tax_decision
  ADD COLUMN envelope_digest text NOT NULL,
  ADD COLUMN result_digest   text NOT NULL,
  ADD CONSTRAINT tax_decision_envelope_digest_shape CHECK (envelope_digest LIKE 'zt1:%'),
  ADD CONSTRAINT tax_decision_result_digest_shape CHECK (result_digest LIKE 'zt1:%');

-- The summary figures are released from NOT NULL. A decision from the generic
-- evaluator emits whatever slots its content declares, and turning those into
-- one "total tax" and one "taxable base" needs the component and base
-- semantics of ZTAX-DET-001 §3–§5 — which W2 lane H implements. Computing them
-- here from slot names would be tax logic in Go (ZTAX-DET-001 §0.2). Until
-- then the figures live in the result evidence, where the replay reads them,
-- and these columns stay empty rather than wrong.
ALTER TABLE ztax.tax_decision
  ALTER COLUMN currency     DROP NOT NULL,
  ALTER COLUMN total_tax    DROP NOT NULL,
  ALTER COLUMN taxable_base DROP NOT NULL;

-- A correction supersedes a decision that exists, in the same tenant, and each
-- decision is superseded at most once. ZTAX-DET-REQ-0032 makes correction
-- supersession rather than modification; this makes the chain linear, so
-- "which version is current" has one answer without needing a closed range.
ALTER TABLE ztax.tax_decision
  ADD CONSTRAINT tax_decision_supersedes_fk
    FOREIGN KEY (tenant_id, supersedes_id) REFERENCES ztax.tax_decision (tenant_id, decision_id);
CREATE UNIQUE INDEX tax_decision_one_successor
  ON ztax.tax_decision (tenant_id, supersedes_id) WHERE supersedes_id IS NOT NULL;

-- The seal's leaf query: every decision a tenant recorded in a period, in
-- evidence.LeafOrder.
CREATE INDEX tax_decision_by_recorded_at
  ON ztax.tax_decision (tenant_id, recorded_at, decision_id);

-- ---------------------------------------------------------------------------
-- evidence_period_seal — ADR-0011 §2.4, §2.5
-- ---------------------------------------------------------------------------
-- The signed seal itself is an evidence object (seal_object_digest). This row
-- indexes it and carries the columns an overlap check and an auditor's listing
-- need. Append-only under the schema-wide default grant: a seal is never
-- amended, and a wrong seal is answered by a verification failure, not an edit.
CREATE TABLE ztax.evidence_period_seal (
  tenant_id          uuid        NOT NULL REFERENCES ztax.tenant (tenant_id),
  seal_id            uuid        NOT NULL,
  cell               text        NOT NULL,
  period_start       timestamptz NOT NULL,
  period_end         timestamptz NOT NULL,
  leaf_count         int         NOT NULL,
  merkle_root        text        NOT NULL,
  seal_object_digest text        NOT NULL,
  -- The key that signed it, recorded so a historical seal verifies against
  -- the key it was made with rather than the current one (ADR-0017 §2.7).
  key_id             text        NOT NULL,
  sealed_at          timestamptz NOT NULL,

  PRIMARY KEY (tenant_id, seal_id),
  CONSTRAINT evidence_period_seal_period_ordered CHECK (period_end > period_start),
  CONSTRAINT evidence_period_seal_sealed_after_close CHECK (sealed_at >= period_end),
  CONSTRAINT evidence_period_seal_leaf_count CHECK (leaf_count >= 0),
  CONSTRAINT evidence_period_seal_root_shape CHECK (merkle_root LIKE 'zt1:%'),
  CONSTRAINT evidence_period_seal_object_shape CHECK (seal_object_digest LIKE 'zt1:%')
);

-- Overlap between periods is checked by the sealer under a per-tenant
-- transaction lock (SealRepository.LockSealing). An exclusion constraint on
-- tstzrange would express it declaratively, and needs btree_gist for the
-- tenant equality — an extension this schema does not otherwise require. The
-- unique index below is the backstop for the one overlap a race could produce,
-- two sealers choosing the identical period.
CREATE UNIQUE INDEX evidence_period_seal_period_key
  ON ztax.evidence_period_seal (tenant_id, period_start);
CREATE INDEX evidence_period_seal_by_period
  ON ztax.evidence_period_seal (tenant_id, period_start, period_end);
