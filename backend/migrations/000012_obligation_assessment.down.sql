-- Local development only. See 000004's header: production is forward-only.
DROP TABLE IF EXISTS ztax.obligation_contribution;
DROP INDEX IF EXISTS ztax.obligation_superseded_once;
DROP INDEX IF EXISTS ztax.obligation_one_root;
ALTER TABLE ztax.obligation
  DROP CONSTRAINT IF EXISTS obligation_bundle_digest_shape,
  DROP CONSTRAINT IF EXISTS obligation_supersedes_fk,
  DROP CONSTRAINT IF EXISTS obligation_legal_entity_fk,
  DROP COLUMN IF EXISTS recorded_by,
  DROP COLUMN IF EXISTS reason_code,
  DROP COLUMN IF EXISTS legal_timezone,
  DROP COLUMN IF EXISTS duty,
  DROP COLUMN IF EXISTS bundle_digest,
  DROP COLUMN IF EXISTS bundle_id,
  DROP COLUMN IF EXISTS definition_version,
  DROP COLUMN IF EXISTS definition_id,
  DROP COLUMN IF EXISTS authority,
  DROP COLUMN IF EXISTS legal_entity_id;
