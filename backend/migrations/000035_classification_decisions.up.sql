
-- Reviewer decisions on AI classification proposals (Intelligence screen).
--
-- One row per proposal: what a person decided about an AI-suggested ontology
-- code. The AI proposal itself is advisory and is not stored, so the row carries
-- what it needs of it: the proposal id, the subject reference and the proposed
-- code, and the evidence id of the call when it had one. The row is immutable:
-- one decision per proposal per tenant, never updated, never deleted.
--
-- Nothing here references or changes a fiscal table. The reason and the note
-- are free text and never leave this table through a log or an error message.
-- How long a row is kept is set by the evidence retention decision, which is not
-- yet made; no period is written here.
--
-- reviewer_id and tenant_id come from the session of the person who decided,
-- never from the request.

CREATE TABLE ztax.classification_decision (
  tenant_id     uuid        NOT NULL REFERENCES ztax.tenant (tenant_id),
  proposal_id   uuid        NOT NULL,
  subject_ref   text        NOT NULL,
  proposed_code text        NOT NULL,
  action        text        NOT NULL,
  reason        text        NOT NULL,
  note          text        NULL,
  modified_code text        NULL,
  evidence_id   text        NULL,
  reviewer_id   uuid        NOT NULL,
  decided_at    timestamptz NOT NULL,
  PRIMARY KEY (tenant_id, proposal_id),
  CONSTRAINT classification_decision_action_known CHECK (action IN ('ACCEPT', 'REJECT', 'MODIFY', 'ESCALATE')),
  CONSTRAINT classification_decision_modified_code CHECK ((action = 'MODIFY') = (modified_code IS NOT NULL)),
  CONSTRAINT classification_decision_evidence_shape CHECK (evidence_id IS NULL OR evidence_id ~ '^[A-Za-z0-9-]{1,64}$'),
  CONSTRAINT classification_decision_text_length CHECK (length(subject_ref) BETWEEN 1 AND 512
    AND length(proposed_code) BETWEEN 1 AND 256
    AND length(reason) BETWEEN 1 AND 500
    AND (note IS NULL OR length(note) BETWEEN 1 AND 2000)
    AND (modified_code IS NULL OR length(modified_code) BETWEEN 1 AND 256))
);
CREATE INDEX classification_decision_decided ON ztax.classification_decision (tenant_id, decided_at DESC, proposal_id);

REVOKE UPDATE, DELETE, TRUNCATE ON ztax.classification_decision FROM ztax_app;

ALTER TABLE ztax.classification_decision ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.classification_decision
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

COMMENT ON TABLE ztax.classification_decision IS
  'A reviewer''s decision on one AI classification proposal. Immutable; one per proposal per tenant.';
