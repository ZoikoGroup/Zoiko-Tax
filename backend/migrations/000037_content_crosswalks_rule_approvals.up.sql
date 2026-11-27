-- Vendor crosswalks and the rule approval queue (Engineering Doc 02 WF-06,
-- WF-07, §06 vendor_crosswalk; Doc 12 FED-04; Doc 03 §07; CONT-001 §3
-- ContentChange, §8; Sidebar Navigation Architecture WF-02).
--
--   content_crosswalk          one vendor code namespace at one vendor
--                              release. Unique per tenant: a change is the
--                              next version, never a second crosswalk.
--   content_crosswalk_version  one immutable set of mappings, the grant the
--                              vendor data is held under (rights basis,
--                              reference, end date) and the effective window.
--                              content_digest is what an approval approves.
--   content_crosswalk_entry    the mappings of a version: external code,
--                              vendor meaning, match quality (EXACT, PARTIAL,
--                              NO_EQUIVALENT), canonical service target,
--                              optional levy hint and predicate.
--   content_crosswalk_event    a version's lifecycle, appended: DRAFT,
--                              IN_REVIEW, APPROVED by a second person, and on.
--   content_rule_change        a proposed change to a rule pack, its source
--                              citation and authority class, and its three
--                              clocks (published, effective, recorded).
--   content_rule_change_event  its review: test evidence, recommendation by
--                              a specialist other than the proposer, approval
--                              for release by a third person.
--
-- Nothing here references or changes a fiscal table, a rule bundle or a pack
-- activation. A crosswalk is a translation aid and never fiscal authority; an
-- approved rule change is APPROVED_FOR_RELEASE, eligible for a content
-- release, and nothing is published by approving it (WF-02).

CREATE TABLE ztax.content_crosswalk (
  tenant_id       uuid        NOT NULL REFERENCES ztax.tenant (tenant_id),
  crosswalk_id    uuid        NOT NULL,
  vendor          text        NOT NULL,
  namespace       text        NOT NULL,
  vendor_release  text        NOT NULL,
  title           text        NOT NULL,
  created_at      timestamptz NOT NULL,
  created_by      uuid        NOT NULL,
  PRIMARY KEY (tenant_id, crosswalk_id),
  CONSTRAINT content_crosswalk_vendor_shape CHECK (vendor ~ '^[A-Z0-9][A-Z0-9_.-]{0,63}$'
    AND namespace ~ '^[A-Z0-9][A-Z0-9_.-]{0,63}$'),
  CONSTRAINT content_crosswalk_text_length CHECK (length(vendor_release) BETWEEN 1 AND 64
    AND length(title) BETWEEN 1 AND 200)
);
CREATE UNIQUE INDEX content_crosswalk_identity ON ztax.content_crosswalk (tenant_id, vendor, namespace, vendor_release);
CREATE INDEX content_crosswalk_recent ON ztax.content_crosswalk (tenant_id, created_at DESC, crosswalk_id);

CREATE TABLE ztax.content_crosswalk_version (
  tenant_id              uuid        NOT NULL,
  crosswalk_id           uuid        NOT NULL,
  version                integer     NOT NULL,
  rights_basis           text        NOT NULL,
  rights_reference       text        NOT NULL,
  rights_expires_on      date        NULL,
  rights_evidence_sha256 text        NULL,
  effective_from         date        NOT NULL,
  effective_to           date        NULL,
  content_digest         text        NOT NULL,
  intake_key             text        NOT NULL,
  intake_hash            text        NOT NULL,
  created_at             timestamptz NOT NULL,
  created_by             uuid        NOT NULL,
  PRIMARY KEY (tenant_id, crosswalk_id, version),
  FOREIGN KEY (tenant_id, crosswalk_id) REFERENCES ztax.content_crosswalk (tenant_id, crosswalk_id),
  CONSTRAINT content_crosswalk_version_positive CHECK (version >= 1),
  CONSTRAINT content_crosswalk_version_rights_known CHECK (rights_basis IN ('VENDOR_LICENCE', 'CUSTOMER_SUPPLIED',
    'PUBLIC_DOCUMENTATION')),
  CONSTRAINT content_crosswalk_version_dates CHECK (effective_to IS NULL OR effective_to > effective_from),
  CONSTRAINT content_crosswalk_version_hash_shape CHECK (intake_hash ~ '^zt1:[0-9a-f]{64}$'
    AND content_digest ~ '^zt1:[0-9a-f]{64}$'
    AND (rights_evidence_sha256 IS NULL OR rights_evidence_sha256 ~ '^[0-9a-f]{64}$')),
  CONSTRAINT content_crosswalk_version_text_length CHECK (length(rights_reference) BETWEEN 1 AND 200
    AND length(intake_key) BETWEEN 1 AND 255)
);
CREATE UNIQUE INDEX content_crosswalk_version_intake_once ON ztax.content_crosswalk_version (tenant_id, intake_key);

