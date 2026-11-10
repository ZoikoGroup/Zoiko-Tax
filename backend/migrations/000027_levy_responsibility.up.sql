-- Levy responsibility (ZTAX-ENG-007 §29; G-018).
--
-- One row per levy of a committed tax decision: the five-role decision of
-- who the law places the levy on, who collects, reports and remits it, and
-- who bears its cost, resolved against the parties the document names
-- (ZTAX-OBL-REQ-0001 to -0019). It sits beside tax_decision rather than in
-- the sealed result, so the result a replay compares is unchanged.
--
-- Immutable, like the decision it belongs to. A correction is a new decision
-- with rows of its own.

CREATE TABLE ztax.levy_responsibility (
  tenant_id                  uuid        NOT NULL REFERENCES ztax.tenant (tenant_id),
  responsibility_decision_id uuid        NOT NULL,
  decision_id                uuid        NOT NULL,
  item_key                   text        NOT NULL,
  component_id               text        NOT NULL,
  component_version          text        NOT NULL,
  tax_type                   text        NOT NULL,
  jurisdiction               text        NOT NULL,
  authority                  text        NOT NULL,
  treatment                  text        NOT NULL,
  legal_entity_id            uuid        NOT NULL,
  rule_id                    text        NOT NULL,
  rule_version               text        NOT NULL,
  bundle_id                  text        NOT NULL,
  bundle_digest              text        NOT NULL,
  effective_from             timestamptz NOT NULL,
  status                     text        NOT NULL,
  -- Every role, in INCIDENCE, COLLECTION, REPORTING, REMITTANCE,
  -- ECONOMIC_BEARER order: role, status, statutory party, agent, detail.
  assignments                jsonb       NOT NULL,
  recorded_at                timestamptz NOT NULL,
  PRIMARY KEY (tenant_id, responsibility_decision_id),
  FOREIGN KEY (tenant_id, decision_id) REFERENCES ztax.tax_decision (tenant_id, decision_id),
  FOREIGN KEY (tenant_id, legal_entity_id) REFERENCES ztax.legal_entity (tenant_id, legal_entity_id),
  CONSTRAINT levy_responsibility_status_known CHECK (
    status IN ('RESOLVED', 'AMBIGUOUS', 'CONFLICTED', 'UNSUPPORTED')),
  CONSTRAINT levy_responsibility_treatment_known CHECK (
    treatment IN ('CUSTOMER_CHARGE', 'PROVIDER_COST', 'REVERSE_CHARGE')),
  CONSTRAINT levy_responsibility_every_role CHECK (
    jsonb_typeof(assignments) = 'array' AND jsonb_array_length(assignments) = 5)
);
-- A levy on an item is decided once per tax decision.
CREATE UNIQUE INDEX levy_responsibility_once
  ON ztax.levy_responsibility (tenant_id, decision_id, item_key, component_id);

REVOKE UPDATE, DELETE, TRUNCATE ON ztax.levy_responsibility FROM ztax_app;

ALTER TABLE ztax.levy_responsibility ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.levy_responsibility
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

COMMENT ON TABLE ztax.levy_responsibility IS
  'The five-role responsibility decision for each levy of a committed tax decision. Append-only.';
