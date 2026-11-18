-- Regulatory workpapers (OBL-07 Workpaper Assembly & Tax/Regulatory Base
-- Ledger; LED-19 Regulatory Workpaper & Return Tie-Out; OPS-15 Period Close &
-- Workpaper Exception Triage).
--
-- One header per legal filer, duty and period: the authority it is reported
-- to, the reporting currency, the materiality threshold above which an
-- adjustment needs a second person, and the content release it was opened
-- under. The header is immutable. Its state is its event history: every
-- build, documented FX rate, adjustment, exception resolution, freeze,
-- return, approval and revocation is a new row.
--
-- A build is a row of its own: the hash of everything it read (the filer's
-- provider contributions for the period, its subledger balances, the
-- documented rates and the approved adjustments), the canonical document it
-- produced and that document's hash. Building again on the same inputs
-- writes nothing; different inputs are the next version. The stored document
-- is re-hashed on every read, so a frozen file that no longer matches its
-- digest is refused rather than shown (LED-19 "Compare workpaper digest").
--
-- Amounts are exact NUMERIC. Evidence is a document's SHA-256, name and media
-- type, computed where the document is; nothing is uploaded or stored here.
--
-- No personal data beyond the people who acted: the rows are a provider's
-- revenue aggregates, references into its own systems, and digests.

CREATE TABLE ztax.regulatory_workpaper (
  tenant_id            uuid        NOT NULL REFERENCES ztax.tenant (tenant_id),
  workpaper_id         uuid        NOT NULL,
  legal_entity_id      uuid        NOT NULL,
  duty                 text        NOT NULL,
  period               text        NOT NULL,
  authority            text        NOT NULL,
  reporting_currency   text        NOT NULL,
  materiality          numeric     NOT NULL,
  obligation_id        uuid        NULL,
  content_release      text        NOT NULL,
  created_at           timestamptz NOT NULL,
  created_by           uuid        NOT NULL,
  PRIMARY KEY (tenant_id, workpaper_id),
  FOREIGN KEY (tenant_id, legal_entity_id) REFERENCES ztax.legal_entity (tenant_id, legal_entity_id),
  FOREIGN KEY (tenant_id, obligation_id) REFERENCES ztax.obligation (tenant_id, obligation_id),
  CONSTRAINT regulatory_workpaper_duty_known CHECK (duty IN ('US_USF', 'CA_CRTC_NCF', 'OTHER')),
  CONSTRAINT regulatory_workpaper_period_shape CHECK (period ~ '^[0-9]{4}-(0[1-9]|1[0-2])$'),
  CONSTRAINT regulatory_workpaper_currency_shape CHECK (reporting_currency ~ '^[A-Z]{3}$'),
  CONSTRAINT regulatory_workpaper_materiality CHECK (materiality >= 0),
  CONSTRAINT regulatory_workpaper_text_length CHECK (length(authority) BETWEEN 1 AND 200
    AND length(content_release) BETWEEN 1 AND 200)
);
-- One workpaper per filer, duty and period.
CREATE UNIQUE INDEX regulatory_workpaper_once
  ON ztax.regulatory_workpaper (tenant_id, legal_entity_id, duty, period);
CREATE INDEX regulatory_workpaper_by_period ON ztax.regulatory_workpaper (tenant_id, period DESC, workpaper_id);

CREATE TABLE ztax.regulatory_workpaper_build (
  tenant_id         uuid        NOT NULL,
  workpaper_id      uuid        NOT NULL,
  version           integer     NOT NULL,
  inputs_hash       text        NOT NULL,
  output_hash       text        NOT NULL,
  basis_digest      text        NOT NULL,
  formula_version   text        NOT NULL,
  content_release   text        NOT NULL,
  output_canonical  text        NOT NULL,
  built_at          timestamptz NOT NULL,
  built_by          uuid        NOT NULL,
  PRIMARY KEY (tenant_id, workpaper_id, version),
  FOREIGN KEY (tenant_id, workpaper_id) REFERENCES ztax.regulatory_workpaper (tenant_id, workpaper_id),
  CONSTRAINT regulatory_workpaper_build_version_positive CHECK (version >= 1),
  CONSTRAINT regulatory_workpaper_build_hash_shape CHECK (inputs_hash ~ '^zt1:[0-9a-f]{64}$'
    AND output_hash ~ '^zt1:[0-9a-f]{64}$' AND basis_digest ~ '^zt1:[0-9a-f]{64}$'),
  CONSTRAINT regulatory_workpaper_build_text_length CHECK (length(formula_version) BETWEEN 1 AND 200
    AND length(content_release) BETWEEN 1 AND 200 AND length(output_canonical) >= 2)
);