CREATE TABLE ztax.content_crosswalk_entry (
  tenant_id      uuid    NOT NULL,
  crosswalk_id   uuid    NOT NULL,
  version        integer NOT NULL,
  ordinal        integer NOT NULL,
  external_code  text    NOT NULL,
  description    text    NOT NULL,
  match_quality  text    NOT NULL,
  service_target text    NULL,
  levy_hint      text    NULL,
  predicate      text    NULL,
  note           text    NULL,
  PRIMARY KEY (tenant_id, crosswalk_id, version, ordinal),
  FOREIGN KEY (tenant_id, crosswalk_id, version) REFERENCES ztax.content_crosswalk_version (tenant_id, crosswalk_id, version),
  CONSTRAINT content_crosswalk_entry_match_known CHECK (match_quality IN ('EXACT', 'PARTIAL', 'NO_EQUIVALENT')),
  -- A code with an equivalent names it; one without names nothing.
  CONSTRAINT content_crosswalk_entry_target_whole CHECK ((match_quality = 'NO_EQUIVALENT') = (service_target IS NULL)
    AND (service_target IS NULL OR service_target LIKE 'ontology:_%')
    AND (levy_hint IS NULL OR service_target IS NOT NULL)),
  -- A partial match says when it applies (WF-07 UNRESOLVED_AMBIGUITY).
  CONSTRAINT content_crosswalk_entry_partial_conditioned CHECK (match_quality <> 'PARTIAL' OR predicate IS NOT NULL),
  CONSTRAINT content_crosswalk_entry_text_length CHECK (length(external_code) BETWEEN 1 AND 128
    AND length(description) BETWEEN 1 AND 500
    AND (predicate IS NULL OR length(predicate) BETWEEN 1 AND 500)
    AND (note IS NULL OR length(note) BETWEEN 1 AND 500)
    AND (levy_hint IS NULL OR length(levy_hint) BETWEEN 1 AND 128))
);
-- The same code under the same predicate is mapped once per version.
CREATE UNIQUE INDEX content_crosswalk_entry_unambiguous ON ztax.content_crosswalk_entry
  (tenant_id, crosswalk_id, version, external_code, coalesce(predicate, ''));

CREATE TABLE ztax.content_crosswalk_event (
  tenant_id     uuid        NOT NULL,
  crosswalk_id  uuid        NOT NULL,
  version       integer     NOT NULL,
  seq           integer     NOT NULL,
  kind          text        NOT NULL,
  status_after  text        NOT NULL,
  reason        text        NULL,
  recorded_at   timestamptz NOT NULL,
  recorded_by   uuid        NOT NULL,
  PRIMARY KEY (tenant_id, crosswalk_id, version, seq),
  FOREIGN KEY (tenant_id, crosswalk_id, version) REFERENCES ztax.content_crosswalk_version (tenant_id, crosswalk_id, version),
  CONSTRAINT content_crosswalk_event_kind_known CHECK (kind IN ('CREATED', 'SUBMITTED', 'APPROVED', 'CHANGES_REQUESTED',
    'REJECTED', 'WITHDRAWN', 'SUSPENDED', 'REINSTATED', 'SUPERSEDED')),
  CONSTRAINT content_crosswalk_event_created_first CHECK ((kind = 'CREATED') = (seq = 1)),
  CONSTRAINT content_crosswalk_event_status_known CHECK (status_after IN ('DRAFT', 'IN_REVIEW', 'APPROVED',
    'CHANGES_REQUESTED', 'REJECTED', 'WITHDRAWN', 'SUSPENDED', 'SUPERSEDED')),
  -- APPROVED is reached only by an approval or a reinstatement.
  CONSTRAINT content_crosswalk_event_approved_by_review CHECK (status_after <> 'APPROVED'
    OR kind IN ('APPROVED', 'REINSTATED')),
  CONSTRAINT content_crosswalk_event_reason_given CHECK (kind IN ('CREATED', 'SUBMITTED') OR reason IS NOT NULL),
  CONSTRAINT content_crosswalk_event_text_length CHECK (reason IS NULL OR length(reason) BETWEEN 1 AND 2000)
);

