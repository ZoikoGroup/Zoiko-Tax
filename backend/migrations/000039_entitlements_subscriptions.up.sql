-- Commercial entitlements and subscription plans (Engineering Doc 18 §05
-- TenantGrant and CommercialSKU, §07; G-004, G-049).
--
-- Two registers, each a row per record and an append-only event table:
--
--   entitlement_grant      a contract right for one legal entity to invoke
--                          one operation (QUOTE, COMMIT, FILE, REMIT,
--                          E_INVOICE, SHADOW_COMPARE) in one mode for one
--                          service family in one jurisdiction, between two
--                          dates, from a named contract. Its lifecycle —
--                          DRAFT, IN_REVIEW, APPROVED, REJECTED, WITHDRAWN,
--                          SUSPENDED, REVOKED — is the fold of its events.
--                          APPROVED only by a second administrator; SCHEDULED,
--                          ACTIVE and EXPIRED are an approved grant read
--                          against its dates and are not stored.
--   subscription_plan      a plan revision: plan code, term, contract and an
--                          included monthly quota per meter (in
--                          subscription_plan_quota). PROPOSED, then APPROVED
--                          by a second administrator, REJECTED or WITHDRAWN.
--                          No price is recorded.
--
-- Usage is not stored. It is counted on request from the cell's own rows —
-- tax_decision, fiscal_document, compliance_step, shadow_run — so a count is
-- reproducible from the records it counts and cannot drift from them.
--
-- An entitlement never makes a market live: whether its coverage is proven
-- is read from the content in force, the cell's authority and the legal gate
-- each time, never stored here. Nothing here references or changes a fiscal
-- table, a rule or content.
--
-- intake_key and intake_hash make intake idempotent on the Idempotency-Key:
-- the same key and request return the same record; the same key with a
-- different request is refused.

CREATE TABLE ztax.entitlement_grant (
  tenant_id          uuid        NOT NULL REFERENCES ztax.tenant (tenant_id),
  entitlement_id     uuid        NOT NULL,
  legal_entity_id    uuid        NOT NULL,
  jurisdiction       text        NOT NULL,
  service            text        NOT NULL,
  mode               text        NOT NULL,
  operation          text        NOT NULL,
  effective_from     date        NOT NULL,
  effective_to       date        NULL,
  contract_ref       text        NOT NULL,
  contract_revision  text        NULL,
  intake_key         text        NOT NULL,
  intake_hash        text        NOT NULL,
  created_at         timestamptz NOT NULL,
  created_by         uuid        NOT NULL,
  PRIMARY KEY (tenant_id, entitlement_id),
  FOREIGN KEY (tenant_id, legal_entity_id) REFERENCES ztax.legal_entity (tenant_id, legal_entity_id),
  CONSTRAINT entitlement_grant_jurisdiction_shape CHECK (jurisdiction ~ '^[A-Z]{2}(-[A-Z0-9]{1,3})?$'),
  CONSTRAINT entitlement_grant_service_shape CHECK (service ~ '^[A-Z][A-Z_]{1,40}$'),
  CONSTRAINT entitlement_grant_mode_known CHECK (mode IN ('NATIVE', 'FEDERATED', 'SHADOW_ASSURANCE', 'OEM_EMBEDDED',
    'MANAGED_COMPLIANCE')),
  CONSTRAINT entitlement_grant_operation_known CHECK (operation IN ('QUOTE', 'COMMIT', 'FILE', 'REMIT', 'E_INVOICE',
    'SHADOW_COMPARE')),
  -- No mode implies another's operations (Doc 18 §02).
  CONSTRAINT entitlement_grant_mode_operation CHECK (
    (mode = 'NATIVE' AND operation IN ('QUOTE', 'COMMIT', 'FILE', 'REMIT', 'E_INVOICE'))
    OR (mode IN ('FEDERATED', 'OEM_EMBEDDED') AND operation IN ('QUOTE', 'COMMIT'))
    OR (mode = 'SHADOW_ASSURANCE' AND operation = 'SHADOW_COMPARE')
    OR (mode = 'MANAGED_COMPLIANCE' AND operation IN ('FILE', 'REMIT'))),
  CONSTRAINT entitlement_grant_dates CHECK (effective_to IS NULL OR effective_to >= effective_from),
  CONSTRAINT entitlement_grant_hash_shape CHECK (intake_hash ~ '^zt1:[0-9a-f]{64}$'),
  CONSTRAINT entitlement_grant_text_length CHECK (length(contract_ref) BETWEEN 1 AND 128
    AND (contract_revision IS NULL OR length(contract_revision) BETWEEN 1 AND 128)
    AND length(intake_key) BETWEEN 1 AND 255)
);
CREATE UNIQUE INDEX entitlement_grant_intake_once ON ztax.entitlement_grant (tenant_id, intake_key);
CREATE INDEX entitlement_grant_recent ON ztax.entitlement_grant (tenant_id, created_at DESC, entitlement_id);

