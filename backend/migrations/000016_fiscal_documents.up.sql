-- Fiscal documents (W2 lane J; ZTAX-FIN-001 §3–§6).
--
-- 000003 sketched fiscal_document and fiscal_line in W0, before FIN-001 was
-- drafted: one decision per document, no lineage, rounding columns the domain
-- does not carry. Nothing ever wrote to them. They are replaced here by the
-- shape FIN-001 and internal/domain/document describe — dropped and created,
-- because there is no row to migrate and a table that keeps the wrong shape
-- for compatibility with nothing is a trap for the next writer.
--
-- Every table below is append-only. A committed document is never changed;
-- a void or a credit is a new document naming its predecessor, and what
-- happens to a document afterwards is a row in fiscal_document_status.

DROP TABLE ztax.fiscal_line;
DROP TABLE ztax.fiscal_document;

CREATE TABLE ztax.fiscal_document (
  tenant_id          uuid        NOT NULL REFERENCES ztax.tenant (tenant_id),
  document_id        uuid        NOT NULL,
  legal_entity_id    uuid        NOT NULL,
  document_type      text        NOT NULL,
  -- The legal, customer-visible number, apart from the canonical id
  -- (ZTAX-FIN-REQ-0006). May be absent until an authority assigns one.
  document_number    text        NULL,
  -- An original is its own root; every correction shares its original's
  -- (ZTAX-FIN-REQ-0007).
  root_id            uuid        NOT NULL,
  reason_code        text        NULL,
  -- Issue date and tax point are separate facts (ZTAX-FIN-REQ-0009).
  issue_date         date        NOT NULL,
  tax_point          timestamptz NOT NULL,
  currency           text        NOT NULL,
  restated           boolean     NOT NULL DEFAULT false,
  -- ADR-0012 §2.5 and ZTAX-DOM-REQ-0003: another system's reference, never a
  -- key, stored as its three parts.
  ext_source_system  text        NULL,
  ext_namespace      text        NULL,
  ext_value          text        NULL,
  net_total          numeric     NOT NULL,
  tax_total          numeric     NOT NULL,
  gross_total        numeric     NOT NULL,
  recorded_at        timestamptz NOT NULL,
  recorded_by        uuid        NULL,
  PRIMARY KEY (tenant_id, document_id),
  FOREIGN KEY (tenant_id, legal_entity_id) REFERENCES ztax.legal_entity (tenant_id, legal_entity_id),
  FOREIGN KEY (tenant_id, root_id) REFERENCES ztax.fiscal_document (tenant_id, document_id),
  CONSTRAINT fiscal_document_type_known CHECK (document_type IN ('INVOICE', 'CREDIT_NOTE', 'DEBIT_NOTE',
    'PARTIAL_CREDIT', 'VOID', 'REFUND', 'REBILL', 'ADJUSTMENT', 'AMENDMENT', 'RESTATEMENT')),
  CONSTRAINT fiscal_document_currency_shape CHECK (currency ~ '^[A-Z]{3}$'),
  CONSTRAINT fiscal_document_external_whole CHECK (
    (ext_source_system IS NULL) = (ext_namespace IS NULL) AND (ext_namespace IS NULL) = (ext_value IS NULL)),
  CONSTRAINT fiscal_document_restated_only_restatement CHECK (restated = (document_type = 'RESTATEMENT'))
);

CREATE INDEX fiscal_document_by_root ON ztax.fiscal_document (tenant_id, root_id, recorded_at);
-- A collision on an external reference is a data-quality finding, not a
-- constraint violation (ADR-0012 §2.5): an index to consult, not a key.
CREATE INDEX fiscal_document_by_external_ref
  ON ztax.fiscal_document (tenant_id, ext_source_system, ext_namespace, ext_value)
  WHERE ext_value IS NOT NULL;

CREATE TABLE ztax.fiscal_document_predecessor (
  tenant_id       uuid NOT NULL,
  document_id     uuid NOT NULL,
  predecessor_id  uuid NOT NULL,
  PRIMARY KEY (tenant_id, document_id, predecessor_id),
  FOREIGN KEY (tenant_id, document_id) REFERENCES ztax.fiscal_document (tenant_id, document_id),
  FOREIGN KEY (tenant_id, predecessor_id) REFERENCES ztax.fiscal_document (tenant_id, document_id)
);

