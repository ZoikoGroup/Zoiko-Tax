-- Legal-entity registrations and exemption certificate evidence
-- (Engineering Doc 17, G-048; Doc 08 OBL-02; Doc 15 OPS-13, OPS-14).
--
-- Two ledgers, each a row per record and an append-only event table:
--
--   entity_registration   a registration a legal entity holds, or is applying
--                         for, with an authority: scheme, number, effective
--                         dates. Its status — DRAFT, SUBMITTED, VERIFIED,
--                         ACTIVE, SUSPENDED, EXPIRED, REVOKED, WITHDRAWN — is
--                         the fold of its events. VERIFIED only on a recorded
--                         authority check with the authority's own reference;
--                         ACTIVE only on a second person's activation.
--   exemption_certificate a buyer's exemption certificate given to a seller
--                         entity, held as evidence. Every replacement document
--                         is a new evidence version; approval is a
--                         recommendation and a different person's
--                         confirmation of that exact version.
--
-- This is not the obligation engine's registration duty (NOT_REQUIRED …
-- DEREGISTERED), and nothing here references or changes a fiscal table: an
-- approved certificate or an active registration is evidence, never a tax
-- treatment (Doc 17 §01).
--
-- Documents are recorded by SHA-256, name, media type and size. The file is
-- hashed in the browser and never uploaded or stored.
--
-- intake_key and intake_hash make intake idempotent on the Idempotency-Key:
-- the same key and request return the same record; the same key with a
-- different request is refused (Doc 17 ER-027).

CREATE TABLE ztax.entity_registration (
  tenant_id           uuid        NOT NULL REFERENCES ztax.tenant (tenant_id),
  registration_id     uuid        NOT NULL,
  legal_entity_id     uuid        NOT NULL,
  scheme              text        NOT NULL,
  jurisdiction        text        NOT NULL,
  authority           text        NOT NULL,
  registration_number text        NOT NULL,
  effective_from      date        NOT NULL,
  effective_to        date        NULL,
  supersedes          uuid        NULL,
  intake_key          text        NOT NULL,
  intake_hash         text        NOT NULL,
  created_at          timestamptz NOT NULL,
  created_by          uuid        NOT NULL,
  PRIMARY KEY (tenant_id, registration_id),
  FOREIGN KEY (tenant_id, legal_entity_id) REFERENCES ztax.legal_entity (tenant_id, legal_entity_id),
  FOREIGN KEY (tenant_id, supersedes) REFERENCES ztax.entity_registration (tenant_id, registration_id),
  CONSTRAINT entity_registration_scheme_known CHECK (scheme IN ('US_FCC_FRN', 'US_USAC_499_FILER_ID',
    'US_USAC_498_ID', 'US_STATE_SALES_TAX_PERMIT', 'US_STATE_TELECOM_REGISTRATION', 'UK_VAT', 'EU_VAT', 'CA_GST_HST',
    'OTHER_REGISTRATION')),
  CONSTRAINT entity_registration_jurisdiction_shape CHECK (jurisdiction ~ '^[A-Z]{2}(-[A-Z0-9]{1,3})?$'),
  CONSTRAINT entity_registration_dates CHECK (effective_to IS NULL OR effective_to >= effective_from),
  CONSTRAINT entity_registration_not_self CHECK (supersedes IS NULL OR supersedes <> registration_id),
  CONSTRAINT entity_registration_hash_shape CHECK (intake_hash ~ '^zt1:[0-9a-f]{64}$'),
  CONSTRAINT entity_registration_text_length CHECK (length(authority) BETWEEN 1 AND 128
    AND length(registration_number) BETWEEN 1 AND 64 AND length(intake_key) BETWEEN 1 AND 255)
);
CREATE UNIQUE INDEX entity_registration_intake_once ON ztax.entity_registration (tenant_id, intake_key);
CREATE INDEX entity_registration_number ON ztax.entity_registration (tenant_id, scheme, registration_number);
CREATE INDEX entity_registration_recent ON ztax.entity_registration (tenant_id, created_at DESC, registration_id);

