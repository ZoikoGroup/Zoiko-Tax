-- Tenancy, identity and sessions.
--
-- ADR-0020 records the decisions here. The two that shape every table below:
--
--   * tenant_id is on every row and is the first column of every index
--     (ADR-0012 §2.7). The one exception is ztax.tenant itself, whose primary
--     key IS the tenant, and ztax.session_token_lookup, which is keyed by a
--     secret the caller supplies before any tenant is known.
--   * Credential material is never stored in a form that can be replayed. The
--     password column holds an Argon2id verifier; the session table holds a
--     SHA-256 of the cookie secret, not the secret.

-- ---------------------------------------------------------------------------
-- tenant
-- ---------------------------------------------------------------------------
CREATE TABLE ztax.tenant (
  tenant_id     uuid        PRIMARY KEY,
  -- slug is the human-facing handle, unique within the cell. It is not an
  -- identifier in the ADR-0012 sense: nothing references it, and it may be
  -- renamed. It exists so a sign-in form can name a tenant without a UUID.
  slug          text        NOT NULL,
  display_name  text        NOT NULL,
  -- residency_region must match the cell's own ZTAX_REGION. A tenant row in the
  -- wrong cell is a residency violation, and the check is in the application
  -- rather than here because the cell's identity is configuration (ADR-0017
  -- §2.10), not something the database knows.
  residency_region text     NOT NULL,
  status        text        NOT NULL,
  created_at    timestamptz NOT NULL,
  -- Append-only: a suspension is a new row in tenant_status_event, not an
  -- UPDATE here. status is the materialised current value, maintained by the
  -- one statement permitted to touch it (see the grant at the foot of this
  -- file), and the event log is the truth.
  CONSTRAINT tenant_status_known CHECK (status IN ('ACTIVE', 'SUSPENDED', 'CLOSED')),
  CONSTRAINT tenant_slug_shape CHECK (slug ~ '^[a-z0-9][a-z0-9-]{1,62}$')
);

CREATE UNIQUE INDEX tenant_slug_key ON ztax.tenant (slug);

CREATE TABLE ztax.tenant_status_event (
  tenant_id     uuid        NOT NULL REFERENCES ztax.tenant (tenant_id),
  seq           bigint      NOT NULL,
  status        text        NOT NULL,
  reason        text        NOT NULL,
  actor_user_id uuid        NULL,
  recorded_at   timestamptz NOT NULL,
  PRIMARY KEY (tenant_id, seq)
);

-- ---------------------------------------------------------------------------
-- app_user
-- ---------------------------------------------------------------------------
-- Named app_user because "user" is reserved in SQL and a quoted table name is a
-- trap that has to be remembered at every call site.
CREATE TABLE ztax.app_user (
  tenant_id     uuid        NOT NULL REFERENCES ztax.tenant (tenant_id),
  user_id       uuid        NOT NULL,
  -- email is the sign-in handle. It is unique per tenant, not globally: the
  -- same person may hold accounts in two tenants, and those accounts share
  -- nothing.
  email         text        NOT NULL,
  display_name  text        NOT NULL,
  -- password_verifier is an Argon2id PHC string: algorithm, version,
  -- parameters and salt travel with the hash, so the parameters can be raised
  -- without invalidating existing credentials.
  --
  -- NULL means this account has no password — an invited user who has not yet
  -- set one, or a user who authenticates another way. It is not "any password
  -- matches"; the verifier refuses NULL explicitly.
  password_verifier text    NULL,
  status        text        NOT NULL,
  created_at    timestamptz NOT NULL,
  PRIMARY KEY (tenant_id, user_id),
  CONSTRAINT app_user_status_known CHECK (status IN ('ACTIVE', 'INVITED', 'DISABLED'))
);

-- tenant_id first, per ADR-0012 §2.7: every read is already scoped to a tenant,
-- so the index that serves it should be too.
CREATE UNIQUE INDEX app_user_email_key ON ztax.app_user (tenant_id, lower(email));

-- The sign-in path looks a user up by tenant slug and email, so user_id alone
-- also needs to resolve for the session join.
CREATE UNIQUE INDEX app_user_id_key ON ztax.app_user (user_id);

CREATE TABLE ztax.user_role (
  tenant_id   uuid        NOT NULL,
  user_id     uuid        NOT NULL,
  role        text        NOT NULL,
  granted_at  timestamptz NOT NULL,
  granted_by  uuid        NULL,
  PRIMARY KEY (tenant_id, user_id, role),
  FOREIGN KEY (tenant_id, user_id) REFERENCES ztax.app_user (tenant_id, user_id),
  CONSTRAINT user_role_known CHECK (role IN ('ADMIN', 'OPERATOR', 'ANALYST', 'AUDITOR'))
);

