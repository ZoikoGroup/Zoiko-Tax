-- Reconciliation runs (W2 lane J; ZTAX-FIN-001 §17–§20).
--
-- A run compares one legal period stage by stage and records every item it
-- compared, matched or not, with its exact variance (ZTAX-FIN-REQ-0081). A
-- resolution is its own row naming who resolved the item, why, how and on
-- what evidence (ZTAX-FIN-REQ-0084). Nothing here is updated: a re-run is a
-- new run, and an item is resolved at most once.

CREATE TABLE ztax.recon_run (
  tenant_id        uuid        NOT NULL REFERENCES ztax.tenant (tenant_id),
  run_id           uuid        NOT NULL,
  legal_entity_id  uuid        NOT NULL,
  legal_period     text        NOT NULL,
  ran_at           timestamptz NOT NULL,
  ran_by           uuid        NULL,
  -- The first stage, in trace order, with an exception (R7).
  first_break      text        NULL,
  -- Stages that could not be compared yet, reported rather than omitted
  -- (ZTAX-FIN-REQ-0077).
  unavailable      text[]      NOT NULL,
  PRIMARY KEY (tenant_id, run_id),
  FOREIGN KEY (tenant_id, legal_entity_id) REFERENCES ztax.legal_entity (tenant_id, legal_entity_id),
  CONSTRAINT recon_run_period_shape CHECK (legal_period ~ '^[0-9]{4}-(0[1-9]|1[0-2])$')
);

CREATE TABLE ztax.recon_item (
  tenant_id   uuid    NOT NULL,
  item_id     uuid    NOT NULL,
  run_id      uuid    NOT NULL,
  ordinal     integer NOT NULL,
  stage       text    NOT NULL,
  match_key   text    NOT NULL,
  currency    text    NULL,
  expected    numeric NULL,
  observed    numeric NULL,
  variance    numeric NULL,
  status      text    NOT NULL,
  root_cause  text    NULL,
  detail      text    NULL,
  PRIMARY KEY (tenant_id, item_id),
  FOREIGN KEY (tenant_id, run_id) REFERENCES ztax.recon_run (tenant_id, run_id),
  CONSTRAINT recon_item_stage_known CHECK (stage IN ('R1_CALCULATED_TO_DOCUMENT', 'R2_DOCUMENT_TO_COLLECTION',
    'R3_DOCUMENT_TO_SUBLEDGER', 'R4_SUBLEDGER_TO_RETURN', 'R5_RETURN_TO_REMITTANCE', 'R6_SUBLEDGER_TO_GL', 'R7_END_TO_END')),
  CONSTRAINT recon_item_status_known CHECK (status IN ('MATCHED', 'TOLERANCE_MATCH', 'UNMATCHED', 'PARTIAL', 'DUPLICATE',
    'MISSING', 'CONFLICTED', 'PENDING', 'EXPLAINED', 'RESOLVED'))
);
CREATE UNIQUE INDEX recon_item_ordinal ON ztax.recon_item (tenant_id, run_id, ordinal);

CREATE TABLE ztax.recon_resolution (
  tenant_id    uuid        NOT NULL,
  item_id      uuid        NOT NULL,
  actor        text        NOT NULL,
  resolver     uuid        NULL,
  reason       text        NOT NULL,
  action       text        NOT NULL,
  root_cause   text        NOT NULL,
  evidence     text[]      NOT NULL,
  resolved_at  timestamptz NOT NULL,
  PRIMARY KEY (tenant_id, item_id),
  FOREIGN KEY (tenant_id, item_id) REFERENCES ztax.recon_item (tenant_id, item_id),
  CONSTRAINT recon_resolution_actor_known CHECK (actor IN ('HUMAN', 'AI_A3')),
  CONSTRAINT recon_resolution_human_named CHECK (actor <> 'HUMAN' OR resolver IS NOT NULL),
  CONSTRAINT recon_resolution_action_known CHECK (action IN ('ADJUST', 'RECLASSIFY', 'AMEND', 'WAIT',
    'WAIVE_WITH_APPROVAL', 'EXTERNAL_CORRECTION')),
  CONSTRAINT recon_resolution_evidence_present CHECK (cardinality(evidence) >= 1),
  CONSTRAINT recon_resolution_reason_length CHECK (length(reason) BETWEEN 1 AND 1000)
);

REVOKE UPDATE, DELETE, TRUNCATE ON ztax.recon_run FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.recon_item FROM ztax_app;
REVOKE UPDATE, DELETE, TRUNCATE ON ztax.recon_resolution FROM ztax_app;

COMMENT ON TABLE ztax.recon_run IS
  'One reconciliation of a legal period. Append-only; a re-run is a new run.';
COMMENT ON TABLE ztax.recon_resolution IS
  'How an exception item was resolved, by whom, on what evidence. Append-only; once per item.';
