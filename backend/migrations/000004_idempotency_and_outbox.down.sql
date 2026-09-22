-- Local development only. ADR-0008 §2.5 makes production forward-only: a down
-- migration is never executed against a cell holding fiscal data, and rollback
-- of a schema change there is a new forward migration.
DROP TABLE IF EXISTS ztax.outbox;
DROP TABLE IF EXISTS ztax.idempotency_record;
