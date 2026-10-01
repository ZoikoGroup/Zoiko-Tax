-- The accumulator concurrency pattern (ADR-0004), made enforceable.
--
-- 000003 created the three tables in the shape ADR-0004 §2.1 sketches. That
-- shape was enough to reserve the names; it was not enough to hold the
-- guarantees the ADR makes, and this migration closes the distance:
--
--   * the snapshot row has to exist before it can be locked, and it is
--     created by the locking path before any contribution is known, so its
--     "last contributed at" column cannot be NOT NULL (see below);
--   * a threshold crossing has to name the threshold it crossed — the limit
--     and whether the limit itself counts — because ADR-0004 §7.1 leaves that
--     comparison to content, and a crossing row that does not record which
--     semantics fired cannot be audited against the content that produced it;
--   * the append-only tables are made explicitly so rather than by omission,
--     and the snapshot's mutability is narrowed to the columns the write path
--     actually changes.
--
-- No row has been written to any of the three tables before this migration:
-- nothing in the cell wrote one (the commit path is wired in W2 lane I). The
-- NOT NULL columns below are therefore added without defaults, on the same
-- reasoning as 000005 — a default would be a made-up threshold limit on a
-- crossing that never happened.

-- ---------------------------------------------------------------------------
-- contribution_event — ADR-0004 §2.1, the truth
-- ---------------------------------------------------------------------------
ALTER TABLE ztax.contribution_event
  -- seq is dense per key and starts at 1, so that last_seq = 0 means "nothing
  -- contributed" and SUM over seq <= last_seq is the whole log (ADR-0004
  -- §2.5). A zero or negative seq is a row the reconciliation would silently
  -- exclude.
  ADD CONSTRAINT contribution_event_seq_positive CHECK (seq >= 1),
  -- The key grammar is an open question (ADR-0004 §7.2, blocked on OBL-001).
  -- Until it lands the key is opaque, and this is the only shape asserted:
  -- printable ASCII, no whitespace, bounded. ASCII is load-bearing rather than
  -- tidy — it makes Go's byte order, the "C" collation and every other
  -- collation agree on how keys sort, so the canonical lock order of §2.3 does
  -- not depend on a database setting. Mirrors accumulator.ParseKey.
  ADD CONSTRAINT contribution_event_key_shape CHECK (accumulator_key ~ '^[!-~]{1,256}$'),
  ADD CONSTRAINT contribution_event_currency_shape CHECK (currency ~ '^[A-Z]{3}$'),
  -- A contribution is caused by a decision that exists, in the same tenant
  -- (ADR-0004 §2.1, "the fiscal decision that caused it"). Deferred to commit,
  -- because §2.2 writes the contribution before the decision row inside the
  -- one transaction, and the order of statements inside an atomic commit is
  -- not something the schema should have an opinion about. What it does have
  -- an opinion about is that no transaction commits a contribution from a
  -- decision nobody recorded.
  ADD CONSTRAINT contribution_event_source_decision_fk
    FOREIGN KEY (tenant_id, source_decision_id)
    REFERENCES ztax.tax_decision (tenant_id, decision_id)
    DEFERRABLE INITIALLY DEFERRED;

-- Append-only, stated rather than implied. The schema-wide default grant of
-- 000001 already withholds UPDATE and DELETE; this makes the intent survive a
-- future default change, and it is the grant the integration test asserts.
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.contribution_event FROM ztax_app;

COMMENT ON TABLE ztax.contribution_event IS
  'Accumulator event log (ADR-0004 §2.1). Append-only; the truth from which accumulator_snapshot is derived. UNIQUE (tenant_id, accumulator_key, source_decision_id) is the inner idempotence layer (§2.4).';

