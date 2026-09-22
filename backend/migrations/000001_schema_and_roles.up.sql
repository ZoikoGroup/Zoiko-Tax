-- Schema, extensions and roles for one regional execution cell.
--
-- ADR-0008 §2.9 pins search_path to named schemas and leaves public unused and
-- revoked, so that a role-configuration change cannot silently redirect a query
-- to a table somebody else created.
--
-- ADR-0008 §2.8 splits the roles: migrations run under a DDL role the
-- application never holds. Locally the compose user owns everything, so this
-- migration creates the application role and grants it what it may have; in a
-- cell, ztax-migrate connects as the DDL role and ztax-core as ztax_app, and
-- neither can do the other's job.

CREATE SCHEMA IF NOT EXISTS ztax;

-- PostGIS is not optional. Jurisdiction resolution needs real geospatial
-- support, and it is the reason ADR-0008 §4.4 rules out CockroachDB.
--
-- It is left wherever it was installed, which for the postgis/postgis image is
-- public. Relocating it is not supported cleanly by PostGIS itself, and a
-- migration that tries produces a half-moved extension.
--
-- So public holds extension types and nothing else. That is a narrower reading
-- of ADR-0008 §2.9 than "public is unused", and the grants below are what make
-- it hold rather than being a matter of discipline: ztax_app gets USAGE on
-- public, so it can name the geography type, and never gets CREATE, so it
-- cannot put a table there. An application table created without a schema
-- qualification fails outright rather than landing somewhere nothing looks for
-- it, which is the property §2.9 is actually protecting.
CREATE EXTENSION IF NOT EXISTS postgis;

REVOKE ALL ON SCHEMA public FROM PUBLIC;

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'ztax_app') THEN
    -- NOLOGIN: the role is a grant holder. The process authenticates as itself
    -- and inherits this, or uses workload identity where the platform provides
    -- it (ADR-0008 §2.11).
    CREATE ROLE ztax_app NOLOGIN;
  END IF;
END
$$;

GRANT USAGE ON SCHEMA ztax TO ztax_app;

-- USAGE, deliberately not CREATE: name the extension's types, never add to it.
GRANT USAGE ON SCHEMA public TO ztax_app;

-- Default privileges are set once here rather than repeated per table, so a
-- table added by a later migration cannot be forgotten. The append-only regime
-- of ADR-0003 §2.1 is enforced by what is NOT in this grant: no UPDATE, no
-- DELETE. Tables that legitimately need them — the accumulator snapshot, outbox
-- delivery bookkeeping, session state — grant them explicitly, one at a time,
-- with a comment saying why.
ALTER DEFAULT PRIVILEGES IN SCHEMA ztax
  GRANT SELECT, INSERT ON TABLES TO ztax_app;

ALTER DEFAULT PRIVILEGES IN SCHEMA ztax
  GRANT USAGE, SELECT ON SEQUENCES TO ztax_app;

-- The schema_migrations table golang-migrate maintains lives here too, so that
-- a cell's migration state is inside the cell's residency boundary like
-- everything else (ADR-0009 §2.6).
COMMENT ON SCHEMA ztax IS
  'ZoikoTax regional execution cell. Append-only under ADR-0003; every row carries tenant_id (ADR-0012 §2.7).';
