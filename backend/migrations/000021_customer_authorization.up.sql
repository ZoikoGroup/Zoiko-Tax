-- Customer authorizations (ZTAX-LEG-001 §9; ZTAX-LEG-REQ-0014 to -0018).
--
-- A customer's grant of authority to one authority — a power of attorney, a
-- filing mandate, a portal delegation — for named permissions, matters and
-- periods, from one of its legal entities. The record is immutable; its state
-- is its event history: granted, then at most one of revoked or superseded
-- (ZTAX-LEG-REQ-0015). Expiry is a date, read at the moment of asking.
--
-- The authority credential is never here. credential_ref names it in the
-- secrets vault, and the shape check refuses anything that is not such a
-- reference (ZTAX-LEG-REQ-0017, -0018).
--
-- The authorization matrix the gate resolves these against is legal content,
-- loaded by the cell from a reviewed file, and has no table.

CREATE TABLE ztax.customer_authorization (
  tenant_id         uuid        NOT NULL REFERENCES ztax.tenant (tenant_id),
  authorization_id  uuid        NOT NULL,
  legal_entity_id   uuid        NOT NULL,
  country_code      text        NOT NULL,
  authority         text        NOT NULL,
  auth_type         text        NOT NULL,
  permissions       text[]      NOT NULL,
  matters           text[]      NOT NULL,
  period_from       text        NULL,
  period_to         text        NULL,
  representative    text        NULL,
  effective_from    timestamptz NOT NULL,
  expires_at        timestamptz NULL,
  evidence          text[]      NOT NULL,
  credential_ref    text        NULL,
  supersedes        uuid        NULL,
  recorded_at       timestamptz NOT NULL,
  recorded_by       uuid        NOT NULL,
  PRIMARY KEY (tenant_id, authorization_id),
  FOREIGN KEY (tenant_id, legal_entity_id) REFERENCES ztax.legal_entity (tenant_id, legal_entity_id),
  FOREIGN KEY (tenant_id, supersedes) REFERENCES ztax.customer_authorization (tenant_id, authorization_id),
  CONSTRAINT customer_authorization_country_shape CHECK (country_code ~ '^[A-Z]{2}$'),
  CONSTRAINT customer_authorization_type_known CHECK (auth_type IN ('CONTRACT', 'DECLARATION', 'POA',
    'TAX_INFORMATION', 'PORTAL_DELEGATION', 'FILING_MANDATE')),
  CONSTRAINT customer_authorization_permissions_present CHECK (cardinality(permissions) >= 1
    AND permissions <@ ARRAY['READ_INFO', 'PREPARE', 'SUBMIT', 'RECEIVE_NOTICE', 'REPRESENT', 'SIGN']::text[]),
  CONSTRAINT customer_authorization_periods CHECK ((period_from IS NULL) = (period_to IS NULL)
    AND (period_from IS NULL OR (period_from ~ '^[0-9]{4}-(0[1-9]|1[0-2])$' AND period_to ~ '^[0-9]{4}-(0[1-9]|1[0-2])$'
         AND period_to >= period_from))),
  CONSTRAINT customer_authorization_window CHECK (expires_at IS NULL OR expires_at > effective_from),
  CONSTRAINT customer_authorization_evidence_present CHECK (cardinality(evidence) >= 1),
  CONSTRAINT customer_authorization_credential_is_a_reference CHECK (credential_ref IS NULL
    OR credential_ref ~ '^(vault|awssm|gcpsm|azkv)://[A-Za-z0-9._/-]{1,240}$')
);
CREATE INDEX customer_authorization_scope ON ztax.customer_authorization (tenant_id, legal_entity_id, country_code, authority);
-- A grant is superseded at most once.
CREATE UNIQUE INDEX customer_authorization_supersedes_once ON ztax.customer_authorization (tenant_id, supersedes)
  WHERE supersedes IS NOT NULL;

CREATE TABLE ztax.customer_authorization_event (
  tenant_id         uuid        NOT NULL,
  authorization_id  uuid        NOT NULL,
  seq               integer     NOT NULL,
  kind              text        NOT NULL,
  reason            text        NULL,
  superseded_by     uuid        NULL,
  recorded_at       timestamptz NOT NULL,
  recorded_by       uuid        NOT NULL,
  PRIMARY KEY (tenant_id, authorization_id, seq),
  FOREIGN KEY (tenant_id, authorization_id) REFERENCES ztax.customer_authorization (tenant_id, authorization_id),
  CONSTRAINT customer_authorization_event_kind_known CHECK (kind IN ('GRANTED', 'REVOKED', 'SUPERSEDED')),
  CONSTRAINT customer_authorization_event_granted_first CHECK ((kind = 'GRANTED') = (seq = 1)),
  CONSTRAINT customer_authorization_event_final CHECK (seq <= 2),
  CONSTRAINT customer_authorization_event_superseded_by CHECK ((kind = 'SUPERSEDED') = (superseded_by IS NOT NULL)),
  CONSTRAINT customer_authorization_event_reason_length CHECK (reason IS NULL OR length(reason) BETWEEN 1 AND 1000)
);

REVOKE UPDATE, DELETE, TRUNCATE ON ztax.customer_authorization FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.customer_authorization_event FROM ztax_app;

ALTER TABLE ztax.customer_authorization ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.customer_authorization
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.customer_authorization_event ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.customer_authorization_event
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

COMMENT ON TABLE ztax.customer_authorization IS
  'A customer grant of authority to one authority. Immutable; its state is its event history.';
COMMENT ON TABLE ztax.customer_authorization_event IS
  'Granted, then at most one of revoked or superseded, with who and why. Append-only.';
