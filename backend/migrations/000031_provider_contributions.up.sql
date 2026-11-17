-- Provider contributions (OBL-06 Contribution Accumulator and Source Event
-- Explorer; OBL-10 USAC; the ContributionEvent model).
--
-- A provider contribution is the operator's own obligation to a fund — the
-- US Universal Service Fund through USAC, Canada's CRTC National
-- Contribution Fund and the like — assessed on the provider's eligible
-- revenue. It is not tax charged to a customer: what a provider recovers
-- through a surcharge line is a receivable, never this payable, and a
-- contribution exists with no surcharge line at all (UAT-OBL-032, -033).
--
-- One row per source event and version, its bases exact NUMERIC in one
-- currency, and its immutable hash over every source field. The natural key
-- makes a redelivered event one contribution (UAT-OBL-026). A reversal is a
-- negative row of its own naming the source event it reverses, never an
-- edit (UAT-OBL-027). Status — ACCEPTED or QUARANTINED — is the fold of the
-- appended status events; a quarantine never deletes the row it holds.
--
-- No personal data (UAT-OBL-025): the rows are a provider's revenue
-- aggregates and references into its own systems. The only person named is
-- the operator who recorded or moved an event.

CREATE TABLE ztax.provider_contribution (
  tenant_id                 uuid        NOT NULL REFERENCES ztax.tenant (tenant_id),
  contribution_id           uuid        NOT NULL,
  source_event_id           text        NOT NULL,
  source_version            integer     NOT NULL,
  source_type               text        NOT NULL,
  source_checksum           text        NULL,
  regime                    text        NOT NULL,
  period                    text        NOT NULL,
  legal_entity_id           uuid        NOT NULL,
  obligation_id             uuid        NULL,
  classification_ref        text        NOT NULL,
  situs_ref                 text        NOT NULL,
  source_ref                text        NOT NULL,
  component_ref             text        NULL,
  rule_version              text        NOT NULL,
  currency                  text        NOT NULL,
  eligible_base             numeric     NOT NULL,
  excluded_base             numeric     NOT NULL,
  exclusion_reason          text        NULL,
  quantity                  numeric     NULL,
  quantity_unit             text        NULL,
  reverses_source_event_id  text        NULL,
  reverses_source_version   integer     NULL,
  amendment                 boolean     NOT NULL,
  immutable_hash            text        NOT NULL,
  recorded_at               timestamptz NOT NULL,
  recorded_by               uuid        NOT NULL,
  PRIMARY KEY (tenant_id, contribution_id),
  FOREIGN KEY (tenant_id, legal_entity_id) REFERENCES ztax.legal_entity (tenant_id, legal_entity_id),
  FOREIGN KEY (tenant_id, obligation_id) REFERENCES ztax.obligation (tenant_id, obligation_id),
  CONSTRAINT provider_contribution_version_positive CHECK (source_version >= 1),
  CONSTRAINT provider_contribution_source_type_known CHECK (source_type IN ('BSS_INVOICE', 'RATED_USAGE',
    'REVENUE_RECOGNITION_ADJUSTMENT', 'LICENCE_ACTIVITY', 'MANUAL_CERTIFIED_SCHEDULE')),
  CONSTRAINT provider_contribution_regime_known CHECK (regime IN ('US_USF', 'CA_CRTC_NCF', 'OTHER')),
  CONSTRAINT provider_contribution_period_shape CHECK (period ~ '^[0-9]{4}-(0[1-9]|1[0-2])$'),
  CONSTRAINT provider_contribution_currency_shape CHECK (currency ~ '^[A-Z]{3}$'),
  CONSTRAINT provider_contribution_hash_shape CHECK (immutable_hash ~ '^zt1:[0-9a-f]{64}$'),
  CONSTRAINT provider_contribution_quantity_unit CHECK ((quantity IS NULL) = (quantity_unit IS NULL)),
  CONSTRAINT provider_contribution_reverses_whole CHECK (
    (reverses_source_event_id IS NULL) = (reverses_source_version IS NULL)),
  -- A contribution is never negative; a reversal is never positive, and
  -- reverses something.
  CONSTRAINT provider_contribution_signs CHECK (
    (reverses_source_event_id IS NULL AND eligible_base >= 0 AND excluded_base >= 0)
    OR (reverses_source_event_id IS NOT NULL AND eligible_base <= 0 AND excluded_base <= 0
        AND (eligible_base < 0 OR excluded_base < 0))),
  CONSTRAINT provider_contribution_exclusion_explained CHECK (excluded_base = 0 OR exclusion_reason IS NOT NULL),
  CONSTRAINT provider_contribution_text_length CHECK (length(source_event_id) BETWEEN 1 AND 200
    AND length(classification_ref) BETWEEN 1 AND 200 AND length(situs_ref) BETWEEN 1 AND 200
    AND length(source_ref) BETWEEN 1 AND 200 AND length(rule_version) BETWEEN 1 AND 200
    AND (component_ref IS NULL OR length(component_ref) BETWEEN 1 AND 200)
    AND (source_checksum IS NULL OR length(source_checksum) BETWEEN 1 AND 200)
    AND (exclusion_reason IS NULL OR length(exclusion_reason) BETWEEN 1 AND 2000)
    AND (reverses_source_event_id IS NULL OR length(reverses_source_event_id) BETWEEN 1 AND 200))
);
-- A source event and version is one contribution (UAT-OBL-026), and an
-- original is reversed at most once.
CREATE UNIQUE INDEX provider_contribution_source_once
  ON ztax.provider_contribution (tenant_id, source_event_id, source_version);
