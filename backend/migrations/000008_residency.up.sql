-- Residency: a tenant's home cell, and the evidence of every cross-cell copy.
--
-- ADR-0009 §2.6 makes the cell the unit of residency, and SEC-001 §11 makes
-- residency metadata authoritative configuration: "moving an entity between
-- cells is a migration, not a UI toggle", and "cross-cell copy is an explicit
-- data-transfer operation with purpose, destination, allowed data classes and
-- evidence". This migration is the storage half of both. The enforcement half
-- is the transport's residency stage (internal/transport/http/residency.go)
-- and privacy.NewCrossCellTransfer.

-- ---------------------------------------------------------------------------
-- tenant.home_cell — the one cell a tenant is homed in
-- ---------------------------------------------------------------------------
-- residency_region (000002) says which geography a tenant's data must stay
-- in. home_cell says which cell, of possibly several in that geography, holds
-- it. Both are needed: two cells in one region are still two residency
-- boundaries, and a tenant row present in the wrong one is the finding the
-- residency stage exists to refuse.
--
-- The value is the provisioning cell's ZTAX_CELL, stamped by
-- AdminService.ProvisionTenant. The database cannot supply it — the cell's
-- identity is configuration (ADR-0017 §2.10), not something it knows — so
-- there is no default. Rows created before this migration therefore hold NULL,
-- and NULL is "no home cell", which every cell refuses to serve. That is the
-- fail-closed reading, and it is deliberate: assigning a cell to an existing
-- tenant is a residency decision, made by an operator in a reviewed one-off
-- migration, followed by VALIDATE CONSTRAINT tenant_home_cell_present.
--
-- The presence constraint is NOT VALID for exactly that reason. PostgreSQL
-- enforces a NOT VALID check on every insert and every update from now on, so
-- no new tenant can be created without a home, while existing rows are left
-- for that reviewed assignment rather than invented here. One consequence is
-- worth stating: an unassigned legacy row cannot have its status changed
-- either, because the update is checked too. That is acceptable — it cannot be
-- served anywhere until it is assigned — and it is preferable to a
-- constraint that silently admits new rows without a home.
ALTER TABLE ztax.tenant ADD COLUMN home_cell text NULL;

ALTER TABLE ztax.tenant
  -- The security.ValidateCellID grammar: a DNS label, because a cell name ends
  -- up in hostnames, bucket names and trust-domain paths.
  ADD CONSTRAINT tenant_home_cell_shape CHECK (home_cell ~ '^[a-z0-9][a-z0-9-]{1,62}$'),
  ADD CONSTRAINT tenant_home_cell_present CHECK (home_cell IS NOT NULL) NOT VALID;

-- No grant. 000002 gave ztax_app UPDATE (status) on ztax.tenant and nothing
-- else, so the application role cannot change a tenant's home cell. Rehoming
-- a tenant is a migration under the DDL role, which is what SEC-001 §11 asks.

COMMENT ON COLUMN ztax.tenant.home_cell IS
  'The one cell this tenant is homed in (ADR-0009 §2.6). Immutable to ztax_app; NULL only on rows predating 000008, which no cell serves.';

-- ---------------------------------------------------------------------------
-- cross_cell_transfer — SEC-REQ-0042
-- ---------------------------------------------------------------------------
-- One row per authorized copy of a tenant's data out of this cell, written in
-- the source cell in the transaction that releases the data, before it moves.
-- It names the TransferProfile version that authorized the copy, the purpose
-- and the data classes it carried, where it went, the ADR-0011 digest of the
-- manifest of what moved, and who requested and who approved it.
--
-- requested_by and approved_by carry no foreign key to app_user. An approver
-- is in general a privacy or legal officer acting under a workforce identity
-- (SEC-001 §5, Z6), not a user of the tenant whose data is moving; the column
-- names the principal, and the identity plane resolves it.
CREATE TABLE ztax.cross_cell_transfer (
  tenant_id        uuid        NOT NULL REFERENCES ztax.tenant (tenant_id),
  transfer_id      uuid        NOT NULL,
  source_cell      text        NOT NULL,
  destination_cell text        NOT NULL,
  -- The authorization, by reference and version: profiles are versioned
  -- records, and the version is what makes "the profile as approved" findable.
  profile_id       text        NOT NULL,
  profile_version  int         NOT NULL,
  mechanism        text        NOT NULL,
  purpose          text        NOT NULL,
  -- The PRIV-001 classes present in what moved, sorted and unique. P7 is not
  -- in the permitted set: secrets and credentials never cross a cell
  -- boundary (SEC-001 §12 key separation).
  data_classes     text[]      NOT NULL,
  content_digest   text        NOT NULL,
  requested_by     uuid        NOT NULL,
  approved_by      uuid        NOT NULL,
  recorded_at      timestamptz NOT NULL,

  PRIMARY KEY (tenant_id, transfer_id),
  CONSTRAINT cross_cell_transfer_cells_differ CHECK (source_cell <> destination_cell),
  CONSTRAINT cross_cell_transfer_source_shape CHECK (source_cell ~ '^[a-z0-9][a-z0-9-]{1,62}$'),
  CONSTRAINT cross_cell_transfer_destination_shape CHECK (destination_cell ~ '^[a-z0-9][a-z0-9-]{1,62}$'),
  CONSTRAINT cross_cell_transfer_profile_version CHECK (profile_version >= 1),
  CONSTRAINT cross_cell_transfer_mechanism_known CHECK (mechanism IN (
    'NOT_RESTRICTED', 'ADEQUACY', 'CONTRACTUAL_SAFEGUARDS', 'BINDING_CORPORATE_RULES',
    'DEROGATION', 'LOCAL_STATUTORY')),
  CONSTRAINT cross_cell_transfer_purpose_known CHECK (purpose IN (
    'PURP-TAX-CALC', 'PURP-SITUS', 'PURP-OBL', 'PURP-FILE', 'PURP-RECON', 'PURP-EVID',
    'PURP-SEC', 'PURP-SUPPORT', 'PURP-SVCOPS', 'PURP-AI-ASSIST', 'PURP-ANALYTICS', 'PURP-LEGAL')),
  CONSTRAINT cross_cell_transfer_classes_present CHECK (cardinality(data_classes) >= 1),
  CONSTRAINT cross_cell_transfer_classes_known CHECK (
    data_classes <@ ARRAY['P0', 'P1', 'P2', 'P3', 'P4', 'P5', 'P6']::text[]),
  CONSTRAINT cross_cell_transfer_digest_shape CHECK (content_digest LIKE 'zt1:%'),
  -- SEC-REQ-0013: the requester does not approve their own transfer.
  CONSTRAINT cross_cell_transfer_four_eyes CHECK (requested_by <> approved_by)
);

CREATE INDEX cross_cell_transfer_by_time
  ON ztax.cross_cell_transfer (tenant_id, recorded_at DESC, transfer_id DESC);

-- Insert-only, stated rather than implied. The schema-wide default grant of
-- 000001 already withholds UPDATE and DELETE; this makes the intent survive a
-- change to that default. A transfer record is evidence that a copy was
-- authorized, and evidence that can be amended after the copy is not.
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.cross_cell_transfer FROM ztax_app;

COMMENT ON TABLE ztax.cross_cell_transfer IS
  'Evidence of every authorized cross-cell copy of tenant data (ADR-0009 §2.6, SEC-REQ-0042). Insert-only; written in the source cell before the data moves.';
