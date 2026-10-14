"""The Gateway's gRPC server: the far end of the Go/Python boundary.

One method, ``/ztax.gateway.v1.ModelGateway/Invoke``, carrying the JSON
messages of ``contracts/schemas/ai/gateway-call`` and ``gateway-reply``
(ADR-0006 §2.1; README.md, "The transport", records the codec). Every call
goes through the four steps ``service.guarded`` enforces, in this order:

    1. Decode the call and reconstruct the governance context. A message that
       is not a valid call is refused as a malformed context.
    2. ``authorise`` — refuse before the payload is touched.
    3. Run the routed model.
    4. Log the crossing with the redacted context (``guarded`` does this).

Refusals and failures leave as gRPC status codes, with the estate reason code
in the ``ztax-reason`` trailing metadata so the Go client can rebuild the exact
``errs.Error``. The status detail never carries the input or the output.

Transport security is mTLS (ADR-0006 §2.1). ``serve`` refuses to listen
without it unless the process is explicitly a local development Gateway.
"""

from __future__ import annotations

import json
import logging
import os
import sys
from concurrent import futures
from dataclasses import dataclass
from pathlib import Path
from typing import Any, NoReturn

import grpc

from .evidence_sink import EvidenceSink, LoggingEvidenceSink
from .fake_runtime import FakeRuntime
from .governance import GovernanceRefusedError, Refusal, UseCase, UseCaseRegistry
from .invocation_evidence import (
    GuardrailResult,
    InvocationEvidenceBuilder,
    InvocationEvidenceRecord,
    InvocationOutcome,
    UsageMetrics,
)
from .provenance import AuthorityOutcome, Provenance, RiskTier
from .runtime import (
    ModelRuntime,
    Route,
    RoutingTable,
    RuntimeNotConfiguredError,
    RuntimeUnavailableError,
    UnconfiguredRuntime,
    UnroutedUseCaseError,
    check_result,
)
from .service import guarded
from .wire import Call, Reply, WireError, decode_call, encode_reply

log = logging.getLogger("ztax_gateway")

SERVICE = "ztax.gateway.v1.ModelGateway"
METHOD = "Invoke"
FULL_METHOD = f"/{SERVICE}/{METHOD}"
REASON_KEY = "ztax-reason"

# The transport's own reason codes. Their Go twins are registered in
# backend/internal/adapter/gateway (ReasonGatewayNotConfigured,
# ReasonGatewayUnavailable).
REASON_NOT_CONFIGURED = "AI_GATEWAY_NOT_CONFIGURED"
REASON_UNAVAILABLE = "AI_GATEWAY_UNAVAILABLE"


@dataclass(frozen=True)
class GatewayError(Exception):
    """A call the Gateway will not complete, as a status and a reason."""

    code: grpc.StatusCode
    reason: str
    detail: str


_REFUSAL_STATUS = {r: grpc.StatusCode.PERMISSION_DENIED for r in Refusal}
_REFUSAL_STATUS[Refusal.MALFORMED_CONTEXT] = grpc.StatusCode.INVALID_ARGUMENT