CREATE TABLE ztax.entity_registration_event (
  tenant_id          uuid        NOT NULL,
  registration_id    uuid        NOT NULL,
  seq                integer     NOT NULL,
  kind               text        NOT NULL,
  status_after       text        NOT NULL,
  reason             text        NULL,
  check_source       text        NULL,
  check_method       text        NULL,
  check_outcome      text        NULL,
  check_reference    text        NULL,
  checked_at         timestamptz NULL,
  receipt_sha256     text        NULL,
  attachment_sha256  text        NULL,
  attachment_name    text        NULL,
  attachment_media   text        NULL,
  attachment_bytes   bigint      NULL,
  recorded_at        timestamptz NOT NULL,
  recorded_by        uuid        NOT NULL,
  PRIMARY KEY (tenant_id, registration_id, seq),
  FOREIGN KEY (tenant_id, registration_id) REFERENCES ztax.entity_registration (tenant_id, registration_id),
  CONSTRAINT entity_registration_event_kind_known CHECK (kind IN ('CREATED', 'SUBMITTED', 'WITHDRAWN', 'RETURNED',
    'CHECK_RECORDED', 'ACTIVATED', 'SUSPENDED', 'REINSTATED', 'EXPIRED', 'REVOKED', 'ATTACHMENT_RECORDED')),
  CONSTRAINT entity_registration_event_created_first CHECK ((kind = 'CREATED') = (seq = 1)),
  CONSTRAINT entity_registration_event_status_known CHECK (status_after IN ('DRAFT', 'SUBMITTED', 'VERIFIED',
    'ACTIVE', 'SUSPENDED', 'EXPIRED', 'REVOKED', 'WITHDRAWN')),
  -- A check is a person's lookup, never coerced: only VERIFIED and NEGATIVE
  -- claim an answer, and both carry the authority's reference.
  CONSTRAINT entity_registration_event_check_whole CHECK ((kind = 'CHECK_RECORDED') = (check_outcome IS NOT NULL)
    AND (check_outcome IS NULL OR (check_source IS NOT NULL AND check_method = 'MANUAL_LOOKUP' AND checked_at IS NOT NULL
      AND check_outcome IN ('VERIFIED', 'NEGATIVE', 'INCONCLUSIVE', 'UNAVAILABLE')
      AND check_source IN ('FCC', 'USAC', 'HMRC', 'VIES', 'CRA', 'STATE_REVENUE_AUTHORITY', 'OTHER_AUTHORITY')))),
  CONSTRAINT entity_registration_event_check_referenced CHECK (check_outcome IS NULL
    OR check_outcome NOT IN ('VERIFIED', 'NEGATIVE') OR check_reference IS NOT NULL),
  -- VERIFIED is reached only by a check that returned VERIFIED.
  CONSTRAINT entity_registration_event_verified_by_check CHECK (status_after <> 'VERIFIED'
    OR kind IN ('CHECK_RECORDED', 'ATTACHMENT_RECORDED')),
  CONSTRAINT entity_registration_event_attachment_whole CHECK ((kind = 'ATTACHMENT_RECORDED') = (attachment_sha256 IS NOT NULL)
    AND ((attachment_sha256 IS NULL) = (attachment_name IS NULL))
    AND ((attachment_sha256 IS NULL) = (attachment_media IS NULL))
    AND ((attachment_sha256 IS NULL) = (attachment_bytes IS NULL))
    AND (attachment_sha256 IS NULL OR attachment_sha256 ~ '^[0-9a-f]{64}$')
    AND (receipt_sha256 IS NULL OR receipt_sha256 ~ '^[0-9a-f]{64}$')
    AND (attachment_bytes IS NULL OR attachment_bytes BETWEEN 1 AND 26214400)),
  CONSTRAINT entity_registration_event_reason_given CHECK (kind NOT IN ('WITHDRAWN', 'RETURNED', 'ACTIVATED',
    'SUSPENDED', 'REINSTATED', 'REVOKED') OR reason IS NOT NULL),
  CONSTRAINT entity_registration_event_text_length CHECK ((reason IS NULL OR length(reason) BETWEEN 1 AND 2000)
    AND (check_reference IS NULL OR length(check_reference) BETWEEN 1 AND 200)
    AND (attachment_name IS NULL OR length(attachment_name) BETWEEN 1 AND 255))
);

