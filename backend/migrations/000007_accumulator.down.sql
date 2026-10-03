-- Local development only. See 000004's header: production is forward-only.

ALTER TABLE ztax.threshold_crossing
  DROP CONSTRAINT IF EXISTS threshold_crossing_contribution_fk,
  DROP CONSTRAINT IF EXISTS threshold_crossing_id_shape,
  DROP CONSTRAINT IF EXISTS threshold_crossing_currency_shape,
  DROP CONSTRAINT IF EXISTS threshold_crossing_seq_positive,
  DROP CONSTRAINT IF EXISTS threshold_crossing_comparison_known,
  DROP COLUMN IF EXISTS comparison,
  DROP COLUMN IF EXISTS threshold_amount;

-- Restore 000003's whole-row grant.
REVOKE UPDATE ON ztax.accumulator_snapshot FROM ztax_app;
GRANT UPDATE ON ztax.accumulator_snapshot TO ztax_app;

-- A snapshot that was locked and never contributed to has no updated_at, and
-- NOT NULL cannot come back while it exists. Snapshots are derived state
-- (ADR-0004 §2.5) and an empty one carries nothing at all, so it is removed
-- rather than given an invented time.
DELETE FROM ztax.accumulator_snapshot WHERE updated_at IS NULL;

ALTER TABLE ztax.accumulator_snapshot
  DROP CONSTRAINT IF EXISTS accumulator_snapshot_crossed_is_array,
  DROP CONSTRAINT IF EXISTS accumulator_snapshot_currency_shape,
  DROP CONSTRAINT IF EXISTS accumulator_snapshot_key_shape,
  DROP CONSTRAINT IF EXISTS accumulator_snapshot_empty_has_no_time,
  DROP CONSTRAINT IF EXISTS accumulator_snapshot_seq_non_negative,
  ALTER COLUMN updated_at SET NOT NULL;

ALTER TABLE ztax.contribution_event
  DROP CONSTRAINT IF EXISTS contribution_event_source_decision_fk,
  DROP CONSTRAINT IF EXISTS contribution_event_currency_shape,
  DROP CONSTRAINT IF EXISTS contribution_event_key_shape,
  DROP CONSTRAINT IF EXISTS contribution_event_seq_positive;