class Gateway:
    """The Gateway's request handling, independent of the transport."""

    def __init__(
        self,
        registry: UseCaseRegistry,
        routes: RoutingTable,
        runtime: ModelRuntime,
        region: str,
        ai_train_version: str,
        *,
        evidence_sink: EvidenceSink | None = None,
    ) -> None:
        if not region or not ai_train_version:
            raise ValueError("gateway: a Gateway names its region and its AI train version")
        self._registry = registry
        self._routes = routes
        self._runtime = runtime
        self._region = region
        self._train = ai_train_version
        # Ch.17 §21: default to the structured-log sink; callers may inject
        # InMemoryEvidenceSink in tests for direct record assertions.
        self._evidence_sink: EvidenceSink = evidence_sink or LoggingEvidenceSink()

    def invoke(self, data: bytes) -> bytes:
        try:
            call = decode_call(data)
        except WireError as exc:
            raise GatewayError(
                grpc.StatusCode.INVALID_ARGUMENT, Refusal.MALFORMED_CONTEXT.value, str(exc)
            ) from exc

        g = call.governance
        route: Route | None
        try:
            route = self._routes.route(g.use_case)
        except UnroutedUseCaseError:
            route = None
        unrouted = "unrouted"
        provenance = Provenance(
            use_case=g.use_case,
            model_profile=route.model_profile if route else unrouted,
            provider_profile=route.provider_profile if route else unrouted,
            prompt_profile=route.prompt_profile if route else unrouted,
            region=g.region,
            data_class=g.highest_data_class(),
            risk_tier=g.risk_tier,
            authority_outcome=g.authority_outcome,
            ai_train_version=self._train,
            data_classes=g.data_classes,
        )

        if g.region != self._region:
            # ADR-0006 §2.8: residency is per cell. A call from another cell's
            # region has reached the wrong Gateway, whatever the use case
            # permits.
            self._seal_block(
                provenance,
                call.input,
                refusal_code=Refusal.RESIDENCY_REFUSED.value,
            )
            raise GatewayError(
                grpc.StatusCode.PERMISSION_DENIED,
                Refusal.RESIDENCY_REFUSED.value,
                f"this Gateway serves {self._region!r}; the call is from {g.region!r}",
            )

        def work(_: UseCase) -> Reply:
            if route is None:
                raise UnroutedUseCaseError(f"use case {g.use_case!r} has no route")
            return self._run(route, call)

        # Ch.17 §21: produce exactly one tamper-evident evidence record per
        # invocation, written to the sink *before* the reply bytes are returned.
        try:
            reply = guarded(self._registry, provenance, work)
        except GovernanceRefusedError as refusal:
            # Governance refused: seal a BLOCK record so the refusal is
            # auditable, then re-raise as a GatewayError for the transport.
            caller_err = GatewayError(
                _REFUSAL_STATUS[refusal.refusal], refusal.refusal.value, refusal.detail
            )
            self._seal_block(
                provenance,
                call.input,
                refusal_code=refusal.refusal.value,
                original_error=caller_err,
            )
            raise caller_err from refusal
        except RuntimeNotConfiguredError as exc:
            # Gap 1: the cell has no approved runtime — write a BLOCK record
            # with a FAIL guardrail so the call is auditable, then surface
            # the original UNIMPLEMENTED / AI_GATEWAY_NOT_CONFIGURED error.
            caller_err = GatewayError(
                grpc.StatusCode.UNIMPLEMENTED, REASON_NOT_CONFIGURED, str(exc)
            )
            self._seal_block(
                provenance,
                call.input,
                refusal_code=REASON_NOT_CONFIGURED,
                original_error=caller_err,
            )
            raise caller_err from exc
        except UnroutedUseCaseError as exc:
            # Gap 2: a deployment defect — the registry permits what routing
            # cannot serve.  The provenance already carries "unrouted"
            # placeholders so the record can be built; write it before raising.
            log.error(
                "gateway: registered use case without a route", extra={"use_case": g.use_case}
            )
            caller_err = GatewayError(
                grpc.StatusCode.UNIMPLEMENTED, REASON_NOT_CONFIGURED, str(exc)
            )
            self._seal_block(
                provenance,
                call.input,
                refusal_code=REASON_NOT_CONFIGURED,
                original_error=caller_err,
            )
            raise caller_err from exc
        except RuntimeUnavailableError as exc:
            self._seal_block(
                provenance,
                call.input,
                refusal_code=REASON_UNAVAILABLE,
            )
            raise GatewayError(grpc.StatusCode.UNAVAILABLE, REASON_UNAVAILABLE, str(exc)) from exc

        # Successful invocation: seal a PROPOSE record (the authority-derived
        # default) and write it to the sink before encode_reply is called.
        self._seal_success(provenance, call.input, reply)
        return encode_reply(reply)

    def _run(self, route: Route, call: Call) -> Reply:
        result = self._runtime.run(route, call)
        check_result(call.kind, result)
        return Reply(
            model_profile=route.model_profile,
            provider_profile=route.provider_profile,
            prompt_profile=route.prompt_profile,
            ai_train_version=self._train,
            text=result.text,
            payload=result.payload,
            fields=dict(result.fields or {}),
            proposed_code=result.proposed_code,
            confidence=result.confidence,
        )

    # ------------------------------------------------------------------
    # Evidence helpers (Ch.17 §21)
    # ------------------------------------------------------------------

    def _seal_success(self, provenance: Provenance, input_bytes: bytes, reply: Reply) -> None:
        """Build and write a successful-invocation evidence record.

        The outcome is derived from the authority outcome so that the
        evidence vocabulary (PROPOSE / PRIORITIZE / REVIEW / …) matches
        what the governed context declared.
        """
        outcome = InvocationOutcome.from_authority_outcome(provenance.authority_outcome)
        builder = InvocationEvidenceBuilder(
            provenance=provenance,
            invocation_outcome=outcome,
            registry=self._registry,
        )
        builder.set_input(input_bytes)
        # Serialise the reply to bytes for the output hash; this is the same
        # canonical form the Go caller will receive, so the hash is verifiable.
        reply_bytes = encode_reply(reply)
        builder.set_output(reply_bytes)
        builder.add_guardrail(GuardrailResult("governance.authority", "PASS"))
        builder.set_usage(UsageMetrics())  # provider telemetry not yet available
        record = builder.build()
        self._write_sink(record)

    def _seal_block(
        self,
        provenance: Provenance,
        input_bytes: bytes,
        *,
        refusal_code: str,
        original_error: GatewayError | None = None,
    ) -> None:
        """Build and write a BLOCK evidence record for a governance refusal.

        A BLOCK record has no output hash (rule 5: a blocked invocation
        produced no output).  The builder skips the governance gate for BLOCK
        records because the refusal is exactly what we are recording.

        Parameters
        ----------
        original_error:
            When set, a sink failure must NOT replace the caller's error; the
            original GatewayError is re-raised instead (Gap 4).  Pass None
            only for paths (residency, RuntimeUnavailable) that already own
            the final error at the call site.
        """
        builder = InvocationEvidenceBuilder(
            provenance=provenance,
            invocation_outcome=InvocationOutcome.BLOCK,
            registry=self._registry,
        )
        builder.set_input(input_bytes)
        builder.add_guardrail(GuardrailResult("governance.authority", refusal_code))
        record = builder.build()
        self._write_sink_block(record, original_error=original_error)

    def _write_sink_block(
        self,
        record: InvocationEvidenceRecord,
        *,
        original_error: GatewayError | None,
    ) -> None:
        """Write a BLOCK record to the sink.

        Gap 4: if the sink raises while recording a refusal or a runtime
        failure, the caller's error must not be replaced.  Log the sink
        failure and re-raise *original_error* when one is provided; otherwise
        fall through to the UNAVAILABLE path (residency / runtime-failure own
        their error at the call site and set original_error=None).
        """
        try:
            self._evidence_sink.write(record)
        except Exception:
            log.exception("gateway: evidence sink write failed (BLOCK record)")
            if original_error is not None:
                raise original_error from None
            # residency / runtime-failure paths: the caller raises after us,
            # so simply return; the outer except will raise the right error.

    def _write_sink(self, record: InvocationEvidenceRecord) -> None:
        """Write a PERMIT record to the sink.

        Ch.17 §21 fail-closed: if the sink raises on a *permitted* call, do
        not return the reply.  Log the failure and surface it through the
        existing runtime-failure error path with no new reason code
        (StatusCode.UNAVAILABLE, REASON_UNAVAILABLE).
        """
        try:
            self._evidence_sink.write(record)
        except Exception as exc:
            log.exception("gateway: evidence sink write failed")
            raise GatewayError(
                grpc.StatusCode.UNAVAILABLE, REASON_UNAVAILABLE, str(exc)
            ) from exc


