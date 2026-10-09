-- Row-level security on every tenant table (ZTAX-SEC-REQ-0027, -0026, -0004).
--
-- Until now tenant isolation rested on every statement carrying a tenant_id
-- predicate from the security context (ADR-0012 §2.7). That rule stays; this
-- is the second wall, in the database, for the statement that forgets it.
--
-- Each row is visible, and writable, only when it belongs to the tenant the
-- session's ztax.tenant_id names — or when the session has declared the
-- cell-wide scope (ztax.scope = 'cell'), which the outbox relay, the webhook
-- dispatcher, the batch claim and the session-token lookup take, by name, in
-- code (internal/adapter/postgres/scope.go). A session that has set neither
-- sees nothing: the failure mode of a missing scope is an empty result, never
-- another tenant's row.
--
-- The application runs as ztax_app (the pool's SET ROLE), which owns no table
-- and has no BYPASSRLS, so the policies bind it. The migrator runs as the
-- owner, which they do not bind; it is granted ztax_app here so the pool can
-- assume it when the same login serves both, as in local development.
--
-- Reference data with no tenant — jurisdictions and boundary datasets — is
-- shared by design and has no policy.

GRANT ztax_app TO CURRENT_USER;


ALTER TABLE ztax.accumulator_snapshot ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.accumulator_snapshot
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.admin_audit ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.admin_audit
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.app_user ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.app_user
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.batch_item ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.batch_item
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.batch_item_result ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.batch_item_result
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.batch_job ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.batch_job
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.classification ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.classification
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.contribution_event ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.contribution_event
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.cross_cell_transfer ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.cross_cell_transfer
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.evidence_period_seal ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.evidence_period_seal
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.fiscal_document ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.fiscal_document
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.fiscal_document_decision ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.fiscal_document_decision
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.fiscal_document_predecessor ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.fiscal_document_predecessor
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.fiscal_document_status ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.fiscal_document_status
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.fiscal_line ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.fiscal_line
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.fiscal_line_tax ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.fiscal_line_tax
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.idempotency_record ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.idempotency_record
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.ledger_entry ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.ledger_entry
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.legal_entity ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.legal_entity
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.obligation ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.obligation
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.obligation_contribution ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.obligation_contribution
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.outbox ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.outbox
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.recon_item ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.recon_item
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.recon_resolution ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.recon_resolution
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.recon_run ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.recon_run
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.refund ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.refund
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.refund_event ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.refund_event
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.session ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.session
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.submission_attempt ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.submission_attempt
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.tax_decision ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.tax_decision
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.tcsl_close_manifest ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.tcsl_close_manifest
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.tcsl_journal ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.tcsl_journal
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.tcsl_journal_line ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.tcsl_journal_line
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.tcsl_period_event ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.tcsl_period_event
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.tcsl_reopen_request ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.tcsl_reopen_request
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.tenant ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.tenant
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.tenant_status_event ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.tenant_status_event
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.threshold_crossing ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.threshold_crossing
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.user_role ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.user_role
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.webhook_attempt ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.webhook_attempt
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.webhook_delivery ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.webhook_delivery
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.webhook_secret ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.webhook_secret
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.webhook_status ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.webhook_status
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

ALTER TABLE ztax.webhook_subscription ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.webhook_subscription
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);