CREATE TABLE ztax.content_rule_change (
  tenant_id         uuid        NOT NULL REFERENCES ztax.tenant (tenant_id),
  change_id         uuid        NOT NULL,
  pack_id           text        NOT NULL,
  base_version      text        NOT NULL,
  rule_id           text        NULL,
  jurisdiction      text        NOT NULL,
  change_type       text        NOT NULL,
  summary           text        NOT NULL,
  detail            text        NOT NULL,
  impact            text        NULL,
  source_id         text        NOT NULL,
  source_reference  text        NOT NULL,
  source_edition    text        NOT NULL,
  authority_class   text        NOT NULL,
  published_on      date        NULL,
  effective_from    date        NOT NULL,
  effective_to      date        NULL,
  origin_ref        text        NULL,
  supersedes        uuid        NULL,
  intake_key        text        NOT NULL,
  intake_hash       text        NOT NULL,
  created_at        timestamptz NOT NULL,
  created_by        uuid        NOT NULL,
  PRIMARY KEY (tenant_id, change_id),
  FOREIGN KEY (tenant_id, supersedes) REFERENCES ztax.content_rule_change (tenant_id, change_id),
  CONSTRAINT content_rule_change_pack_shape CHECK (pack_id ~ '^[A-Z]([A-Z0-9-]*[A-Z0-9])?$'
    AND base_version ~ '^[0-9]+\.[0-9]+\.[0-9]+$'
    AND (rule_id IS NULL OR rule_id ~ '^ZTAX-RULE-[A-Z0-9-]+$')),
  CONSTRAINT content_rule_change_jurisdiction_shape CHECK (jurisdiction ~ '^[A-Z]{2}(-[A-Z0-9]{1,3})?$'),
  CONSTRAINT content_rule_change_type_known CHECK (change_type IN ('RATE_CHANGE', 'BASE_CHANGE', 'NEW_LEVY',
    'LEVY_REPEAL', 'EXEMPTION_CHANGE', 'THRESHOLD_CHANGE', 'SITUS_CHANGE', 'RESPONSIBILITY_CHANGE',
    'OBLIGATION_CHANGE', 'OTHER')),
  CONSTRAINT content_rule_change_authority_known CHECK (authority_class IN ('A0', 'A1', 'A2', 'B1', 'B2', 'C1', 'U')),
  CONSTRAINT content_rule_change_dates CHECK (effective_to IS NULL OR effective_to > effective_from),
  CONSTRAINT content_rule_change_not_self CHECK (supersedes IS NULL OR supersedes <> change_id),
  CONSTRAINT content_rule_change_hash_shape CHECK (intake_hash ~ '^zt1:[0-9a-f]{64}$'),
  CONSTRAINT content_rule_change_text_length CHECK (length(summary) BETWEEN 1 AND 500
    AND length(detail) BETWEEN 1 AND 8000 AND (impact IS NULL OR length(impact) BETWEEN 1 AND 8000)
    AND length(source_id) BETWEEN 1 AND 200 AND length(source_reference) BETWEEN 1 AND 200
    AND length(source_edition) BETWEEN 1 AND 200 AND (origin_ref IS NULL OR length(origin_ref) BETWEEN 1 AND 200)
    AND length(intake_key) BETWEEN 1 AND 255)
);
CREATE UNIQUE INDEX content_rule_change_intake_once ON ztax.content_rule_change (tenant_id, intake_key);
CREATE UNIQUE INDEX content_rule_change_superseded_once ON ztax.content_rule_change (tenant_id, supersedes)
  WHERE supersedes IS NOT NULL;
CREATE INDEX content_rule_change_recent ON ztax.content_rule_change (tenant_id, created_at DESC, change_id);

