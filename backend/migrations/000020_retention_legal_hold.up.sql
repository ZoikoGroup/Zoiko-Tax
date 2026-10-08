-- Evidence retention and legal hold (ZTAX-EVID-001 §11–§13; ZTAX-PRIV-001
-- §14; ZTAX-LEG-REQ-0091).
--
-- A retention policy is a versioned rule for one class of record in one
-- country: how many years, from when, on what authority. A change is a new
-- version; the version a verdict used is named in it (ZTAX-EVID-REQ-0052,
-- -0126). A legal hold is a header naming its matter and an append-only
-- history of placements, scope changes and a release (ZTAX-EVID-REQ-0053).
-- Nothing here is updated or deleted by the application, and nothing here
-- touches a decision: retention is evaluated against the record as it stands
-- (ZTAX-EVID-REQ-0105).

CREATE TABLE ztax.retention_policy (
  tenant_id       uuid        NOT NULL REFERENCES ztax.tenant (tenant_id),
  policy_id       text        NOT NULL,
  version         integer     NOT NULL,
  record_class    text        NOT NULL,
  country_code    text        NOT NULL,
  years           integer     NOT NULL,
  trigger_kind    text        NOT NULL,
  effective_from  timestamptz NOT NULL,
  citation        text        NOT NULL,
  recorded_at     timestamptz NOT NULL,
  recorded_by     uuid        NOT NULL,
  PRIMARY KEY (tenant_id, policy_id, version),
  CONSTRAINT retention_policy_id_length CHECK (length(policy_id) BETWEEN 1 AND 128),
  CONSTRAINT retention_policy_version_positive CHECK (version >= 1),
  CONSTRAINT retention_policy_class_known CHECK (record_class IN ('DECISION', 'DOCUMENT', 'JOURNAL', 'REFUND')),
  CONSTRAINT retention_policy_country_shape CHECK (country_code ~ '^[A-Z]{2}$'),
  CONSTRAINT retention_policy_years_bounded CHECK (years BETWEEN 1 AND 100),
  CONSTRAINT retention_policy_trigger_known CHECK (trigger_kind IN ('EVENT_TIME', 'RECORDED_AT', 'EVENT_YEAR_END')),
  CONSTRAINT retention_policy_citation_length CHECK (length(citation) BETWEEN 1 AND 1000)
);

CREATE TABLE ztax.legal_hold (
  tenant_id   uuid        NOT NULL REFERENCES ztax.tenant (tenant_id),
  hold_id     uuid        NOT NULL,
  matter      text        NOT NULL,
  placed_at   timestamptz NOT NULL,
  placed_by   uuid        NOT NULL,
  PRIMARY KEY (tenant_id, hold_id),
  CONSTRAINT legal_hold_matter_length CHECK (length(matter) BETWEEN 1 AND 255)
);

CREATE TABLE ztax.legal_hold_event (
  tenant_id        uuid        NOT NULL,
  hold_id          uuid        NOT NULL,
  seq              integer     NOT NULL,
  kind             text        NOT NULL,
  -- The scope as of this event; empty on a release, which keeps the last.
  legal_entity_id  uuid        NULL,
  business_keys    text[]      NOT NULL,
  decision_ids     uuid[]      NOT NULL,
  event_from       timestamptz NULL,
  event_to         timestamptz NULL,
  reason           text        NOT NULL,
  recorded_at      timestamptz NOT NULL,
  recorded_by      uuid        NOT NULL,
  PRIMARY KEY (tenant_id, hold_id, seq),
  FOREIGN KEY (tenant_id, hold_id) REFERENCES ztax.legal_hold (tenant_id, hold_id),
  CONSTRAINT legal_hold_event_seq_positive CHECK (seq >= 1),
  CONSTRAINT legal_hold_event_kind_known CHECK (kind IN ('PLACED', 'SCOPE_CHANGED', 'RELEASED')),
  CONSTRAINT legal_hold_event_placed_first CHECK ((kind = 'PLACED') = (seq = 1)),
  CONSTRAINT legal_hold_event_window CHECK ((event_from IS NULL) = (event_to IS NULL)
    AND (event_from IS NULL OR event_to > event_from)),
  CONSTRAINT legal_hold_event_reason_length CHECK (length(reason) BETWEEN 1 AND 1000)
);

REVOKE UPDATE, DELETE, TRUNCATE ON ztax.retention_policy FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.legal_hold FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.legal_hold_event FROM ztax_app;

ALTER TABLE ztax.retention_policy ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.retention_policy
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.legal_hold ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.legal_hold
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.legal_hold_event ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.legal_hold_event
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

COMMENT ON TABLE ztax.retention_policy IS
  'Versioned retention policies by record class and country. Append-only; a change is a new version.';
COMMENT ON TABLE ztax.legal_hold IS
  'A legal hold and the matter it serves. Its state is its legal_hold_event history.';
COMMENT ON TABLE ztax.legal_hold_event IS
  'Every placement, scope change and release of a legal hold, with who and why. Append-only.';
