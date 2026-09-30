-- The idempotency response is stored as bytes, not jsonb (ADR-0013 §2.4).
--
-- A replay returns the stored response verbatim. jsonb cannot hold a response
-- verbatim: it reorders object keys, drops insignificant whitespace and keeps
-- the last of a duplicated key, so the bytes read back are not the bytes a
-- client was first given. For a fiscal amount the difference is not cosmetic —
-- a retry and its original must be the same document, or a client comparing
-- them sees two answers to one request.
--
-- No row has been written to idempotency_record before this migration: nothing
-- in the cell wrote one. The column is replaced rather than converted, and the
-- digest of the stored bytes is checked on every replay, so a body that no
-- longer matches its digest is an integrity failure rather than a response.

ALTER TABLE ztax.idempotency_record DROP COLUMN response_body;
ALTER TABLE ztax.idempotency_record ADD COLUMN response_body bytea NULL;

ALTER TABLE ztax.idempotency_record
  ADD CONSTRAINT idempotency_response_digest_shape CHECK (response_digest IS NULL OR response_digest LIKE 'zt1:%'),
  -- A settled record replays its body, so it must carry one and the digest
  -- that verifies it.
  ADD CONSTRAINT idempotency_settled_has_body CHECK (
    state = 'PENDING' OR (response_body IS NOT NULL AND response_digest IS NOT NULL));