CREATE UNIQUE INDEX provider_contribution_reversed_once
  ON ztax.provider_contribution (tenant_id, reverses_source_event_id, reverses_source_version)
  WHERE reverses_source_event_id IS NOT NULL;
CREATE INDEX provider_contribution_by_period
  ON ztax.provider_contribution (tenant_id, period, legal_entity_id, recorded_at);

CREATE TABLE ztax.provider_contribution_event (
  tenant_id        uuid        NOT NULL,
  contribution_id  uuid        NOT NULL,
  seq              integer     NOT NULL,
  kind             text        NOT NULL,
  status_after     text        NOT NULL,
  reason           text        NULL,
  recorded_at      timestamptz NOT NULL,
  recorded_by      uuid        NOT NULL,
  PRIMARY KEY (tenant_id, contribution_id, seq),
  FOREIGN KEY (tenant_id, contribution_id) REFERENCES ztax.provider_contribution (tenant_id, contribution_id),
  CONSTRAINT provider_contribution_event_kind_known CHECK (kind IN ('RECORDED', 'QUARANTINED', 'RELEASED')),
  CONSTRAINT provider_contribution_event_recorded_first CHECK ((kind = 'RECORDED') = (seq = 1)),
  CONSTRAINT provider_contribution_event_status_known CHECK (status_after IN ('ACCEPTED', 'QUARANTINED')),
  CONSTRAINT provider_contribution_event_status_follows CHECK (
    (kind = 'QUARANTINED') = (status_after = 'QUARANTINED')),
  CONSTRAINT provider_contribution_event_reason_given CHECK (kind = 'RECORDED' OR reason IS NOT NULL),
  CONSTRAINT provider_contribution_event_reason_length CHECK (reason IS NULL OR length(reason) BETWEEN 1 AND 2000)
);

REVOKE UPDATE, DELETE, TRUNCATE ON ztax.provider_contribution FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.provider_contribution_event FROM ztax_app;

ALTER TABLE ztax.provider_contribution ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.provider_contribution
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.provider_contribution_event ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.provider_contribution_event
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

COMMENT ON TABLE ztax.provider_contribution IS
  'One provider contribution per source event and version: the operator''s contribution base, exact, hashed. Never a customer charge. Append-only.';
COMMENT ON TABLE ztax.provider_contribution_event IS
  'Recording, quarantine and release of a provider contribution, in order. Append-only; a quarantine deletes nothing.';