CREATE TABLE ztax.content_rule_change_event (
  tenant_id             uuid        NOT NULL,
  change_id             uuid        NOT NULL,
  seq                   integer     NOT NULL,
  kind                  text        NOT NULL,
  status_after          text        NOT NULL,
  reason                text        NULL,
  test_evidence_ref     text        NULL,
  test_evidence_sha256  text        NULL,
  recorded_at           timestamptz NOT NULL,
  recorded_by           uuid        NOT NULL,
  PRIMARY KEY (tenant_id, change_id, seq),
  FOREIGN KEY (tenant_id, change_id) REFERENCES ztax.content_rule_change (tenant_id, change_id),
  CONSTRAINT content_rule_change_event_kind_known CHECK (kind IN ('PROPOSED', 'TEST_EVIDENCE_RECORDED', 'RECOMMENDED',
    'APPROVED', 'CHANGES_REQUESTED', 'REJECTED', 'WITHDRAWN', 'SUPERSEDED')),
  CONSTRAINT content_rule_change_event_proposed_first CHECK ((kind = 'PROPOSED') = (seq = 1)),
  CONSTRAINT content_rule_change_event_status_known CHECK (status_after IN ('PROPOSED', 'RECOMMENDED',
    'APPROVED_FOR_RELEASE', 'CHANGES_REQUESTED', 'REJECTED', 'WITHDRAWN', 'SUPERSEDED')),
  -- APPROVED_FOR_RELEASE is reached only by an approval. There is no
  -- published or active state here: release is a separate gate.
  CONSTRAINT content_rule_change_event_approved_by_approval CHECK ((status_after = 'APPROVED_FOR_RELEASE') = (kind = 'APPROVED')),
  CONSTRAINT content_rule_change_event_evidence_whole CHECK ((kind = 'TEST_EVIDENCE_RECORDED') = (test_evidence_ref IS NOT NULL)
    AND (test_evidence_sha256 IS NULL OR (test_evidence_ref IS NOT NULL AND test_evidence_sha256 ~ '^[0-9a-f]{64}$'))),
  CONSTRAINT content_rule_change_event_reason_given CHECK (kind IN ('PROPOSED', 'TEST_EVIDENCE_RECORDED') OR reason IS NOT NULL),
  CONSTRAINT content_rule_change_event_text_length CHECK ((reason IS NULL OR length(reason) BETWEEN 1 AND 2000)
    AND (test_evidence_ref IS NULL OR length(test_evidence_ref) BETWEEN 1 AND 200))
);

REVOKE UPDATE, DELETE, TRUNCATE ON ztax.content_crosswalk FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.content_crosswalk_version FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.content_crosswalk_entry FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.content_crosswalk_event FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.content_rule_change FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.content_rule_change_event FROM ztax_app;

ALTER TABLE ztax.content_crosswalk ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.content_crosswalk
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.content_crosswalk_version ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.content_crosswalk_version
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.content_crosswalk_entry ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.content_crosswalk_entry
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.content_crosswalk_event ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.content_crosswalk_event
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.content_rule_change ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.content_rule_change
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.content_rule_change_event ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.content_rule_change_event
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

COMMENT ON TABLE ztax.content_crosswalk IS
  'A vendor code namespace at one vendor release, mapped onto Zoiko canonical service codes. A translation aid, never fiscal authority. Append-only.';
COMMENT ON TABLE ztax.content_crosswalk_version IS
  'One immutable crosswalk version: its grant (rights basis, reference, end date), effective window and content digest. A change is the next version.';
COMMENT ON TABLE ztax.content_crosswalk_entry IS
  'The mappings of one crosswalk version: external code, vendor meaning, match quality, canonical target, levy hint and predicate. Never edited.';
COMMENT ON TABLE ztax.content_crosswalk_event IS
  'Submission, four-eyes approval, review outcome, suspension, reinstatement and supersession of a crosswalk version, in order. Append-only.';
COMMENT ON TABLE ztax.content_rule_change IS
  'A proposed change to a jurisdiction rule pack, with its source citation, authority class and legal dates. Approval makes it eligible for a content release; nothing is published from here. Append-only.';
COMMENT ON TABLE ztax.content_rule_change_event IS
  'Test evidence, specialist recommendation, independent approval for release, requests for changes, rejection, withdrawal and supersession of a rule change, in order. Append-only.';
