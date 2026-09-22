"""ZoikoTax Governed Model Gateway.

The only path between the Go fiscal core and any model provider (ADR-0006
§2.1). Nothing in the estate calls a provider directly; network policy in the
regional cell denies Go workloads egress to provider endpoints, so this is
enforced by the network rather than by anyone remembering.

What is here today is the enforcement point — the part ADR-0006 §2.5 makes the
Gateway's reason for existing — and the decimal boundary of §2.3. What is not
here is the transport: gRPC service definitions are generated from
``contracts/schemas`` (ADR-0006 §2.2, ADR-0010 §2.1), and that pipeline opens in
W2 lane K. ``service.py`` marks the seam.

The ordering is deliberate rather than convenient. The governance logic is
testable with no transport, no provider and no model, and it is the half that
has to be right; the transport is plumbing that can be generated once the
contract exists. Building it the other way round produces a Gateway that can
carry a request before it can refuse one.
"""

from .decimal_wire import DecimalWireError, parse, render
from .governance import (
    MAX_PERMITTED_AUTHORITY,
    GovernanceRefusedError,
    Refusal,
    UseCase,
    UseCaseRegistry,
    authorise,
)
from .provenance import AuthorityOutcome, Provenance, RiskTier

__all__ = [
    "MAX_PERMITTED_AUTHORITY",
    "AuthorityOutcome",
    "DecimalWireError",
    "GovernanceRefusedError",
    "Provenance",
    "Refusal",
    "RiskTier",
    "UseCase",
    "UseCaseRegistry",
    "authorise",
    "parse",
    "render",
]
