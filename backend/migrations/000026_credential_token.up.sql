-- One-time links that set a password: invitations and resets.
--
-- An invited user has no password, and until now had no way to get one; a user
-- who forgot theirs had no way back either. A link emailed to the address on
-- the account is the credential that bridges the gap, so it is stored the way
-- a session is: token_digest is SHA-256 of a 256-bit secret from crypto/rand,
-- and the secret itself is never stored (see 000002 on why SHA-256 suffices).
CREATE TABLE ztax.credential_token (
  tenant_id    uuid        NOT NULL,
  token_digest bytea       NOT NULL,
  user_id      uuid        NOT NULL,
  purpose      text        NOT NULL,
  created_at   timestamptz NOT NULL,
  expires_at   timestamptz NOT NULL,
  -- used_at is set when the link sets a password, and when a newer link for
  -- the same user and purpose supersedes it. Either way it is spent.
  used_at      timestamptz NULL,
  PRIMARY KEY (tenant_id, token_digest),
  FOREIGN KEY (tenant_id, user_id) REFERENCES ztax.app_user (tenant_id, user_id),
  CONSTRAINT credential_token_purpose_known CHECK (purpose IN ('INVITE', 'RESET')),
  CONSTRAINT credential_token_expires_after_created CHECK (expires_at > created_at)
);

-- A link names no tenant, like a session cookie, so the lookup index is
-- necessarily not tenant-prefixed. It is unique across the cell: a digest
-- resolves to exactly one token or to none, and the tenant comes from the row.
CREATE UNIQUE INDEX credential_token_digest_key ON ztax.credential_token (token_digest);

CREATE INDEX credential_token_by_user ON ztax.credential_token (tenant_id, user_id, purpose)
  WHERE used_at IS NULL;

-- Spending a link is the one change: the row is otherwise the record of what
-- was issued and when.
GRANT UPDATE (used_at) ON ztax.credential_token TO ztax_app;

ALTER TABLE ztax.credential_token ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ztax.credential_token
  USING (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid)
  WITH CHECK (current_setting('ztax.scope', true) = 'cell'
         OR tenant_id = NULLIF(current_setting('ztax.tenant_id', true), '')::uuid);

COMMENT ON TABLE ztax.credential_token IS
  'One-time invitation and password-reset links. token_digest is SHA-256 of the emailed secret; the secret is never stored.';