-- The TaxDecisions a document pins (ZTAX-FIN-REQ-0010).
CREATE TABLE ztax.fiscal_document_decision (
  tenant_id    uuid NOT NULL,
  document_id  uuid NOT NULL,
  decision_id  uuid NOT NULL,
  PRIMARY KEY (tenant_id, document_id, decision_id),
  FOREIGN KEY (tenant_id, document_id) REFERENCES ztax.fiscal_document (tenant_id, document_id),
  FOREIGN KEY (tenant_id, decision_id) REFERENCES ztax.tax_decision (tenant_id, decision_id)
);
CREATE INDEX fiscal_document_decision_by_decision ON ztax.fiscal_document_decision (tenant_id, decision_id);

CREATE TABLE ztax.fiscal_line (
  tenant_id           uuid    NOT NULL,
  line_id             uuid    NOT NULL,
  document_id         uuid    NOT NULL,
  ordinal             integer NOT NULL,
  -- The billing or ERP line it came from (ZTAX-FIN-REQ-0020).
  source_line_ref     text    NULL,
  component_instance  text    NULL,
  net_amount          numeric NOT NULL,
  discount_amount     numeric NULL,
  allocation_ref      text    NULL,
  -- The line this one corrects (ZTAX-FIN-REQ-0013).
  predecessor_line_id uuid    NULL,
  PRIMARY KEY (tenant_id, line_id),
  FOREIGN KEY (tenant_id, document_id) REFERENCES ztax.fiscal_document (tenant_id, document_id),
  FOREIGN KEY (tenant_id, predecessor_line_id) REFERENCES ztax.fiscal_line (tenant_id, line_id),
  CONSTRAINT fiscal_line_ordinal_non_negative CHECK (ordinal >= 0),
  CONSTRAINT fiscal_line_discount_allocated CHECK (discount_amount IS NULL OR allocation_ref IS NOT NULL)
);
CREATE UNIQUE INDEX fiscal_line_ordinal_key ON ztax.fiscal_line (tenant_id, document_id, ordinal);
CREATE INDEX fiscal_line_by_predecessor ON ztax.fiscal_line (tenant_id, predecessor_line_id)
  WHERE predecessor_line_id IS NOT NULL;

-- One tax on one line, from one pinned decision (ZTAX-FIN-REQ-0011).
CREATE TABLE ztax.fiscal_line_tax (
  tenant_id    uuid    NOT NULL,
  line_id      uuid    NOT NULL,
  ordinal      integer NOT NULL,
  decision_id  uuid    NOT NULL,
  component    text    NOT NULL,
  amount       numeric NOT NULL,
  PRIMARY KEY (tenant_id, line_id, ordinal),
  FOREIGN KEY (tenant_id, line_id) REFERENCES ztax.fiscal_line (tenant_id, line_id),
  FOREIGN KEY (tenant_id, decision_id) REFERENCES ztax.tax_decision (tenant_id, decision_id)
);

CREATE TABLE ztax.fiscal_document_status (
  tenant_id     uuid        NOT NULL,
  document_id   uuid        NOT NULL,
  seq           integer     NOT NULL,
  status        text        NOT NULL,
  -- The document whose commit caused the move, where one did.
  cause_id      uuid        NULL,
  recorded_at   timestamptz NOT NULL,
  recorded_by   uuid        NULL,
  PRIMARY KEY (tenant_id, document_id, seq),
  FOREIGN KEY (tenant_id, document_id) REFERENCES ztax.fiscal_document (tenant_id, document_id),
  FOREIGN KEY (tenant_id, cause_id) REFERENCES ztax.fiscal_document (tenant_id, document_id),
  CONSTRAINT fiscal_document_status_known CHECK (status IN ('COMMITTED', 'ISSUED', 'DELIVERED', 'ACCEPTED',
    'PARTIALLY_CREDITED', 'FULLY_CREDITED', 'VOIDED', 'REFUNDED', 'AMENDED', 'DISPUTED', 'CLOSED', 'SUSPENDED')),
  CONSTRAINT fiscal_document_status_first_is_commit CHECK (seq > 1 OR (seq = 1 AND status = 'COMMITTED'))
);

REVOKE UPDATE, DELETE, TRUNCATE ON ztax.fiscal_document FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.fiscal_document_predecessor FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.fiscal_document_decision FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.fiscal_line FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.fiscal_line_tax FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.fiscal_document_status FROM ztax_app;

COMMENT ON TABLE ztax.fiscal_document IS
  'A committed fiscal document. Append-only; corrections are new documents sharing the root.';
COMMENT ON TABLE ztax.fiscal_document_status IS
  'What happened to a document after it was committed, in order. Append-only.';
