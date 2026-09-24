# GENERATED — do not edit.
#
# TypedDicts for the ZoikoTax v1 API, generated from
# contracts/openapi/export/ztax.v1.3.1.yaml by datamodel-code-generator 0.83.0
# (sdk/python/scripts/generate.py).
#
# A hand edit here fails CI: `python scripts/check.py` regenerates this file
# and fails on any diff. Edit the contract, then regenerate.

from __future__ import annotations

from typing import Literal, TypeAlias, TypedDict

from zoikotax._compat import NotRequired

ReasonCode: TypeAlias = str
"""
A registered code from a closed vocabulary (ADR-0016 §2.4). Codes are
never renumbered, never reused and never redefined; a retired code stops
being emitted and keeps its meaning so historical evidence still
resolves.

This is the field to branch on. The set this deployment can emit is
reported by `GET /v1/capabilities`; the enum is deliberately not closed
here, because the register grows by addition and a client that rejects
an unrecognised code would break on an additive change (ADR-0010 §2.6).

"""


Timestamp: TypeAlias = str
"""
RFC 3339 UTC with exactly six fractional digits and a literal `Z`
(ADR-0011 §2.1 P2). No offsets and no variable precision, because two
encodings of one instant must not produce two digests.

"""


Digest: TypeAlias = str
"""
A canonical digest: SHA-256 over the canonical bytes, lowercase hex,
carrying its profile-and-algorithm prefix (ADR-0011 §2.3). The prefix is
not decoration — a future `zt2:` digest of the same document is a
different digest, and comparing the two without it would be wrong.

"""


TenantId: TypeAlias = str
"""
A tenant identifier (ADR-0012). Prefixed, sortable, never recycled.
"""


UserId: TypeAlias = str
"""
A user identifier (ADR-0012).
"""


SessionId: TypeAlias = str
"""
A session identifier (ADR-0012). It is **not** the session secret: the
secret is in the cookie, is never returned by this API, and is not
derivable from this.

"""


AuditId: TypeAlias = str
"""
An audit record identifier (ADR-0012).
"""


Role: TypeAlias = Literal['ADMIN', 'OPERATOR', 'ANALYST', 'AUDITOR']
"""
A role within a tenant. Closed: adding one is a reviewed change with an
authorization matrix entry, not a string a caller may invent.

"""


TenantStatus: TypeAlias = Literal['ACTIVE', 'SUSPENDED', 'CLOSED']


UserStatus: TypeAlias = Literal['ACTIVE', 'INVITED', 'DISABLED']
"""
`INVITED` is a user who exists and has no password, and therefore cannot
sign in. It is a distinct state rather than a disabled user, because the
two lead to different administrative actions.

"""


class Tenant(TypedDict):
    id: TenantId
    slug: str
    """
    The stable, human-usable name a client signs in with.
    """
    displayName: str
    residencyRegion: str
    """
    The region whose cell holds this tenant's data. It never leaves it,
    which is why this is reported rather than inferred.

    """
    status: TenantStatus


class User(TypedDict):
    """
    A user as this API represents one. Note what is absent and always will
    be: there is no password, no verifier, no salt and no parameter set. A
    domain type serialized directly is a domain type whose every future
    field becomes public API by accident.

    """

    id: UserId
    email: str
    displayName: str
    status: UserStatus
    roles: list[Role]
    """
    The roles this user holds, which may be empty.
    """
    createdAt: Timestamp


class Session(TypedDict):
    """
    The current session. It carries no token: the session secret is in an
    `HttpOnly` cookie and is never in a response body.

    """

    tenant: Tenant
    user: User
    expiresAt: Timestamp
    """
    The session's absolute expiry. Absolute, not idle: activity does not
    extend it, so a session ends at a time that was fixed when it began.

    """


class SessionSummary(TypedDict):
    id: SessionId
    userId: UserId
    createdAt: Timestamp
    expiresAt: Timestamp
    userAgent: NotRequired[str]
    """
    The user agent recorded when the session opened, where one was sent.
    """
    clientIp: NotRequired[str]
    """
    The client address recorded when the session opened.
    """
    revoked: bool
    """
    Whether the session has been revoked. A revoked session is kept
    rather than deleted; a session that vanished would leave nothing for
    an audit to look at.

    """
    current: bool
    """
    Whether this is the calling session.
    """