def _abort(context: grpc.ServicerContext, err: GatewayError) -> NoReturn:
    context.set_trailing_metadata(((REASON_KEY, err.reason),))
    context.abort(err.code, err.detail)
    raise AssertionError("unreachable: abort raises")


def handler(gateway: Gateway) -> grpc.GenericRpcHandler:
    """The generic handler for the one method. Requests and responses are the
    raw JSON bytes; the codec is ``wire``."""

    def invoke(request: bytes, context: grpc.ServicerContext) -> bytes:
        try:
            return gateway.invoke(request)
        except GatewayError as err:
            _abort(context, err)
        except Exception:
            log.exception("gateway: unhandled error")
            _abort(
                context,
                GatewayError(grpc.StatusCode.UNAVAILABLE, REASON_UNAVAILABLE, "internal error"),
            )

    return grpc.method_handlers_generic_handler(
        SERVICE, {METHOD: grpc.unary_unary_rpc_method_handler(invoke)}
    )


@dataclass(frozen=True)
class TLS:
    """The server's mTLS material. Client certificates are required."""

    cert_chain: bytes
    private_key: bytes
    client_ca: bytes


def serve(
    gateway: Gateway,
    address: str,
    tls: TLS | None,
    *,
    insecure_local: bool = False,
    max_workers: int = 16,
) -> tuple[grpc.Server, int]:
    """Start a server and return it with the port it bound. Without TLS it
    refuses to listen unless ``insecure_local`` says this is a development
    Gateway."""
    if tls is None and not insecure_local:
        raise ValueError("gateway: refusing to serve without mTLS (ADR-0006 §2.1)")
    server = grpc.server(futures.ThreadPoolExecutor(max_workers=max_workers))
    server.add_generic_rpc_handlers((handler(gateway),))
    if tls is not None:
        creds = grpc.ssl_server_credentials(
            [(tls.private_key, tls.cert_chain)],
            root_certificates=tls.client_ca,
            require_client_auth=True,
        )
        port = server.add_secure_port(address, creds)
    else:
        port = server.add_insecure_port(address)
    if port == 0:
        raise OSError(f"gateway: could not bind {address}")
    server.start()
    return server, port