CREATE TABLE ztax.regulatory_workpaper_event (
  tenant_id              uuid        NOT NULL,
  workpaper_id           uuid        NOT NULL,
  seq                    integer     NOT NULL,
  kind                   text        NOT NULL,
  status_after           text        NOT NULL,
  note                   text        NULL,
  build_version          integer     NULL,
  content_release        text        NULL,
  fx_currency            text        NULL,
  fx_rate                numeric     NULL,
  fx_rate_source         text        NULL,
  fx_rate_date           date        NULL,
  fx_rounding_mode       text        NULL,
  fx_rounding_scale      integer     NULL,
  fx_conversion_basis    text        NULL,
  adjustment_number      integer     NULL,
  adjustment_kind        text        NULL,
  adjustment_category    text        NULL,
  adjustment_amount      numeric     NULL,
  decision               text        NULL,
  exception_key          text        NULL,
  exception_fingerprint  text        NULL,
  evidence_sha256        text        NULL,
  evidence_name          text        NULL,
  evidence_media         text        NULL,
  recorded_at            timestamptz NOT NULL,
  recorded_by            uuid        NOT NULL,
  PRIMARY KEY (tenant_id, workpaper_id, seq),
  FOREIGN KEY (tenant_id, workpaper_id) REFERENCES ztax.regulatory_workpaper (tenant_id, workpaper_id),
  CONSTRAINT regulatory_workpaper_event_kind_known CHECK (kind IN ('CREATED', 'BUILT', 'APPROVAL_REVOKED',
    'BASIS_CHANGED', 'FX_DOCUMENTED', 'ADJUSTMENT_PROPOSED', 'ADJUSTMENT_DECIDED', 'RESOLUTION_PROPOSED',
    'RESOLUTION_DECIDED', 'FROZEN', 'RETURNED', 'APPROVED')),
  CONSTRAINT regulatory_workpaper_event_created_first CHECK ((kind = 'CREATED') = (seq = 1)),
  CONSTRAINT regulatory_workpaper_event_status_known CHECK (status_after IN ('DRAFT', 'DATA_INCOMPLETE',
    'READY_FOR_REVIEW', 'APPROVED', 'REVOKED')),
  -- A build, a freeze and an approval name the build version they are of.
  CONSTRAINT regulatory_workpaper_event_build_named CHECK (kind NOT IN ('BUILT', 'FROZEN', 'APPROVED')
    OR build_version >= 1),
  CONSTRAINT regulatory_workpaper_event_status_follows CHECK (
    (kind <> 'FROZEN' OR status_after = 'READY_FOR_REVIEW')
    AND (kind <> 'APPROVED' OR status_after = 'APPROVED')
    AND (kind <> 'APPROVAL_REVOKED' OR status_after = 'REVOKED')),
  CONSTRAINT regulatory_workpaper_event_basis CHECK ((kind = 'BASIS_CHANGED') = (content_release IS NOT NULL)),
  -- A documented rate carries its source, date and conversion basis.
  CONSTRAINT regulatory_workpaper_event_fx_whole CHECK ((kind = 'FX_DOCUMENTED') = (fx_currency IS NOT NULL)
    AND (fx_currency IS NULL OR (fx_currency ~ '^[A-Z]{3}$' AND fx_rate > 0 AND fx_rate_source IS NOT NULL
      AND fx_rate_date IS NOT NULL AND fx_rounding_scale BETWEEN 0 AND 12 AND fx_conversion_basis IS NOT NULL
      AND fx_rounding_mode IN ('HALF_UP', 'HALF_EVEN', 'HALF_DOWN', 'DOWN', 'UP', 'CEILING', 'FLOOR')))),
  -- An adjustment is evidence-linked; an allowance is a positive deduction.
  CONSTRAINT regulatory_workpaper_event_adjustment_whole CHECK (
    (kind = 'ADJUSTMENT_PROPOSED') = (adjustment_kind IS NOT NULL)
    AND (kind NOT IN ('ADJUSTMENT_PROPOSED', 'ADJUSTMENT_DECIDED') OR adjustment_number >= 1)
    AND (adjustment_kind IS NULL OR (adjustment_kind IN ('PERMITTED_ALLOWANCE', 'AUDITED_ADJUSTMENT')
      AND adjustment_category IS NOT NULL AND adjustment_amount IS NOT NULL AND adjustment_amount <> 0
      AND (adjustment_kind <> 'PERMITTED_ALLOWANCE' OR adjustment_amount > 0)
      AND evidence_sha256 IS NOT NULL AND note IS NOT NULL))),
  CONSTRAINT regulatory_workpaper_event_resolution_whole CHECK (
    (kind IN ('RESOLUTION_PROPOSED', 'RESOLUTION_DECIDED')) = (exception_key IS NOT NULL)
    AND (kind <> 'RESOLUTION_PROPOSED' OR (evidence_sha256 IS NOT NULL AND note IS NOT NULL
      AND exception_fingerprint IS NOT NULL))),
  CONSTRAINT regulatory_workpaper_event_decision CHECK (
    (kind IN ('ADJUSTMENT_DECIDED', 'RESOLUTION_DECIDED')) = (decision IS NOT NULL)
    AND (decision IS NULL OR decision IN ('APPROVED', 'REJECTED'))),
  CONSTRAINT regulatory_workpaper_event_evidence_shape CHECK ((evidence_sha256 IS NULL
      OR evidence_sha256 ~ '^[0-9a-f]{64}$')
    AND ((evidence_sha256 IS NULL) = (evidence_name IS NULL))
    AND ((evidence_sha256 IS NULL) = (evidence_media IS NULL))),
  CONSTRAINT regulatory_workpaper_event_text_length CHECK ((note IS NULL OR length(note) BETWEEN 1 AND 2000)
    AND (content_release IS NULL OR length(content_release) BETWEEN 1 AND 200)
    AND (fx_rate_source IS NULL OR length(fx_rate_source) BETWEEN 1 AND 200)
    AND (fx_conversion_basis IS NULL OR length(fx_conversion_basis) BETWEEN 1 AND 2000)
    AND (adjustment_category IS NULL OR length(adjustment_category) BETWEEN 1 AND 200)
    AND (exception_key IS NULL OR length(exception_key) BETWEEN 1 AND 200)
    AND (evidence_name IS NULL OR length(evidence_name) BETWEEN 1 AND 255))
);
CREATE INDEX regulatory_workpaper_event_evidence ON ztax.regulatory_workpaper_event (tenant_id, evidence_sha256)
  WHERE evidence_sha256 IS NOT NULL;

REVOKE UPDATE, DELETE, TRUNCATE ON ztax.regulatory_workpaper FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.regulatory_workpaper_build FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.regulatory_workpaper_event FROM ztax_app;

ALTER TABLE ztax.regulatory_workpaper ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.regulatory_workpaper
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.regulatory_workpaper_build ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.regulatory_workpaper_build
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.regulatory_workpaper_event ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.regulatory_workpaper_event
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

COMMENT ON TABLE ztax.regulatory_workpaper IS
  'One regulatory workpaper header per legal filer, duty and period. Immutable; its state is its event history.';
COMMENT ON TABLE ztax.regulatory_workpaper_build IS
  'Each deterministic build of a workpaper: inputs hash, canonical output and output hash. Append-only.';
COMMENT ON TABLE ztax.regulatory_workpaper_event IS
  'Builds, FX documentation, evidence-linked adjustments and resolutions, freezes, returns, approvals and revocations, in order. Append-only.';