CREATE TABLE ztax.entitlement_grant_event (
  tenant_id          uuid        NOT NULL,
  entitlement_id     uuid        NOT NULL,
  seq                integer     NOT NULL,
  kind               text        NOT NULL,
  lifecycle_after    text        NOT NULL,
  reason             text        NULL,
  recorded_at        timestamptz NOT NULL,
  recorded_by        uuid        NOT NULL,
  PRIMARY KEY (tenant_id, entitlement_id, seq),
  FOREIGN KEY (tenant_id, entitlement_id) REFERENCES ztax.entitlement_grant (tenant_id, entitlement_id),
  CONSTRAINT entitlement_grant_event_kind_known CHECK (kind IN ('CREATED', 'SUBMITTED', 'APPROVED', 'REJECTED',
    'WITHDRAWN', 'SUSPENDED', 'RESUME_REQUESTED', 'RESUMED', 'REVOKED')),
  CONSTRAINT entitlement_grant_event_created_first CHECK ((kind = 'CREATED') = (seq = 1)),
  CONSTRAINT entitlement_grant_event_lifecycle_known CHECK (lifecycle_after IN ('DRAFT', 'IN_REVIEW', 'APPROVED',
    'REJECTED', 'WITHDRAWN', 'SUSPENDED', 'REVOKED')),
  -- APPROVED is reached only by an approval or a resume.
  CONSTRAINT entitlement_grant_event_approved_by_checker CHECK (lifecycle_after <> 'APPROVED'
    OR kind IN ('APPROVED', 'RESUMED')),
  CONSTRAINT entitlement_grant_event_reason_given CHECK (kind IN ('CREATED', 'SUBMITTED') OR reason IS NOT NULL),
  CONSTRAINT entitlement_grant_event_reason_length CHECK (reason IS NULL OR length(reason) BETWEEN 1 AND 2000)
);

CREATE TABLE ztax.subscription_plan (
  tenant_id          uuid        NOT NULL REFERENCES ztax.tenant (tenant_id),
  plan_id            uuid        NOT NULL,
  plan_code          text        NOT NULL,
  plan_name          text        NOT NULL,
  contract_ref       text        NOT NULL,
  term_start         date        NOT NULL,
  term_end           date        NULL,
  intake_key         text        NOT NULL,
  intake_hash        text        NOT NULL,
  created_at         timestamptz NOT NULL,
  created_by         uuid        NOT NULL,
  PRIMARY KEY (tenant_id, plan_id),
  CONSTRAINT subscription_plan_code_shape CHECK (plan_code ~ '^[A-Z0-9][A-Z0-9_.-]{0,63}$'),
  CONSTRAINT subscription_plan_term CHECK (term_end IS NULL OR term_end >= term_start),
  CONSTRAINT subscription_plan_hash_shape CHECK (intake_hash ~ '^zt1:[0-9a-f]{64}$'),
  CONSTRAINT subscription_plan_text_length CHECK (length(plan_name) BETWEEN 1 AND 128
    AND length(contract_ref) BETWEEN 1 AND 128 AND length(intake_key) BETWEEN 1 AND 255)
);
CREATE UNIQUE INDEX subscription_plan_intake_once ON ztax.subscription_plan (tenant_id, intake_key);
CREATE INDEX subscription_plan_recent ON ztax.subscription_plan (tenant_id, created_at DESC, plan_id);

