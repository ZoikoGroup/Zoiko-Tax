-- Boundary datasets as versioned, immutable artifacts (ZTAX-JUR-001 §2.5).
--
-- 000003 put a boundary column on ztax.jurisdiction. A boundary there has no
-- version, so a spatial resolution cannot record which boundaries it used
-- (JUR-REQ-0003) and a replay cannot ask for them back (JUR-REQ-0004): a 2027
-- transaction replayed against 2031 boundaries would return a different
-- jurisdiction with nothing to say that anything changed. JUR-001 §13 names
-- this migration as the fix.
--
-- A dataset version is a header row and one boundary row per jurisdiction.
-- Both tables are reference content, like ztax.jurisdiction: no tenant_id,
-- and no customer data. jurisdiction.boundary is left in place and unread; it
-- is dropped once nothing deployed reads it, rather than in the migration
-- that makes it redundant.

CREATE TABLE ztax.boundary_dataset (
  dataset_id   text        NOT NULL,
  version      text        NOT NULL,
  published_at timestamptz NOT NULL,
  -- Attribution is required: authoritative boundary data is licensed, and a
  -- dataset whose source is unknown is one whose rights nobody can check.
  source       text        NOT NULL,
  -- The ADR-0011 digest of the dataset's canonical form, computed by
  -- jurisdiction.NewBoundaryDataset. A resolution records it beside the
  -- version, so a version label reused for different content is detectable.
  digest       text        NOT NULL,
  recorded_at  timestamptz NOT NULL,
  PRIMARY KEY (dataset_id, version),
  CONSTRAINT boundary_dataset_digest_shape CHECK (digest LIKE 'zt1:%'),
  CONSTRAINT boundary_dataset_source_present CHECK (source <> '')
);

CREATE TABLE ztax.jurisdiction_boundary (
  dataset_id      text NOT NULL,
  version         text NOT NULL,
  jurisdiction_id text NOT NULL REFERENCES ztax.jurisdiction (jurisdiction_id),
  level           text NOT NULL,
  boundary        geography(MULTIPOLYGON, 4326) NOT NULL,
  PRIMARY KEY (dataset_id, version, jurisdiction_id),
  FOREIGN KEY (dataset_id, version) REFERENCES ztax.boundary_dataset (dataset_id, version),
  CONSTRAINT jurisdiction_boundary_level_known CHECK (
    level IN ('COUNTRY', 'STATE', 'COUNTY', 'CITY', 'DISTRICT', 'SPECIAL'))
);

CREATE INDEX jurisdiction_boundary_by_version_gix
  ON ztax.jurisdiction_boundary USING GIST (boundary);

-- Immutable to the application, stated rather than implied (JUR-REQ-0005).
-- A dataset version is loaded once by the content pipeline under the DDL
-- role and is never corrected in place: a correction is a new version. The
-- application only reads, so it does not keep the INSERT the schema-wide
-- default of 000001 would give it either.
REVOKE INSERT, UPDATE, DELETE, TRUNCATE ON ztax.boundary_dataset FROM ztax_app;
REVOKE INSERT, UPDATE, DELETE, TRUNCATE ON ztax.jurisdiction_boundary FROM ztax_app;

COMMENT ON TABLE ztax.boundary_dataset IS
  'One version of a boundary dataset (ZTAX-JUR-001 §2.5). Immutable; a correction is a new version.';
COMMENT ON TABLE ztax.jurisdiction_boundary IS
  'One jurisdiction''s boundary in one dataset version. Spatial resolution pins a version and records it (JUR-REQ-0003).';