-- ---------------------------------------------------------------------------
-- session
-- ---------------------------------------------------------------------------
-- An opaque server-side session, not a token the client can read.
--
-- token_digest is SHA-256 of the cookie secret. The secret itself is never
-- stored, so a database disclosure does not yield a set of usable sessions —
-- which is the property a signed JWT in a cookie cannot offer, because the
-- cookie IS the credential there.
--
-- SHA-256 rather than Argon2id deliberately: the secret is 256 bits from
-- crypto/rand, so it has no guessable structure and there is nothing for a
-- work factor to defend. A password needs Argon2id because humans choose
-- passwords; a random 256-bit token does not.
CREATE TABLE ztax.session (
  tenant_id       uuid        NOT NULL,
  session_id      uuid        NOT NULL,
  user_id         uuid        NOT NULL,
  token_digest    bytea       NOT NULL,
  created_at      timestamptz NOT NULL,
  -- absolute_expires_at is the hard limit; idle_expires_at slides forward on
  -- use. Two limits rather than one because a session that is merely long is
  -- not the same risk as one that is long and unattended.
  idle_expires_at timestamptz NOT NULL,
  absolute_expires_at timestamptz NOT NULL,
  revoked_at      timestamptz NULL,
  revoked_reason  text        NULL,
  -- Recorded for the admin session list, so an administrator revoking a
  -- session can tell which one it is. Not used for authentication: binding a
  -- session to an IP breaks mobile users and binding to a user agent breaks on
  -- every browser update.
  user_agent      text        NOT NULL,
  client_ip       inet        NULL,
  PRIMARY KEY (tenant_id, session_id),
  FOREIGN KEY (tenant_id, user_id) REFERENCES ztax.app_user (tenant_id, user_id)
);

-- The authentication path has a cookie and nothing else — no tenant yet — so
-- this one index is necessarily not tenant-prefixed. It is unique across the
-- cell, which is what makes the lookup safe: a digest resolves to exactly one
-- session or to none, and the tenant comes from the row rather than from the
-- request.
CREATE UNIQUE INDEX session_token_digest_key ON ztax.session (token_digest);

CREATE INDEX session_by_user ON ztax.session (tenant_id, user_id, created_at DESC);

-- ---------------------------------------------------------------------------
-- admin_audit
-- ---------------------------------------------------------------------------
-- Every administrative action, append-only. This is not the fiscal evidence
-- store — it answers "who changed access, and when", which is the question an
-- access review asks.
CREATE TABLE ztax.admin_audit (
  tenant_id    uuid        NOT NULL,
  audit_id     uuid        NOT NULL,
  -- actor_user_id is NULL for actions taken by the system rather than a person
  -- (a session expiring, a sweep). The distinction matters to a reviewer.
  actor_user_id uuid       NULL,
  action       text        NOT NULL,
  subject_type text        NOT NULL,
  subject_id   text        NOT NULL,
  -- detail is canonical JSON (ADR-0011), holding no credential material and no
  -- fiscal amount. It records what changed, not the values of secrets.
  detail       jsonb       NOT NULL,
  recorded_at  timestamptz NOT NULL,
  PRIMARY KEY (tenant_id, audit_id)
);

CREATE INDEX admin_audit_by_time ON ztax.admin_audit (tenant_id, recorded_at DESC);
CREATE INDEX admin_audit_by_subject ON ztax.admin_audit (tenant_id, subject_type, subject_id, recorded_at DESC);

-- ---------------------------------------------------------------------------
-- Grants
-- ---------------------------------------------------------------------------
-- The default privilege from migration 1 already gave SELECT and INSERT on all
-- of the above. What follows is the deliberate, itemised set of exceptions —
-- each one a place where ADR-0003's append-only rule does not apply, with the
-- reason stated.

-- Sessions are operational state, not evidence. They slide on use and are
-- revoked in place; an append-only session log would mean a read of the current
-- session state on every request, which is the hot path of every request there
-- is.
GRANT UPDATE, DELETE ON ztax.session TO ztax_app;

-- tenant.status and app_user.status are materialised current values whose
-- history lives in tenant_status_event and admin_audit respectively.
GRANT UPDATE (status) ON ztax.tenant TO ztax_app;
GRANT UPDATE (status, password_verifier, display_name) ON ztax.app_user TO ztax_app;

-- Role grants are revocable by definition; the audit trail is in admin_audit.
GRANT DELETE ON ztax.user_role TO ztax_app;

COMMENT ON TABLE ztax.session IS
  'Opaque server-side sessions. token_digest is SHA-256 of the cookie secret; the secret is never stored.';
COMMENT ON TABLE ztax.admin_audit IS
  'Append-only administrative audit. No credential material, no fiscal amounts.';
