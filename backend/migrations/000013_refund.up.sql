-- Refunds (W2 lane K, ZTAX-FIN-001 §14).
--
-- A refund returns tax a committed decision charged. It is modelled apart from
-- the decision and from any credit (ZTAX-FIN-REQ-0015): the decision is never
-- rewritten, and the refund has a lifecycle of its own (ZTAX-FIN-REQ-0059).
--
-- Two tables, both append-only. The header is what was asked for and never
-- changes; the events are what the payment provider said, in order, and the
-- current status is the last event's. A status column updated in place would
-- need UPDATE, which ztax_app does not hold, and would lose the history that
-- explains how a refund became UNCERTAIN and how it was resolved.

CREATE TABLE ztax.refund (
  tenant_id          uuid        NOT NULL REFERENCES ztax.tenant (tenant_id),
  refund_id          uuid        NOT NULL,
  legal_entity_id    uuid        NOT NULL,
  decision_id        uuid        NOT NULL,
  -- Positive: the kind says which way it moves.
  amount             numeric     NOT NULL,
  currency           text        NOT NULL,
  payment_reference  text        NOT NULL,
  reason             text        NULL,
  requested_at       timestamptz NOT NULL,
  -- NULL for a refund system work requested.
  requested_by       uuid        NULL,
  PRIMARY KEY (tenant_id, refund_id),
  FOREIGN KEY (tenant_id, decision_id) REFERENCES ztax.tax_decision (tenant_id, decision_id),
  FOREIGN KEY (tenant_id, legal_entity_id) REFERENCES ztax.legal_entity (tenant_id, legal_entity_id),
  CONSTRAINT refund_amount_positive CHECK (amount > 0),
  CONSTRAINT refund_currency_shape CHECK (currency ~ '^[A-Z]{3}$'),
  CONSTRAINT refund_payment_reference_shape CHECK (length(payment_reference) BETWEEN 1 AND 255),
  CONSTRAINT refund_reason_length CHECK (reason IS NULL OR length(reason) <= 1000)
);

-- "What has been refunded against this decision" is the question every new
-- refund asks before it is admitted.
CREATE INDEX refund_by_decision ON ztax.refund (tenant_id, decision_id);

CREATE TABLE ztax.refund_event (
  tenant_id           uuid        NOT NULL,
  refund_id           uuid        NOT NULL,
  -- 1 is the REQUESTED event written with the header. The primary key makes
  -- a stale writer's event collide with the one it did not see, so two
  -- reports read against the same history cannot both be appended.
  seq                 integer     NOT NULL,
  status              text        NOT NULL,
  outcome             text        NULL,
  external_reference  text        NULL,
  recorded_at         timestamptz NOT NULL,
  recorded_by         uuid        NULL,
  PRIMARY KEY (tenant_id, refund_id, seq),
  FOREIGN KEY (tenant_id, refund_id) REFERENCES ztax.refund (tenant_id, refund_id),
  CONSTRAINT refund_event_seq_positive CHECK (seq >= 1),
  CONSTRAINT refund_event_status_known CHECK (status IN ('REQUESTED', 'PENDING', 'COMPLETED', 'FAILED', 'UNCERTAIN')),
  CONSTRAINT refund_event_outcome_known CHECK (outcome IS NULL OR outcome IN ('ACCEPTED', 'SUCCEEDED', 'DECLINED', 'TIMED_OUT', 'UNKNOWN')),
  -- The first event is the request, and only the first event is.
  CONSTRAINT refund_event_first_is_request CHECK ((seq = 1) = (status = 'REQUESTED' AND outcome IS NULL)),
  CONSTRAINT refund_event_external_reference_length CHECK (external_reference IS NULL OR length(external_reference) <= 255)
);

REVOKE UPDATE, DELETE, TRUNCATE ON ztax.refund FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.refund_event FROM ztax_app;

COMMENT ON TABLE ztax.refund IS
  'A refund of tax a committed decision charged. Append-only; its status is the last refund_event.';
COMMENT ON TABLE ztax.refund_event IS
  'What the payment provider reported about a refund, in order. Append-only.';
