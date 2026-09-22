-- Idempotency (ADR-0013) and the transactional outbox (ADR-0014).
--
-- Both tables are written in the same transaction as the domain effect they
-- describe. That is the requirement that decided the service topology
-- (ADR-0009 §1.1): a crash between the fiscal write and either of these would
-- leave a committed fiscal document with no idempotency binding and no event,
-- and the retry would duplicate it.

-- ---------------------------------------------------------------------------
-- idempotency_record — ADR-0013 §2.2
-- ---------------------------------------------------------------------------
CREATE TABLE ztax.idempotency_record (
  tenant_id       uuid        NOT NULL REFERENCES ztax.tenant (tenant_id),
  endpoint        text        NOT NULL,
  idempotency_key text        NOT NULL,

  -- The ADR-0011 canonical digest of the request body, not a hash of raw
  -- bytes. Two byte-different but semantically identical retries — a reordered
  -- JSON object, a client library upgrade — must match, and raw-byte hashing
  -- would produce a false conflict precisely when a client is behaving
  -- correctly (ADR-0013 §2.3).
  request_digest  text        NOT NULL,

  state           text        NOT NULL,
  response_status int         NULL,
  response_body   jsonb       NULL,
  response_digest text        NULL,
  -- What the request created, so that "given this key, name the decision it
  -- produced" is a query rather than a JSON reparse (ADR-0013 §2.8).
  result_ref      uuid        NULL,

  created_at      timestamptz NOT NULL,
  completed_at    timestamptz NULL,
  expires_at      timestamptz NOT NULL,

  -- The composite primary key IS the concurrency control (ADR-0013 §2.5). The
  -- second concurrent request's insert fails on this constraint and takes the
  -- request-in-progress path; there is no window in which two requests both
  -- believe they are first, because the exclusion is a uniqueness constraint
  -- rather than a check-then-act.
  PRIMARY KEY (tenant_id, endpoint, idempotency_key),

  CONSTRAINT idempotency_state_known CHECK (state IN ('PENDING', 'SUCCEEDED', 'FAILED')),
  CONSTRAINT idempotency_digest_shape CHECK (request_digest LIKE 'zt1:%'),
  -- A settled record must carry what it settled on. A SUCCEEDED row with no
  -- status could not be replayed to the client, which is the whole purpose.
  CONSTRAINT idempotency_settled_is_complete CHECK (
    state = 'PENDING' OR (response_status IS NOT NULL AND completed_at IS NOT NULL))
);

CREATE INDEX idempotency_expiry_sweep ON ztax.idempotency_record (expires_at)
  WHERE state <> 'PENDING';

-- A record is inserted PENDING and completed in the same transaction, and a
-- transient failure deletes it so a retry is a genuine new attempt
-- (ADR-0013 §2.7). Both need more than INSERT.
GRANT UPDATE, DELETE ON ztax.idempotency_record TO ztax_app;

-- ---------------------------------------------------------------------------
-- outbox — ADR-0014 §2.1
-- ---------------------------------------------------------------------------
CREATE TABLE ztax.outbox (
  -- id is also the CloudEvents id and the consumer's deduplication key
  -- (ADR-0014 §2.3), so it is stable across redeliveries by construction.
  id            uuid        PRIMARY KEY,
  tenant_id     uuid        NOT NULL REFERENCES ztax.tenant (tenant_id),
  -- The partition key. Ordering is per aggregate_key and nothing stronger is
  -- claimed (ADR-0014 §2.7).
  aggregate_key text        NOT NULL,
  event_type    text        NOT NULL,
  schema_ref    text        NOT NULL,
  payload       jsonb       NOT NULL,
  -- The CloudEvents time is the transaction commit time, not the publish time
  -- (ADR-0014 §2.5).
  created_at    timestamptz NOT NULL,
  published_at  timestamptz NULL,
  -- Delivery bookkeeping. Not evidence, which is why this table may be
  -- updated: the event itself is immutable, and only the record of whether it
  -- has left changes.
  attempts      int         NOT NULL DEFAULT 0,
  last_error    text        NULL
);

-- The relay's claim query: unpublished rows, oldest first, taken with
-- FOR UPDATE SKIP LOCKED so relay instances run concurrently without
-- coordinating and without blocking each other (ADR-0014 §2.2).
CREATE INDEX outbox_unpublished ON ztax.outbox (created_at)
  WHERE published_at IS NULL;

CREATE INDEX outbox_by_aggregate ON ztax.outbox (tenant_id, aggregate_key, created_at);

GRANT UPDATE (published_at, attempts, last_error) ON ztax.outbox TO ztax_app;

COMMENT ON TABLE ztax.outbox IS
  'Transactional outbox. The only path by which an event leaves the cell (ADR-0014 §2.1). Delivery is at-least-once; id is the consumer dedup key.';
