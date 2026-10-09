-- Local development only. See 000004's header: production is forward-only.
-- Restores 000003's sketch tables, which 000016 replaced.
DROP TABLE IF EXISTS ztax.fiscal_document_status;
DROP TABLE IF EXISTS ztax.fiscal_line_tax;
DROP TABLE IF EXISTS ztax.fiscal_line;
DROP TABLE IF EXISTS ztax.fiscal_document_decision;
DROP TABLE IF EXISTS ztax.fiscal_document_predecessor;
DROP TABLE IF EXISTS ztax.fiscal_document;

-- ---------------------------------------------------------------------------
CREATE TABLE ztax.fiscal_document (
  tenant_id     uuid        NOT NULL REFERENCES ztax.tenant (tenant_id),
  document_id   uuid        NOT NULL,
  decision_id   uuid        NOT NULL,

  business_key  text        NOT NULL,
  valid_from    timestamptz NOT NULL,
  valid_to      timestamptz NULL,
  recorded_at   timestamptz NOT NULL,
  supersedes_id uuid        NULL,

  document_type text        NOT NULL,
  -- external_ref is ADR-0012 §2.5: never a key, never parsed, never assumed
  -- unique. Stored as three columns so nothing is tempted to concatenate them.
  ext_source_system text    NULL,
  ext_namespace     text    NULL,
  ext_value         text    NULL,

  currency      text        NOT NULL,
  net_total     numeric     NOT NULL,
  tax_total     numeric     NOT NULL,
  gross_total   numeric     NOT NULL,

  event_time    timestamptz NOT NULL,

  PRIMARY KEY (tenant_id, document_id),
  CONSTRAINT fiscal_document_type_known CHECK (
    document_type IN ('INVOICE', 'CREDIT_NOTE', 'ADJUSTMENT', 'REFUND'))
);

CREATE INDEX fiscal_document_as_of
  ON ztax.fiscal_document (tenant_id, business_key, recorded_at DESC);

-- ADR-0012 §2.5: uniqueness on an external reference, where required, is a
-- tenant-scoped constraint on the triple. A collision is a data-quality finding
-- rather than a crash, so this is an index the application consults, not a
-- constraint that aborts a transaction.
CREATE INDEX fiscal_document_by_external_ref
  ON ztax.fiscal_document (tenant_id, ext_source_system, ext_namespace, ext_value)
  WHERE ext_value IS NOT NULL;

CREATE TABLE ztax.fiscal_line (
  tenant_id     uuid        NOT NULL,
  line_id       uuid        NOT NULL,
  document_id   uuid        NOT NULL,
  -- ordinal is part of the canonical input and is what allocation ties break
  -- on (ADR-0002 §2.6). It is dense and starts at zero.
  ordinal       int         NOT NULL,

  item_ref      text        NULL,
  classification_id uuid    NULL,

  quantity      numeric     NOT NULL,
  unit          text        NOT NULL,
  currency      text        NOT NULL,
  net_amount    numeric     NOT NULL,
  tax_amount    numeric     NOT NULL,

  -- The rounding policy that produced tax_amount, recorded by name
  -- (ADR-0002 §5.1 control 3). A decision that cannot name its rounding does
  -- not replay, so these are NOT NULL.
  rounding_mode  text       NOT NULL,
  rounding_scale int        NOT NULL,
  rounding_basis text       NOT NULL,

  PRIMARY KEY (tenant_id, line_id),
  FOREIGN KEY (tenant_id, document_id) REFERENCES ztax.fiscal_document (tenant_id, document_id),
  CONSTRAINT fiscal_line_rounding_mode_known CHECK (
    rounding_mode IN ('HALF_UP', 'HALF_EVEN', 'HALF_DOWN', 'DOWN', 'UP', 'CEILING', 'FLOOR')),
  CONSTRAINT fiscal_line_rounding_basis_known CHECK (
    rounding_basis IN ('LINE', 'DOCUMENT', 'TAX_COMPONENT', 'JURISDICTION_TOTAL')),
  CONSTRAINT fiscal_line_rounding_scale_range CHECK (rounding_scale BETWEEN 0 AND 12),
  CONSTRAINT fiscal_line_ordinal_non_negative CHECK (ordinal >= 0)
);

CREATE UNIQUE INDEX fiscal_line_ordinal_key
  ON ztax.fiscal_line (tenant_id, document_id, ordinal);
