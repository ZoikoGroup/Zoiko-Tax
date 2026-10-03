-- Local development only. See 000004's header: production is forward-only.
DROP TABLE IF EXISTS ztax.cross_cell_transfer;

ALTER TABLE ztax.tenant
  DROP CONSTRAINT IF EXISTS tenant_home_cell_present,
  DROP CONSTRAINT IF EXISTS tenant_home_cell_shape,
  DROP COLUMN IF EXISTS home_cell;