def load_config(path: Path) -> tuple[UseCaseRegistry, RoutingTable]:
    """Read the registry and routing from a JSON file.

    The file is a reviewed AI-train artifact with the schema::

        {
          "use_cases": [
            {
              "use_case_id":          str,
              "owner":                str,
              "description":          str,
              "max_risk_tier":        str,   # T0-T4
              "max_authority":        str,   # A0-A4
              "permitted_regions":    [str], # optional; empty = no region allowed
              "permitted_data_classes": [str], # optional; absent = deny all
              "suspended":            bool,  # optional; default false
              "route": {
                "model_profile":    str,
                "provider_profile": str,
                "prompt_profile":   str
              }
            }
          ]
        }

    A ``"suspended": true`` entry is registered with its ``suspended`` flag
    set, so the Gateway refuses it with ``AI_USE_CASE_SUSPENDED`` rather than
    ``AI_UNKNOWN_USE_CASE``.
    """
    doc: dict[str, Any] = json.loads(path.read_text(encoding="utf-8"))
    registry = UseCaseRegistry()
    routes: dict[str, Route] = {}
    for entry in doc.get("use_cases", []):
        dc_raw = entry.get("permitted_data_classes")
        uc = UseCase(
            use_case_id=entry["use_case_id"],
            owner=entry["owner"],
            description=entry["description"],
            max_risk_tier=RiskTier(entry["max_risk_tier"]),
            max_authority=AuthorityOutcome(entry["max_authority"]),
            permitted_regions=frozenset(entry.get("permitted_regions", [])),
            permitted_data_classes=frozenset(dc_raw) if dc_raw is not None else None,
            suspended=bool(entry.get("suspended", False)),
        )
        registry.register(uc)
        r = entry["route"]
        routes[uc.use_case_id] = Route(
            r["model_profile"], r["provider_profile"], r["prompt_profile"]
        )
    return registry, RoutingTable(routes)


