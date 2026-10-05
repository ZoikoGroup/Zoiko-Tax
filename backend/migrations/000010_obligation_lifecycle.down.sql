-- Local development only. See 000004's header: production is forward-only.
ALTER TABLE ztax.obligation DROP CONSTRAINT IF EXISTS obligation_status_known;
ALTER TABLE ztax.obligation ADD CONSTRAINT obligation_status_known CHECK (
  status IN ('OPEN', 'READY', 'FILED', 'ACCEPTED', 'REJECTED', 'UNCERTAIN', 'CLOSED'));
