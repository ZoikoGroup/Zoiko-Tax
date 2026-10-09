-- Webhooks (W2 lane K): subscriptions, their signing secrets, and the
-- delivery of outbox events to them.
--
-- The subscription, its status history and its secrets are append-only, like
-- every record here that says who decided what. A delivery is different: it
-- is bookkeeping about getting an event to a receiver, as ztax.outbox's
-- published_at is, so its status columns may be updated — and every attempt
-- it makes is a row of its own in webhook_attempt, which may not.

CREATE TABLE ztax.webhook_subscription (
  tenant_id        uuid        NOT NULL REFERENCES ztax.tenant (tenant_id),
  webhook_id       uuid        NOT NULL,
  url              text        NOT NULL,
  -- Sorted and distinct; never empty. There is no wildcard subscription.
  event_types      text[]      NOT NULL,
  description      text        NULL,
  created_at       timestamptz NOT NULL,
  created_by       uuid        NULL,
  PRIMARY KEY (tenant_id, webhook_id),
  CONSTRAINT webhook_subscription_url_length CHECK (length(url) BETWEEN 8 AND 2048),
  CONSTRAINT webhook_subscription_event_types_present CHECK (cardinality(event_types) >= 1),
  CONSTRAINT webhook_subscription_description_length CHECK (description IS NULL OR length(description) <= 500)
);

CREATE TABLE ztax.webhook_status (
  tenant_id    uuid        NOT NULL,
  webhook_id   uuid        NOT NULL,
  -- 1 is the ACTIVE status the subscription is created with. The key refuses
  -- a second writer acting on the history it read.
  seq          integer     NOT NULL,
  status       text        NOT NULL,
  recorded_at  timestamptz NOT NULL,
  recorded_by  uuid        NULL,
  PRIMARY KEY (tenant_id, webhook_id, seq),
  FOREIGN KEY (tenant_id, webhook_id) REFERENCES ztax.webhook_subscription (tenant_id, webhook_id),
  CONSTRAINT webhook_status_known CHECK (status IN ('ACTIVE', 'PAUSED', 'DISABLED')),
  CONSTRAINT webhook_status_seq_positive CHECK (seq >= 1),
  -- A subscription is created ACTIVE.
  CONSTRAINT webhook_status_first_is_active CHECK (seq > 1 OR status = 'ACTIVE')
);

CREATE TABLE ztax.webhook_secret (
  tenant_id            uuid        NOT NULL,
  webhook_id           uuid        NOT NULL,
  version              integer     NOT NULL,
  -- AES-256-GCM under the cell's webhook key, bound to tenant, webhook and
  -- version (internal/platform/secretbox). Never the secret in clear.
  sealed_secret        bytea       NOT NULL,
  created_at           timestamptz NOT NULL,
  created_by           uuid        NULL,
  -- When the version before this one stops signing. A rotation ends the old
  -- secret by saying so here, not by editing the old row.
  retires_previous_at  timestamptz NULL,
  PRIMARY KEY (tenant_id, webhook_id, version),
  FOREIGN KEY (tenant_id, webhook_id) REFERENCES ztax.webhook_subscription (tenant_id, webhook_id),
  CONSTRAINT webhook_secret_version_positive CHECK (version >= 1),
  CONSTRAINT webhook_secret_retirement CHECK ((version = 1) = (retires_previous_at IS NULL))
);

CREATE TABLE ztax.webhook_delivery (
  tenant_id        uuid        NOT NULL,
  delivery_id      uuid        NOT NULL,
  webhook_id       uuid        NOT NULL,
  -- The outbox row, and so the CloudEvents id: the receiver's dedup key.
  event_id         uuid        NOT NULL,
  event_type       text        NOT NULL,
  -- The exact bytes sent, rendered once at fan-out, so every attempt and
  -- every replay sends what the first attempt sent.
  body             bytea       NOT NULL,
  status           text        NOT NULL,
  attempts         integer     NOT NULL DEFAULT 0,
  next_attempt_at  timestamptz NULL,
  created_at       timestamptz NOT NULL,
  delivered_at     timestamptz NULL,
  -- A replay is a new delivery of the same event naming the one it replays.
  replay_of        uuid        NULL,
  replayed_by      uuid        NULL,
  PRIMARY KEY (tenant_id, delivery_id),
  FOREIGN KEY (tenant_id, webhook_id) REFERENCES ztax.webhook_subscription (tenant_id, webhook_id),
  FOREIGN KEY (event_id) REFERENCES ztax.outbox (id),
  FOREIGN KEY (tenant_id, replay_of) REFERENCES ztax.webhook_delivery (tenant_id, delivery_id),
  CONSTRAINT webhook_delivery_status_known CHECK (status IN ('PENDING', 'DELIVERED', 'DEAD')),
  CONSTRAINT webhook_delivery_pending_is_scheduled CHECK ((status = 'PENDING') = (next_attempt_at IS NOT NULL)),
  CONSTRAINT webhook_delivery_delivered_has_time CHECK ((status = 'DELIVERED') = (delivered_at IS NOT NULL)),
  CONSTRAINT webhook_delivery_attempts_bounded CHECK (attempts >= 0)
);

-- Fan-out is idempotent: a relay that crashes after fanning an event out and
-- before marking it published fans it out again, and this refuses the second
-- copy. Replays are exempt; they are deliberate second copies.
CREATE UNIQUE INDEX webhook_delivery_once ON ztax.webhook_delivery (tenant_id, webhook_id, event_id)
  WHERE replay_of IS NULL;

-- The dispatcher's claim: due deliveries, oldest first, FOR UPDATE SKIP
-- LOCKED, as the outbox relay claims its rows.
CREATE INDEX webhook_delivery_due ON ztax.webhook_delivery (next_attempt_at) WHERE status = 'PENDING';
CREATE INDEX webhook_delivery_by_webhook ON ztax.webhook_delivery (tenant_id, webhook_id, created_at);

CREATE TABLE ztax.webhook_attempt (
  tenant_id     uuid        NOT NULL,
  delivery_id   uuid        NOT NULL,
  attempt       integer     NOT NULL,
  started_at    timestamptz NOT NULL,
  duration_ms   integer     NOT NULL,
  -- NULL when no response arrived: refused, timed out, or forbidden by the
  -- egress guard before a connection was made.
  status_code   integer     NULL,
  error         text        NULL,
  PRIMARY KEY (tenant_id, delivery_id, attempt),
  FOREIGN KEY (tenant_id, delivery_id) REFERENCES ztax.webhook_delivery (tenant_id, delivery_id),
  CONSTRAINT webhook_attempt_positive CHECK (attempt >= 1 AND duration_ms >= 0),
  CONSTRAINT webhook_attempt_error_length CHECK (error IS NULL OR length(error) <= 500)
);

REVOKE UPDATE, DELETE, TRUNCATE ON ztax.webhook_subscription FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.webhook_status FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.webhook_secret FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.webhook_attempt FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.webhook_delivery FROM ztax_app;
-- The delivery's bookkeeping, and nothing else, moves.
GRANT UPDATE (status, attempts, next_attempt_at, delivered_at) ON ztax.webhook_delivery TO ztax_app;

COMMENT ON TABLE ztax.webhook_subscription IS
  'A webhook: where, and which event types. Append-only; status in webhook_status.';
COMMENT ON TABLE ztax.webhook_secret IS
  'Signing secret versions, sealed. Append-only; a rotation retires the previous version by date.';
COMMENT ON TABLE ztax.webhook_delivery IS
  'One event to one webhook. Status columns are delivery bookkeeping; attempts are in webhook_attempt.';
