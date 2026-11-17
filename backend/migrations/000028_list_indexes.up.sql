-- Latest-first listings of refunds, fiscal documents, reconciliation runs and
-- batch jobs.
--
-- Each of these was readable only by its identifier, so a person had to keep
-- the id from the response that created it. The listing endpoints read the
-- most recent rows of one tenant; without an index on the time they sort by,
-- each would scan the tenant's whole history to return a page.
--
-- Indexes only: no row is changed and no grant is widened.

CREATE INDEX refund_recent
  ON ztax.refund (tenant_id, requested_at DESC, refund_id DESC);

CREATE INDEX fiscal_document_recent
  ON ztax.fiscal_document (tenant_id, recorded_at DESC, document_id DESC);

CREATE INDEX recon_run_recent
  ON ztax.recon_run (tenant_id, ran_at DESC, run_id DESC);

CREATE INDEX batch_job_recent
  ON ztax.batch_job (tenant_id, requested_at DESC, job_id DESC);