class AuditRecord(TypedDict):
    id: AuditId
    actorUserId: NotRequired[UserId]
    """
    Absent where the action had no human actor — a bootstrap at startup,
    or a scheduled sweep. Absent rather than a placeholder identifier,
    because "the system did this" and "some user did this" are different
    facts.

    """
    action: str
    """
    A dotted action name from a closed vocabulary.
    """
    subjectType: str
    subjectId: str
    detail: str
    """
    A canonical JSON document, as a string. It is the exact bytes that
    were recorded and digested, so it is passed through rather than
    re-encoded (ADR-0011 §2.7).

    """
    recordedAt: Timestamp


class Trains(TypedDict):
    """
    The seven release-train versions. Together they name the exact
    combination of code, content, models, adapters, infrastructure, schemas
    and migrations that produced a response — which is what makes a decision
    replayable years later.

    """

    app: str
    content: str
    ai: str
    adapter: str
    infra: str
    schema: str
    migration: str


class ContentCapability(TypedDict):
    """
    The rule bundle this cell is running. Absent where no bundle is loaded.

    The digest is here because it is what a replay names: quoting the bundle
    identifier alone would not distinguish two builds of one pack, and the
    difference between them is a difference in law.

    """

    bundleId: str
    digest: Digest
    irVersion: int
    """
    The rule-IR instruction-set version the bundle was compiled for. A
    runtime that cannot execute a bundle refuses to load it and stays on
    the previous one rather than degrading (ADR-0005 §2.8).

    """
    nodeCount: int
    """
    The size of the rule graph, for telemetry.
    """


class SignInRequest(TypedDict):
    tenant: str
    """
    The tenant slug.
    """
    email: str
    password: str


class ChangePasswordRequest(TypedDict):
    currentPassword: str
    newPassword: str


class CreateUserRequest(TypedDict):
    email: str
    displayName: str
    roles: NotRequired[list[Role]]
    """
    Roles to grant on creation. A user may be created with none.
    """
    password: NotRequired[str]
    """
    Optional. Omitting it creates an `INVITED` user who cannot sign in
    until a password is set.

    """


class SetUserStatusRequest(TypedDict):
    status: UserStatus


class RoleRequest(TypedDict):
    role: Role


class V1AdminUsersGetResponse(TypedDict):
    users: list[User]


class V1AdminSessionsGetResponse(TypedDict):
    sessions: list[SessionSummary]


class V1AdminAuditGetResponse(TypedDict):
    records: list[AuditRecord]


class Problem(TypedDict):
    """
    RFC 9457 Problem Details, with the `ztx_` extensions from ADR-0016 §2.5.
    The extensions carry identifiers rather than data: an error that needs
    data to be understood names a decision or a request, and the data is
    fetched through an audited path.

    """

    type: str
    """
    A stable, versioned, documented URI for this reason code.
    """
    title: str
    """
    A short summary. **Do not match on this**; it may be reworded.
    """
    status: int
    detail: NotRequired[str]
    """
    What happened and what to do about it. Also not for matching.
    """
    instance: NotRequired[str]
    """
    The request path this occurred on.
    """
    ztx_reason_code: ReasonCode
    ztx_request_id: NotRequired[str]
    """
    This request's identifier, also returned in the `X-Request-Id`
    header. Quote it in a support request; it is what correlates the
    response with the server-side log.

    """
    ztx_trace_id: NotRequired[str]
    """
    The OpenTelemetry trace identifier, where tracing is enabled.
    """
    ztx_decision_id: NotRequired[str]
    """
    The decision this error concerns, where there is one.
    """
    ztx_field: NotRequired[str]
    """
    The contract field at fault, for a validation failure. A field name,
    never a value — a value in an error message is a value in a log.

    """
    ztx_retryable: bool
    """
    Whether retrying this request unchanged could succeed. Only a
    transient failure is retryable; everything else fails identically,
    and a client that retries a validation failure is generating load
    rather than making progress.

    """


class Capabilities(TypedDict):
    cell: str
    """
    The cell serving this request.
    """
    region: str
    environment: str
    trains: Trains
    canonProfile: str
    """
    The canonicalization profile this deployment digests under. It is
    never redefined: a change to any profile rule is a new profile, and
    the old one stays implemented for as long as any record references
    it (ADR-0011 §2.6).

    """
    authoritative: bool
    """
    **Whether this deployment may produce authoritative fiscal output.**
    False until A4. A deployment reporting `false` produces advisory
    figures that must not be filed, and says so here rather than leaving
    a client to assume.

    """
    reasonCodes: list[ReasonCode]
    """
    Every reason code this deployment can emit.
    """
    content: NotRequired[ContentCapability]
