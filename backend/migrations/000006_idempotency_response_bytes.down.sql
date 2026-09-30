-- Local development only. See 000004's header: production is forward-only.
ALTER TABLE ztax.idempotency_record
  DROP CONSTRAINT IF EXISTS idempotency_settled_has_body,
  DROP CONSTRAINT IF EXISTS idempotency_response_digest_shape;

ALTER TABLE ztax.idempotency_record DROP COLUMN IF EXISTS response_body;
ALTER TABLE ztax.idempotency_record ADD COLUMN response_body jsonb NULL;