CREATE TABLE ztax.exemption_certificate (
  tenant_id           uuid        NOT NULL REFERENCES ztax.tenant (tenant_id),
  certificate_id      uuid        NOT NULL,
  legal_entity_id     uuid        NOT NULL,
  buyer_ref           text        NOT NULL,
  buyer_name          text        NOT NULL,
  certificate_type    text        NOT NULL,
  exemption_reason    text        NOT NULL,
  jurisdiction        text        NOT NULL,
  certificate_number  text        NULL,
  product_scope       text[]      NOT NULL,
  issued_on           date        NOT NULL,
  expires_on          date        NULL,
  supersedes          uuid        NULL,
  intake_key          text        NOT NULL,
  intake_hash         text        NOT NULL,
  created_at          timestamptz NOT NULL,
  created_by          uuid        NOT NULL,
  PRIMARY KEY (tenant_id, certificate_id),
  FOREIGN KEY (tenant_id, legal_entity_id) REFERENCES ztax.legal_entity (tenant_id, legal_entity_id),
  FOREIGN KEY (tenant_id, supersedes) REFERENCES ztax.exemption_certificate (tenant_id, certificate_id),
  CONSTRAINT exemption_certificate_type_known CHECK (certificate_type IN ('RESALE', 'WHOLESALE_TELECOM',
    'EXEMPT_ORGANIZATION', 'GOVERNMENT_ENTITY', 'DIPLOMATIC', 'DIRECT_PAY_PERMIT', 'OTHER_EXEMPTION')),
  CONSTRAINT exemption_certificate_jurisdiction_shape CHECK (jurisdiction ~ '^[A-Z]{2}(-[A-Z0-9]{1,3})?$'),
  CONSTRAINT exemption_certificate_dates CHECK (expires_on IS NULL OR expires_on >= issued_on),
  CONSTRAINT exemption_certificate_not_self CHECK (supersedes IS NULL OR supersedes <> certificate_id),
  CONSTRAINT exemption_certificate_scope_bounded CHECK (cardinality(product_scope) <= 20),
  CONSTRAINT exemption_certificate_hash_shape CHECK (intake_hash ~ '^zt1:[0-9a-f]{64}$'),
  CONSTRAINT exemption_certificate_text_length CHECK (length(buyer_ref) BETWEEN 1 AND 200
    AND length(buyer_name) BETWEEN 1 AND 200 AND length(exemption_reason) BETWEEN 1 AND 500
    AND (certificate_number IS NULL OR length(certificate_number) BETWEEN 1 AND 200)
    AND length(intake_key) BETWEEN 1 AND 255)
);
CREATE UNIQUE INDEX exemption_certificate_intake_once ON ztax.exemption_certificate (tenant_id, intake_key);
CREATE INDEX exemption_certificate_buyer ON ztax.exemption_certificate (tenant_id, legal_entity_id, buyer_ref);
CREATE INDEX exemption_certificate_recent ON ztax.exemption_certificate (tenant_id, created_at DESC, certificate_id);

