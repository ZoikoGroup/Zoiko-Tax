-- The W2 obligation lifecycle (ZTAX-OBL-REQ-0084 to -0089).
--
-- internal/domain/obligation adds five stored states: DATA_REQUIRED,
-- PAYMENT_DUE, PAID, AMENDMENT_REQUIRED and SUSPENDED. OVERDUE is derived as
-- of an instant and is never stored, so it is deliberately absent here: a row
-- whose status said OVERDUE would be a row whose status changed with nobody
-- writing it.
ALTER TABLE ztax.obligation DROP CONSTRAINT obligation_status_known;
ALTER TABLE ztax.obligation ADD CONSTRAINT obligation_status_known CHECK (
  status IN ('OPEN', 'DATA_REQUIRED', 'READY', 'FILED', 'ACCEPTED', 'REJECTED', 'UNCERTAIN',
             'PAYMENT_DUE', 'PAID', 'AMENDMENT_REQUIRED', 'SUSPENDED', 'CLOSED'));
