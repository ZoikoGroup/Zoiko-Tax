-- Local development only. See 000004's header: production is forward-only.
DROP TABLE IF EXISTS ztax.evidence_period_seal;

DROP INDEX IF EXISTS ztax.tax_decision_by_recorded_at;
DROP INDEX IF EXISTS ztax.tax_decision_one_successor;
ALTER TABLE ztax.tax_decision DROP CONSTRAINT IF EXISTS tax_decision_supersedes_fk;

-- Restoring NOT NULL on the summary figures would fail on any row written after
-- 000005, and a down migration that fails on the data it was written for is not
-- a rollback. Local databases with such rows are recreated instead.
ALTER TABLE ztax.tax_decision
  DROP CONSTRAINT IF EXISTS tax_decision_envelope_digest_shape,
  DROP CONSTRAINT IF EXISTS tax_decision_result_digest_shape,
  DROP COLUMN IF EXISTS envelope_digest,
  DROP COLUMN IF EXISTS result_digest;
