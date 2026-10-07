-- Batches and jobs (W2 lane K: POST /v1/batches, GET /v1/jobs/{id}).
--
-- A batch is a list of commits submitted at once and executed asynchronously,
-- in order, by a worker in the cell. The job row is bookkeeping about the
-- execution — its status, its lease — and may be updated, as ztax.outbox's
-- delivery columns may. What was asked for (batch_item) and what each item
-- came to (batch_item_result) are append-only: a result is written once, and
-- a worker that dies mid-job resumes at the first item without one.

CREATE TABLE ztax.batch_job (
  tenant_id     uuid        NOT NULL REFERENCES ztax.tenant (tenant_id),
  job_id        uuid        NOT NULL,
  operation     text        NOT NULL,
  status        text        NOT NULL,
  item_count    integer     NOT NULL,
  requested_at  timestamptz NOT NULL,
  requested_by  uuid        NULL,
  started_at    timestamptz NULL,
  finished_at   timestamptz NULL,
  -- A RUNNING job whose lease has lapsed belongs to whichever worker claims
  -- it next: the one that held it is presumed dead.
  lease_until   timestamptz NULL,
  PRIMARY KEY (tenant_id, job_id),
  CONSTRAINT batch_job_operation_known CHECK (operation IN ('COMMIT')),
  CONSTRAINT batch_job_status_known CHECK (status IN ('QUEUED', 'RUNNING', 'COMPLETED')),
  CONSTRAINT batch_job_item_count CHECK (item_count BETWEEN 1 AND 1000),
  CONSTRAINT batch_job_running_is_leased CHECK ((status = 'RUNNING') = (lease_until IS NOT NULL)),
  CONSTRAINT batch_job_completed_has_time CHECK ((status = 'COMPLETED') = (finished_at IS NOT NULL))
);

CREATE INDEX batch_job_claimable ON ztax.batch_job (requested_at) WHERE status <> 'COMPLETED';

CREATE TABLE ztax.batch_item (
  tenant_id     uuid    NOT NULL,
  job_id        uuid    NOT NULL,
  item_index    integer NOT NULL,
  business_key  text    NOT NULL,
  -- The item in canonical form (canon/v1): business key, supersedes, event
  -- time, input and read set, exactly what a commit's request digest covers.
  request       bytea   NOT NULL,
  PRIMARY KEY (tenant_id, job_id, item_index),
  FOREIGN KEY (tenant_id, job_id) REFERENCES ztax.batch_job (tenant_id, job_id),
  CONSTRAINT batch_item_index_range CHECK (item_index >= 0 AND item_index < 1000)
);

CREATE TABLE ztax.batch_item_result (
  tenant_id     uuid        NOT NULL,
  job_id        uuid        NOT NULL,
  item_index    integer     NOT NULL,
  status        text        NOT NULL,
  decision_id   uuid        NULL,
  -- A refused item's registered reason code: what a client branches on, and
  -- what its HTTP status derives from. The detail text is not kept; it can
  -- quote the request.
  reason_code   text        NULL,
  recorded_at   timestamptz NOT NULL,
  PRIMARY KEY (tenant_id, job_id, item_index),
  FOREIGN KEY (tenant_id, job_id, item_index) REFERENCES ztax.batch_item (tenant_id, job_id, item_index),
  FOREIGN KEY (tenant_id, decision_id) REFERENCES ztax.tax_decision (tenant_id, decision_id),
  CONSTRAINT batch_item_result_status_known CHECK (status IN ('SUCCEEDED', 'FAILED')),
  CONSTRAINT batch_item_result_shape CHECK (
    (status = 'SUCCEEDED' AND decision_id IS NOT NULL AND reason_code IS NULL) OR
    (status = 'FAILED' AND decision_id IS NULL AND reason_code IS NOT NULL))
);

REVOKE UPDATE, DELETE, TRUNCATE ON ztax.batch_job FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.batch_item FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.batch_item_result FROM ztax_app;
GRANT UPDATE (status, started_at, finished_at, lease_until) ON ztax.batch_job TO ztax_app;

COMMENT ON TABLE ztax.batch_job IS
  'An asynchronous batch of commits. Status and lease are execution bookkeeping.';
COMMENT ON TABLE ztax.batch_item_result IS
  'What each batch item came to. Append-only; written once per item.';