CREATE TABLE ztax.exemption_certificate_event (
  tenant_id          uuid        NOT NULL,
  certificate_id     uuid        NOT NULL,
  seq                integer     NOT NULL,
  kind               text        NOT NULL,
  status_after       text        NOT NULL,
  evidence_version   integer     NOT NULL,
  note               text        NULL,
  document_sha256    text        NULL,
  document_name      text        NULL,
  document_media     text        NULL,
  document_bytes     bigint      NULL,
  screening          jsonb       NULL,
  screening_result   text        NULL,
  recommendation     text        NULL,
  effective_from     date        NULL,
  effective_to       date        NULL,
  decision           text        NULL,
  recorded_at        timestamptz NOT NULL,
  recorded_by        uuid        NOT NULL,
  PRIMARY KEY (tenant_id, certificate_id, seq),
  FOREIGN KEY (tenant_id, certificate_id) REFERENCES ztax.exemption_certificate (tenant_id, certificate_id),
  CONSTRAINT exemption_certificate_event_kind_known CHECK (kind IN ('RECEIVED', 'VERSION_ADDED', 'SCREENED',
    'RECOMMENDED', 'CONFIRMED', 'RETURNED', 'SUSPENDED', 'REINSTATED', 'EXPIRED', 'REVOKED', 'SUPERSEDED')),
  CONSTRAINT exemption_certificate_event_received_first CHECK ((kind = 'RECEIVED') = (seq = 1)),
  CONSTRAINT exemption_certificate_event_status_known CHECK (status_after IN ('SUBMITTED', 'SCREENING',
    'REVIEW_REQUIRED', 'APPROVED', 'REJECTED', 'EXPIRED', 'SUSPENDED', 'REVOKED', 'SUPERSEDED')),
  CONSTRAINT exemption_certificate_event_version_positive CHECK (evidence_version >= 1),
  -- Every evidence version is a document.
  CONSTRAINT exemption_certificate_event_document_whole CHECK ((kind IN ('RECEIVED', 'VERSION_ADDED')) = (document_sha256 IS NOT NULL)
    AND ((document_sha256 IS NULL) = (document_name IS NULL))
    AND ((document_sha256 IS NULL) = (document_media IS NULL))
    AND ((document_sha256 IS NULL) = (document_bytes IS NULL))
    AND (document_sha256 IS NULL OR document_sha256 ~ '^[0-9a-f]{64}$')
    AND (document_bytes IS NULL OR document_bytes BETWEEN 1 AND 26214400)),
  CONSTRAINT exemption_certificate_event_screening_whole CHECK ((kind = 'SCREENED') = (screening IS NOT NULL)
    AND ((screening IS NULL) = (screening_result IS NULL))
    AND (screening IS NULL OR jsonb_typeof(screening) = 'object')
    AND (screening_result IS NULL OR screening_result IN ('DOCUMENT_COMPLETE', 'INCOMPLETE', 'INCONSISTENT',
      'NEEDS_LEGAL_DECISION'))),
  CONSTRAINT exemption_certificate_event_recommendation_whole CHECK ((kind = 'RECOMMENDED') = (recommendation IS NOT NULL)
    AND (recommendation IS NULL OR recommendation IN ('APPROVE', 'REJECT'))
    AND ((recommendation = 'APPROVE') = (effective_from IS NOT NULL))
    AND (effective_to IS NULL OR (effective_from IS NOT NULL AND effective_to >= effective_from))),
  CONSTRAINT exemption_certificate_event_decision_whole CHECK ((kind IN ('CONFIRMED', 'RETURNED')) = (decision IS NOT NULL)
    AND (decision IS NULL OR (kind = 'CONFIRMED' AND decision = 'CONFIRM') OR (kind = 'RETURNED' AND decision = 'RETURN'))),
  -- APPROVED is reached only by a confirmation or a reinstatement.
  CONSTRAINT exemption_certificate_event_approved_by_checker CHECK (status_after <> 'APPROVED'
    OR kind IN ('CONFIRMED', 'REINSTATED')),
  CONSTRAINT exemption_certificate_event_reason_given CHECK (kind NOT IN ('RECOMMENDED', 'RETURNED', 'SUSPENDED',
    'REINSTATED', 'REVOKED', 'SUPERSEDED') OR note IS NOT NULL),
  CONSTRAINT exemption_certificate_event_text_length CHECK ((note IS NULL OR length(note) BETWEEN 1 AND 2000)
    AND (document_name IS NULL OR length(document_name) BETWEEN 1 AND 255))
);
CREATE INDEX exemption_certificate_event_document ON ztax.exemption_certificate_event (tenant_id, document_sha256)
  WHERE document_sha256 IS NOT NULL;

REVOKE UPDATE, DELETE, TRUNCATE ON ztax.entity_registration FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.entity_registration_event FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.exemption_certificate FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.exemption_certificate_event FROM ztax_app;

ALTER TABLE ztax.entity_registration ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.entity_registration
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.entity_registration_event ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.entity_registration_event
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.exemption_certificate ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.exemption_certificate
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.exemption_certificate_event ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.exemption_certificate_event
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

COMMENT ON TABLE ztax.entity_registration IS
  'A registration a legal entity holds or is applying for: scheme, number, effective dates, as recorded. Immutable; its status is its event history. Evidence of status, never a tax treatment.';
COMMENT ON TABLE ztax.entity_registration_event IS
  'Submission, authority checks, activation, suspension, expiry, revocation and supporting documents of a registration, in order. Append-only.';
COMMENT ON TABLE ztax.exemption_certificate IS
  'An exemption certificate given by a buyer to a seller entity, held as evidence. Immutable; its status and evidence versions are its event history. Approval changes no tax result.';
COMMENT ON TABLE ztax.exemption_certificate_event IS
  'Document versions, screening, maker recommendation, checker confirmation, suspension, expiry, revocation and supersession of a certificate, in order. Append-only.';