-- ---------------------------------------------------------------------------
-- accumulator_snapshot — ADR-0004 §2.2, the one mutable exception
-- ---------------------------------------------------------------------------
-- updated_at is the recorded_at of the contribution at last_seq — decision
-- time from the envelope, never now() (ADR-0003 §2.5). A snapshot created by
-- the locking path before its first contribution has no such contribution, so
-- the column is NULL exactly when last_seq is 0. The alternative was to stamp
-- the creating transaction's clock on it, which would put a wall-clock reading
-- into a row a replay is supposed to be able to rebuild.
ALTER TABLE ztax.accumulator_snapshot
  ALTER COLUMN updated_at DROP NOT NULL,
  ADD CONSTRAINT accumulator_snapshot_seq_non_negative CHECK (last_seq >= 0),
  ADD CONSTRAINT accumulator_snapshot_empty_has_no_time CHECK ((last_seq = 0) = (updated_at IS NULL)),
  ADD CONSTRAINT accumulator_snapshot_key_shape CHECK (accumulator_key ~ '^[!-~]{1,256}$'),
  ADD CONSTRAINT accumulator_snapshot_currency_shape CHECK (currency ~ '^[A-Z]{3}$'),
  -- A sorted array of threshold ids. An object or a scalar here would be read
  -- as "nothing has fired", which is the direction that re-emits a crossing.
  ADD CONSTRAINT accumulator_snapshot_crossed_is_array CHECK (jsonb_typeof(crossed_thresholds) = 'array');

-- 000003 granted UPDATE on the whole row. The write path changes four
-- columns, and nothing changes the key, the tenant or the currency — a
-- snapshot that changed currency would be a running total of two different
-- things. Narrowing the grant makes that a privilege error rather than a
-- convention.
--
-- There is still no DELETE. A rebuild (ADR-0004 §2.5) recomputes the row in
-- place under the same lock as a commit; it never removes it, so there is no
-- window in which the key has no row to lock and two transactions could each
-- create one.
REVOKE UPDATE ON ztax.accumulator_snapshot FROM ztax_app;
REVOKE DELETE, TRUNCATE ON ztax.accumulator_snapshot FROM ztax_app;
GRANT UPDATE (running_total, last_seq, crossed_thresholds, updated_at)
  ON ztax.accumulator_snapshot TO ztax_app;

COMMENT ON TABLE ztax.accumulator_snapshot IS
  'Derived, transactionally exact cache of contribution_event (ADR-0004 §2.1). The one deliberate exception to ADR-0003 append-only (§2.2); rebuildable from the log (§2.5). Locked only through AccumulatorRepo.LockAll (§2.3, control 1).';

-- ---------------------------------------------------------------------------
-- threshold_crossing — ADR-0004 §2.6
-- ---------------------------------------------------------------------------
ALTER TABLE ztax.threshold_crossing
  -- The limit that was crossed and how it was compared, recorded as the
  -- crossing saw them. ADR-0004 §7.1 leaves ">= or >" to content
  -- (ZTAX-DET-001); the runtime expresses both, and the row says which one
  -- fired so the answer can be checked against the bundle that asked for it.
  ADD COLUMN threshold_amount numeric NOT NULL,
  ADD COLUMN comparison       text    NOT NULL,
  ADD CONSTRAINT threshold_crossing_comparison_known CHECK (comparison IN ('GTE', 'GT')),
  ADD CONSTRAINT threshold_crossing_seq_positive CHECK (crossed_at_seq >= 1),
  ADD CONSTRAINT threshold_crossing_currency_shape CHECK (currency ~ '^[A-Z]{3}$'),
  ADD CONSTRAINT threshold_crossing_id_shape CHECK (threshold_id ~ '^[!-~]{1,128}$'),
  -- The contribution that caused the crossing exists. Not deferred: §2.2
  -- writes the contribution before the crossing, and a crossing whose cause
  -- is not yet in the log is a crossing nobody can explain.
  ADD CONSTRAINT threshold_crossing_contribution_fk
    FOREIGN KEY (tenant_id, accumulator_key, crossed_at_seq)
    REFERENCES ztax.contribution_event (tenant_id, accumulator_key, seq);

-- The primary key (tenant_id, accumulator_key, threshold_id) from 000003 is
-- the database half of "crossed exactly once" (ADR-0004 §2.6): a second
-- crossing of one threshold on one key fails the insert, and with it the
-- transaction that would have announced it twice. crossed_thresholds on the
-- snapshot is the half that stops the application from trying.
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.threshold_crossing FROM ztax_app;

COMMENT ON TABLE ztax.threshold_crossing IS
  'Threshold crossings, one per (tenant, key, threshold), written with the outbox event in the transaction that caused them (ADR-0004 §2.6). Append-only.';
