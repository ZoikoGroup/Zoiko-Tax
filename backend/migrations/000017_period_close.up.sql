-- Tax Control Subledger period close (W2 lane J; ZTAX-FIN-001 §20–§22).
--
-- A legal period's state is its last event: a period with none is OPEN. A
-- hard close writes a close manifest — the sealed population of the period —
-- and names it in the closing event. Reopening takes a request and a
-- different user's approval. All three tables are append-only: a period
-- closed twice has two manifests, the second naming the first.

CREATE TABLE ztax.tcsl_close_manifest (
  tenant_id        uuid        NOT NULL REFERENCES ztax.tenant (tenant_id),
  manifest_digest  text        NOT NULL,
  legal_entity_id  uuid        NOT NULL,
  legal_period     text        NOT NULL,
  -- The canonical manifest exactly as digested (subledger.CloseManifest).
  body             bytea       NOT NULL,
  created_at       timestamptz NOT NULL,
  PRIMARY KEY (tenant_id, manifest_digest),
  FOREIGN KEY (tenant_id, legal_entity_id) REFERENCES ztax.legal_entity (tenant_id, legal_entity_id),
  CONSTRAINT tcsl_close_manifest_digest_shape CHECK (manifest_digest LIKE 'zt1:%'),
  CONSTRAINT tcsl_close_manifest_period_shape CHECK (legal_period ~ '^[0-9]{4}-(0[1-9]|1[0-2])$')
);

CREATE TABLE ztax.tcsl_reopen_request (
  tenant_id        uuid        NOT NULL REFERENCES ztax.tenant (tenant_id),
  request_id       uuid        NOT NULL,
  legal_entity_id  uuid        NOT NULL,
  legal_period     text        NOT NULL,
  reason           text        NOT NULL,
  requested_at     timestamptz NOT NULL,
  requested_by     uuid        NOT NULL,
  PRIMARY KEY (tenant_id, request_id),
  FOREIGN KEY (tenant_id, legal_entity_id) REFERENCES ztax.legal_entity (tenant_id, legal_entity_id),
  CONSTRAINT tcsl_reopen_request_reason_present CHECK (length(reason) BETWEEN 1 AND 1000)
);

CREATE TABLE ztax.tcsl_period_event (
  tenant_id        uuid        NOT NULL REFERENCES ztax.tenant (tenant_id),
  legal_entity_id  uuid        NOT NULL,
  legal_period     text        NOT NULL,
  seq              integer     NOT NULL,
  state            text        NOT NULL,
  reason           text        NULL,
  recorded_at      timestamptz NOT NULL,
  recorded_by      uuid        NULL,
  -- On a REOPENED event: the request approved, and who approved it. On that
  -- event recorded_by is the requester, so the four-eyes check below compares
  -- the two people the rule is about.
  request_id       uuid        NULL,
  approved_by      uuid        NULL,
  -- On a closing event: the manifest it wrote.
  manifest_digest  text        NULL,
  PRIMARY KEY (tenant_id, legal_entity_id, legal_period, seq),
  FOREIGN KEY (tenant_id, legal_entity_id) REFERENCES ztax.legal_entity (tenant_id, legal_entity_id),
  FOREIGN KEY (tenant_id, request_id) REFERENCES ztax.tcsl_reopen_request (tenant_id, request_id),
  FOREIGN KEY (tenant_id, manifest_digest) REFERENCES ztax.tcsl_close_manifest (tenant_id, manifest_digest),
  CONSTRAINT tcsl_period_event_state_known CHECK (state IN ('OPEN', 'SOFT_CLOSE', 'HARD_CLOSE', 'REOPENED', 'AMENDMENT_ACTIVE', 'SEALED')),
  CONSTRAINT tcsl_period_event_period_shape CHECK (legal_period ~ '^[0-9]{4}-(0[1-9]|1[0-2])$'),
  CONSTRAINT tcsl_period_event_seq_positive CHECK (seq >= 1),
  -- A hard close carries its manifest; a reopening carries its approval.
  CONSTRAINT tcsl_period_event_close_has_manifest CHECK ((state = 'HARD_CLOSE') = (manifest_digest IS NOT NULL)),
  CONSTRAINT tcsl_period_event_reopen_is_approved CHECK (
    (state = 'REOPENED') = (request_id IS NOT NULL AND approved_by IS NOT NULL)),
  -- Nobody approves their own request (ZTAX-FIN-REQ-0091).
  CONSTRAINT tcsl_period_event_four_eyes CHECK (approved_by IS NULL OR approved_by <> recorded_by OR recorded_by IS NULL)
);

-- A reopen request is approved at most once.
CREATE UNIQUE INDEX tcsl_period_event_request_once ON ztax.tcsl_period_event (tenant_id, request_id)
  WHERE request_id IS NOT NULL;

REVOKE UPDATE, DELETE, TRUNCATE ON ztax.tcsl_close_manifest FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.tcsl_reopen_request FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.tcsl_period_event FROM ztax_app;

COMMENT ON TABLE ztax.tcsl_period_event IS
  'A subledger legal period''s state history. Append-only; no event is OPEN.';
COMMENT ON TABLE ztax.tcsl_close_manifest IS
  'The sealed population of a hard-closed period, canonical and digested. Append-only.';