def _env(name: str, default: str = "") -> str:
    return os.environ.get(name, default).strip()


def _runtime() -> ModelRuntime:
    """Resolve the model runtime from the environment.

    ``ZTAX_GATEWAY_RUNTIME`` controls which runtime is started:

    * ``"unconfigured"`` (default) — ``UnconfiguredRuntime``, which refuses
      every call.  This is the safe default for any unreviewed deployment.
    * ``"fake"`` — ``FakeRuntime``, which returns canned answers without
      calling any real provider.  Allowed **only** when
      ``ZTAX_ENVIRONMENT == "development"``; any other environment exits with
      a non-zero code because a fake runtime must never run in production.

    Any other value is also refused with a non-zero exit, so a typo in the
    env var fails loudly rather than silently falling back to an unsafe state.
    """
    runtime_name = _env("ZTAX_GATEWAY_RUNTIME", "unconfigured")
    if runtime_name == "unconfigured":
        return UnconfiguredRuntime()
    if runtime_name == "fake":
        if _env("ZTAX_ENVIRONMENT") != "development":
            print(
                "gateway: ZTAX_GATEWAY_RUNTIME=fake is only permitted when "
                "ZTAX_ENVIRONMENT=development",
                file=sys.stderr,
            )
            raise SystemExit(2)
        return FakeRuntime()
    print(
        f"gateway: unknown ZTAX_GATEWAY_RUNTIME value {runtime_name!r}; "
        "expected 'unconfigured' or 'fake'",
        file=sys.stderr,
    )
    raise SystemExit(2)


def main() -> int:
    """Run a Gateway from the environment (ADR-0017 §2.1: environment only)."""
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s %(message)s")
    config = _env("ZTAX_GATEWAY_CONFIG")
    region = _env("ZTAX_GATEWAY_REGION")
    train = _env("ZTAX_GATEWAY_AI_TRAIN")
    address = _env("ZTAX_GATEWAY_ADDRESS", "[::]:8443")
    if not (config and region and train):
        print(
            "gateway: ZTAX_GATEWAY_CONFIG, ZTAX_GATEWAY_REGION and ZTAX_GATEWAY_AI_TRAIN "
            "are required",
            file=sys.stderr,
        )
        return 2
    registry, routes = load_config(Path(config))
    gateway = Gateway(registry, routes, _runtime(), region, train)

    cert, key, ca = (
        _env("ZTAX_GATEWAY_TLS_CERT"),
        _env("ZTAX_GATEWAY_TLS_KEY"),
        _env("ZTAX_GATEWAY_TLS_CLIENT_CA"),
    )
    tls: TLS | None = None
    if cert or key or ca:
        if not (cert and key and ca):
            print("gateway: mTLS needs a certificate, a key and a client CA", file=sys.stderr)
            return 2
        tls = TLS(Path(cert).read_bytes(), Path(key).read_bytes(), Path(ca).read_bytes())
    insecure = (
        _env("ZTAX_GATEWAY_INSECURE_LOCAL") == "true" and _env("ZTAX_ENVIRONMENT") == "development"
    )
    server, _ = serve(gateway, address, tls, insecure_local=insecure)
    log.info(
        "gateway: serving", extra={"address": address, "region": region, "mtls": tls is not None}
    )
    server.wait_for_termination()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