CREATE TABLE ztax.subscription_plan_quota (
  tenant_id          uuid        NOT NULL,
  plan_id            uuid        NOT NULL,
  meter              text        NOT NULL,
  included           bigint      NOT NULL,
  overage_policy     text        NOT NULL,
  PRIMARY KEY (tenant_id, plan_id, meter),
  FOREIGN KEY (tenant_id, plan_id) REFERENCES ztax.subscription_plan (tenant_id, plan_id),
  CONSTRAINT subscription_plan_quota_meter_known CHECK (meter IN ('COMMITTED_DECISIONS', 'FISCAL_DOCUMENTS',
    'FILING_SUBMISSIONS', 'SHADOW_RUNS')),
  CONSTRAINT subscription_plan_quota_included CHECK (included BETWEEN 0 AND 1000000000000),
  CONSTRAINT subscription_plan_quota_overage_known CHECK (overage_policy IN ('ALLOW_OVERAGE', 'HARD_LIMIT'))
);

CREATE TABLE ztax.subscription_plan_event (
  tenant_id          uuid        NOT NULL,
  plan_id            uuid        NOT NULL,
  seq                integer     NOT NULL,
  kind               text        NOT NULL,
  status_after       text        NOT NULL,
  reason             text        NULL,
  recorded_at        timestamptz NOT NULL,
  recorded_by        uuid        NOT NULL,
  PRIMARY KEY (tenant_id, plan_id, seq),
  FOREIGN KEY (tenant_id, plan_id) REFERENCES ztax.subscription_plan (tenant_id, plan_id),
  CONSTRAINT subscription_plan_event_kind_known CHECK (kind IN ('PROPOSED', 'APPROVED', 'REJECTED', 'WITHDRAWN')),
  CONSTRAINT subscription_plan_event_proposed_first CHECK ((kind = 'PROPOSED') = (seq = 1)),
  CONSTRAINT subscription_plan_event_status CHECK (status_after = kind),
  -- A proposal moves once: approved, rejected or withdrawn.
  CONSTRAINT subscription_plan_event_once CHECK (seq <= 2),
  CONSTRAINT subscription_plan_event_reason_given CHECK (kind IN ('PROPOSED', 'APPROVED') OR reason IS NOT NULL),
  CONSTRAINT subscription_plan_event_reason_length CHECK (reason IS NULL OR length(reason) BETWEEN 1 AND 2000)
);

REVOKE UPDATE, DELETE, TRUNCATE ON ztax.entitlement_grant FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.entitlement_grant_event FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.subscription_plan FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.subscription_plan_quota FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.subscription_plan_event FROM ztax_app;

ALTER TABLE ztax.entitlement_grant ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.entitlement_grant
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.entitlement_grant_event ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.entitlement_grant_event
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.subscription_plan ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.subscription_plan
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.subscription_plan_quota ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.subscription_plan_quota
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.subscription_plan_event ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.subscription_plan_event
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

COMMENT ON TABLE ztax.entitlement_grant IS
  'A contract right for one legal entity, jurisdiction, service, mode and operation, between two dates, as recorded. Immutable; its lifecycle is its event history. Never coverage: whether the cell proves it is read each time.';
COMMENT ON TABLE ztax.entitlement_grant_event IS
  'Submission, second-administrator approval, rejection, withdrawal, suspension, resume request and approval, and revocation of a grant, in order. Append-only.';
COMMENT ON TABLE ztax.subscription_plan IS
  'A subscription plan revision: plan code, term and contract. Immutable; its status is its event history. No price.';
COMMENT ON TABLE ztax.subscription_plan_quota IS
  'The included monthly quantity and overage policy per meter of a plan revision. Immutable.';
COMMENT ON TABLE ztax.subscription_plan_event IS
  'Proposal and its one approval, rejection or withdrawal. Append-only.';
