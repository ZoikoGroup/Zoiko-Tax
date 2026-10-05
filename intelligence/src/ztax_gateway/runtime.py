"""What runs a permitted call: the routing table and the model runtime seam.

The Gateway decides *whether* a call may proceed (``governance.authorise``)
before it decides *how*. This module is the how, and it is deliberately thin:

* ``RoutingTable`` maps a registered use case to the model, provider and
  prompt profile that serve it. The routing is the Gateway's, never the
  caller's — the Go client sends governance, and the reply tells it which
  profiles produced the result (ADR-0006 §2.7).
* ``ModelRuntime`` is the seam a provider adapter implements. The production
  default is ``UnconfiguredRuntime``, which refuses every call with
  ``AI_GATEWAY_NOT_CONFIGURED``: a cell with no approved provider says so,
  rather than answering from a stand-in. Wiring a real provider is a reviewed
  change on the AI train with credentials from the Gateway's own vault
  namespace (ADR-0017 §2.5), and nothing in the transport changes when it
  lands.
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import Protocol

from .wire import Call, Kind


class RuntimeNotConfiguredError(Exception):
    """No model runtime is configured for this cell."""


class RuntimeUnavailableError(Exception):
    """The configured runtime failed transiently. Safe to retry: an advisory
    call has no side effect."""


class UnroutedUseCaseError(Exception):
    """A permitted use case with no route. A registry/routing mismatch, which
    is a deployment defect rather than a caller error."""


@dataclass(frozen=True)
class Route:
    """The profiles that serve one use case."""

    model_profile: str
    provider_profile: str
    prompt_profile: str

    def validate(self) -> None:
        if not (self.model_profile and self.provider_profile and self.prompt_profile):
            raise ValueError("route: a route names a model, a provider and a prompt profile")


class RoutingTable:
    """Use case to route. Built once at start-up and read-only after."""

    def __init__(self, routes: dict[str, Route]) -> None:
        for use_case, route in routes.items():
            if not use_case:
                raise ValueError("routing: empty use case")
            route.validate()
        self._routes = dict(routes)

    def route(self, use_case: str) -> Route:
        r = self._routes.get(use_case)
        if r is None:
            raise UnroutedUseCaseError(f"use case {use_case!r} is registered and has no route")
        return r


@dataclass(frozen=True)
class Result:
    """The advisory content of one completed call."""

    text: str = ""
    payload: str = ""
    fields: dict[str, str] | None = None
    proposed_code: str = ""
    confidence: str = ""


class ModelRuntime(Protocol):
    """Runs one permitted call against the routed model."""

    def run(self, route: Route, call: Call) -> Result: ...


class UnconfiguredRuntime:
    """The runtime for a cell with no approved provider. Refuses everything."""

    def run(self, route: Route, call: Call) -> Result:
        raise RuntimeNotConfiguredError(
            f"no model runtime is configured for {route.provider_profile!r} in this cell"
        )


def check_result(kind: Kind, result: Result) -> None:
    """Refuse a result that does not carry its kind's content."""
    match kind:
        case Kind.SUGGESTION:
            if not result.text:
                raise RuntimeUnavailableError("runtime: a suggestion with no text")
        case Kind.EXTRACTION:
            if not result.fields:
                raise RuntimeUnavailableError("runtime: an extraction with no fields")
        case Kind.CLASSIFICATION_PROPOSAL:
            if not result.proposed_code:
                raise RuntimeUnavailableError("runtime: a proposal with no code")
