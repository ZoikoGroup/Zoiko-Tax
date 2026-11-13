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

import contextlib
import hashlib
import json
import logging
import math
import os
import sys
import threading
import time
from concurrent import futures
from dataclasses import dataclass, replace
from pathlib import Path
from typing import Any, Final, NoReturn

import grpc

from .evidence_file_sink import default_evidence_sink
from .evidence_sink import EvidenceSink
from .fake_runtime import FakeRuntime
from .governance import (
    UNREGISTERED_USE_CASE,
    GovernanceRefusedError,
    Refusal,
    UseCase,
    UseCaseRegistry,
)
from .invocation_check_event import (
    AnswerCheckedEvent,
    CheckEventSink,
    CheckReason,
    LoggingCheckEventSink,
    Verdict,
)
from .invocation_evidence import (
    GuardrailResult,
    InvocationEvidenceBuilder,
    InvocationEvidenceRecord,
    InvocationOutcome,
    TokenPricing,
    UsageMetrics,
)
from .production_gates import PromptProfileRegistry
from .provenance import AuthorityOutcome, Provenance, RiskTier
from .runtime import (
    ModelRuntime,
    Result,
    Route,
    RoutingTable,
    RuntimeNotConfiguredError,
    RuntimeUnavailableError,
    UnconfiguredRuntime,
    UnroutedUseCaseError,
    check_result,
    token_count,
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
# Fixed caller-facing details. Exception text never reaches the caller on any path
# that reports a malformed call, a runtime that is not configured, an unrouted use
# case, an unavailable runtime, a failed evidence write or a residency refusal: a
# runtime, a sink or a caller's own bytes can put provider, store or caller text in
# an exception, so each of these paths returns one of the constants below instead.
# A governance refusal returns the detail the governance check wrote; some of those
# details name the use case the caller sent (known gap, tracked separately).
DETAIL_CALL_MALFORMED = "call is malformed"
DETAIL_NOT_CONFIGURED = "model runtime is not configured"
DETAIL_RESIDENCY_REFUSED = "call is from a region this Gateway does not serve"
DETAIL_RUNTIME_UNAVAILABLE = "model runtime unavailable"
DETAIL_EVIDENCE_WRITE_FAILED = "evidence write failed"
# Detail of the BLOCK record written when a runtime raises an error the Gateway has no
# name for. Fixed text: the record never carries the exception's text or class name.
RECORD_DETAIL_UNEXPECTED_ERROR = "runtime raised an unexpected error"

# Most runtime calls that may be in flight at once when a runtime timeout is set.
# A call that times out leaves its worker running until the runtime returns, so
# the count includes abandoned workers: with every slot taken a new call fails
# closed at once instead of queueing behind a hung runtime.
RUNTIME_MAX_IN_FLIGHT: Final = 8
_RUNTIME_TIMED_OUT = "runtime: call timed out"
_RUNTIME_BUSY = "runtime: no free slot for a call"
_RUNTIME_NO_WORKER = "runtime: a worker could not be started"

# PRIV-001 valid privacy class labels (P0 through P7).
_VALID_DATA_CLASSES: Final[frozenset[str]] = frozenset(f"P{i}" for i in range(8))


@dataclass(frozen=True)
class GatewayError(Exception):
    """A call the Gateway will not complete, as a status and a reason."""

    code: grpc.StatusCode
    reason: str
    detail: str


_REFUSAL_STATUS = {r: grpc.StatusCode.PERMISSION_DENIED for r in Refusal}
_REFUSAL_STATUS[Refusal.MALFORMED_CONTEXT] = grpc.StatusCode.INVALID_ARGUMENT

_NS_PER_MS: Final = 1_000_000


def _monotonic_ns() -> int:
    """The clock the usage latency is measured with.

    A module-level function so a test can replace it and never has to sleep.
    """
    return time.monotonic_ns()


class _CallTiming:
    """What one runtime call left behind for the evidence record.

    ``ms`` is the whole milliseconds the call took, set when it ends however it ends.
    ``input_tokens`` and ``output_tokens`` are the provider's counts, set only after
    a result came back and only when each is a valid count; None otherwise. Counts
    only: no text from the result is ever kept here.
    """

    __slots__ = ("input_tokens", "ms", "output_tokens")

    def __init__(self) -> None:
        self.ms = 0
        self.input_tokens: int | None = None
        self.output_tokens: int | None = None


class _UnexpectedRuntimeError(Exception):
    """Carries an error the runtime raised that the Gateway has no name for.

    Private to this module: ``_run`` raises it (outside its ``except`` block) so that
    ``invoke_with_id`` can write one BLOCK record, then re-raises ``original`` exactly
    as before. It has no message of its own.
    """

    def __init__(self, original: Exception) -> None:
        super().__init__()
        self.original = original


# Errors the runtime path may raise on purpose; each has its own handling in
# ``invoke_with_id``. Anything else that is an ``Exception`` is "unexpected".
_EXPECTED_RUNTIME_FAILURES: Final = (
    GovernanceRefusedError,
    GatewayError,
    RuntimeNotConfiguredError,
    RuntimeUnavailableError,
    UnroutedUseCaseError,
)


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
        kill_state_path: Path | None = None,
        prompt_profile_registry: PromptProfileRegistry | None = None,
        check_event_sink: CheckEventSink | None = None,
        runtime_timeout_seconds: float | None = None,
        token_pricing: TokenPricing | None = None,
    ) -> None:
        if not region or not ai_train_version:
            raise ValueError("gateway: a Gateway names its region and its AI train version")
        if runtime_timeout_seconds is not None and not _valid_timeout(runtime_timeout_seconds):
            raise ValueError("gateway: runtime_timeout_seconds must be None or a positive number")
        self._registry = registry
        self._routes = routes
        self._runtime = runtime
        self._region = region
        self._train = ai_train_version
        # Ch.17 §21: default to the structured-log sink; callers may inject
        # InMemoryEvidenceSink in tests for direct record assertions. With
        # ZTAX_EVIDENCE_FILE_PATH set, the default also appends each record to that file
        # (evidence_file_sink.py); with it unset the default is the logging sink alone.
        self._evidence_sink: EvidenceSink = evidence_sink or default_evidence_sink()
        # WP5: optional registry for resolving prompt labels to PromptProfile hashes.
        self._prompt_registry: PromptProfileRegistry | None = prompt_profile_registry
        # WP5: sink for AnswerCheckedEvents; defaults to the logging sink.
        self._check_event_sink: CheckEventSink = check_event_sink or LoggingCheckEventSink()
        # Live kill switch (AI-09): the registry file is re-read on every call and
        # parsed only when its bytes change. None disables it (tests, embedding).
        self._kill_state_path = kill_state_path
        # None keeps the runtime call synchronous on the caller's thread. When set,
        # the call runs in a daemon worker thread bounded by ``_runtime_slots``.
        self._runtime_timeout = runtime_timeout_seconds
        # Explicit per-1,000-token prices, or None: with None the cost stays 0.0.
        self._token_pricing = token_pricing
        self._runtime_slots = threading.BoundedSemaphore(RUNTIME_MAX_IN_FLIGHT)
        self._kill_lock = threading.Lock()
        self._kill_digest: str | None = None
        self._kill_by_file: set[str] = set()
        self._global_kill_by_file = False

    def record_answer_check(
        self,
        invocation_id: str,
        verdict: Verdict,
        reason_code: CheckReason,
        validator_version: str,
    ) -> None:
        """Append one :class:`AnswerCheckedEvent` to the check-event sink (WP5).

        Builds the event from the supplied fields and writes it to
        ``self._check_event_sink``.  Never edits the original evidence record.

        Parameters
        ----------
        invocation_id:
            The ``ai_invocation_id`` from the original evidence record.
        verdict:
            The check conclusion (:class:`~ztax_gateway.invocation_check_event.Verdict`).
        reason_code:
            Why the verdict was reached (:class:`~ztax_gateway.invocation_check_event.CheckReason`).
        validator_version:
            The version string of the validator that ran (e.g. ``"explanation_guard@1"``).
        """
        from datetime import UTC, datetime

        event = AnswerCheckedEvent(
            evidence_id=invocation_id,
            verdict=verdict,
            reason_code=reason_code,
            validator_version=validator_version,
            created_at=datetime.now(UTC),
        )
        self._check_event_sink.write_check_event(event)

    def invoke(self, data: bytes) -> bytes:
        """Run one call and return the encoded reply.

        Delegates to :meth:`invoke_with_id`, so the two entry points cannot
        diverge: every refusal, record and error is produced in one place.
        """
        return self.invoke_with_id(data)[0]

    @staticmethod
    def _decode(data: bytes) -> Call:
        """Decode a call, or raise a GatewayError with a fixed detail.

        The WireError is swallowed inside the ``with`` and the GatewayError is
        raised after it, so the error handed to the transport has no
        ``__cause__`` and no ``__context__``: nothing in it can lead back to the
        caller's bytes (the wire messages echo JSON errors and caller values).
        """
        call: Call | None = None
        with contextlib.suppress(WireError):
            call = decode_call(data)
        if call is None:
            raise GatewayError(
                grpc.StatusCode.INVALID_ARGUMENT,
                Refusal.MALFORMED_CONTEXT.value,
                DETAIL_CALL_MALFORMED,
            )
        return call

    def invoke_with_id(self, data: bytes) -> tuple[bytes, str | None]:
        """Run one call and also return the ``ai_invocation_id`` for check events.

        Returns ``(reply_bytes, invocation_id)``.  ``invocation_id`` is ``None``
        when the call is refused or killed before reaching the runtime.  Each
        call's id is local to that call; this method is safe to call concurrently.
        """
        call = self._decode(data)

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

        # What a refusal record may hold. A use-case id the registry does not know is
        # whatever the caller sent, and so is the region of a call from another cell:
        # neither is copied into the evidence record (the record names this cell's own
        # region instead, the place that took the decision).
        refusal_provenance = provenance
        if self._registry.get(g.use_case) is None:
            refusal_provenance = replace(refusal_provenance, use_case=UNREGISTERED_USE_CASE)

        if g.region != self._region:
            # ADR-0006 §2.8: residency is per cell. A call from another cell's
            # region has reached the wrong Gateway, whatever the use case
            # permits. The detail is fixed text: it names neither region.
            self._seal_block(
                replace(refusal_provenance, region=self._region),
                call.input,
                refusal_code=Refusal.RESIDENCY_REFUSED.value,
            )
            raise GatewayError(
                grpc.StatusCode.PERMISSION_DENIED,
                Refusal.RESIDENCY_REFUSED.value,
                DETAIL_RESIDENCY_REFUSED,
            )

        timing = _CallTiming()

        def work(_: UseCase) -> Reply:
            if route is None:
                raise UnroutedUseCaseError("use case has no route")
            return self._run(route, call, timing)

        # Ch.17 §21: produce exactly one tamper-evident evidence record per
        # invocation, written to the sink *before* the reply bytes are returned.
        self._refresh_kill_state()
        # The two "not configured" failures are recorded in flags and handled
        # after the try statement, so the error raised for the caller is not
        # raised inside an ``except`` block and carries no ``__context__``.
        not_configured = False
        no_route = False
        unexpected: Exception | None = None
        try:
            reply = guarded(self._registry, provenance, work)
        except GovernanceRefusedError as refusal:
            # Governance refused: seal a BLOCK record so the refusal is
            # auditable, then re-raise as a GatewayError for the transport.
            caller_err = GatewayError(
                _REFUSAL_STATUS[refusal.refusal], refusal.refusal.value, refusal.detail
            )
            self._seal_block(
                refusal_provenance,
                call.input,
                refusal_code=refusal.refusal.value,
                original_error=caller_err,
            )
            raise caller_err from refusal
        except RuntimeNotConfiguredError:
            # Gap 1: the cell has no approved runtime.
            not_configured = True
        except UnroutedUseCaseError:
            # Gap 2: a deployment defect - the registry permits what routing
            # cannot serve.
            no_route = True
        except RuntimeUnavailableError as exc:
            self._seal_block(
                provenance,
                call.input,
                refusal_code=REASON_UNAVAILABLE,
                latency_ms=timing.ms,
            )
            raise GatewayError(
                grpc.StatusCode.UNAVAILABLE, REASON_UNAVAILABLE, DETAIL_RUNTIME_UNAVAILABLE
            ) from exc
        except _UnexpectedRuntimeError as failure:
            # ADR-0006 §2.7: the crossing is evidenced even when the runtime fails in a
            # way nobody named. Handled after the try statement.
            unexpected = failure.original
        if not_configured or no_route:
            self._refuse_not_configured(provenance, call, no_route=no_route)
        if unexpected is not None:
            # One BLOCK record, the same shape as the runtime-unavailable one (verdict
            # AI_GATEWAY_UNAVAILABLE) with a fixed detail and no exception text. If the
            # sink fails here the failure is logged and the error below is unchanged.
            self._seal_block(
                provenance,
                call.input,
                refusal_code=REASON_UNAVAILABLE,
                detail=RECORD_DETAIL_UNEXPECTED_ERROR,
                latency_ms=timing.ms,
            )
            # The caller sees exactly what it saw before this record existed: the
            # original error (the transport turns it into UNAVAILABLE / "internal error").
            raise unexpected

        # Successful invocation: seal a PROPOSE record (the authority-derived
        # default) and write it to the sink before encode_reply is called.
        invocation_id = self._seal_success(
            provenance,
            call.input,
            reply,
            latency_ms=timing.ms,
            input_tokens=timing.input_tokens,
            output_tokens=timing.output_tokens,
        )
        return encode_reply(reply), invocation_id

    def _refuse_not_configured(
        self, provenance: Provenance, call: Call, *, no_route: bool
    ) -> NoReturn:
        """Seal a BLOCK record, then raise UNIMPLEMENTED / AI_GATEWAY_NOT_CONFIGURED.

        Called outside any ``except`` block. The detail is a fixed constant:
        the runtime's own text (which a custom runtime chooses) is never used.
        For an unrouted use case the missing route is logged for the operator
        (use case id only), since the caller cannot be told.
        """
        if no_route:
            log.error(
                "gateway: registered use case without a route",
                extra={"use_case": call.governance.use_case},
            )
        caller_err = GatewayError(
            grpc.StatusCode.UNIMPLEMENTED, REASON_NOT_CONFIGURED, DETAIL_NOT_CONFIGURED
        )
        self._seal_block(
            provenance,
            call.input,
            refusal_code=REASON_NOT_CONFIGURED,
            original_error=caller_err,
        )
        raise caller_err

    def _refresh_kill_state(self) -> None:
        """Bring the registry's kill state in line with the registry file.

        Reads ONLY ``killed`` and ``global_kill``; ``suspended``, authority, risk
        tier and data classes stay as ``load_config`` set them. Fails closed: a
        file that cannot be read or parsed engages the global kill ("kill state
        unreadable") until a later read parses cleanly. Runs before ``guarded``,
        so a kill takes effect on the next call and never reaches the runtime.
        """
        if self._kill_state_path is None:
            return
        with self._kill_lock:
            try:
                raw = self._kill_state_path.read_bytes()
            except OSError:
                self._kill_digest = None  # re-parse as soon as it is readable again
                self._apply_kill_state(None)
                return
            digest = hashlib.sha256(raw).hexdigest()
            if digest == self._kill_digest:
                return
            self._kill_digest = digest
            self._apply_kill_state(_parse_kill_state(raw))

    def _apply_kill_state(self, state: tuple[bool, frozenset[str]] | None) -> None:
        """Apply parsed kill state (None = unreadable). Kills first, releases last."""
        if state is None and not self._global_kill_by_file:
            log.error("gateway: kill state unreadable; global kill engaged")
        want_global = state is None or state[0]
        want_killed = state[1] if state is not None else frozenset()
        if want_global and not self._registry.global_kill_engaged:
            self._registry.engage_global_kill()
            self._global_kill_by_file = True
        for use_case_id in want_killed - self._kill_by_file:
            self._registry.kill(use_case_id)
        for use_case_id in self._kill_by_file - want_killed:
            self._registry.revive(use_case_id)
        self._kill_by_file = set(want_killed)
        if not want_global and self._global_kill_by_file:
            self._registry.release_global_kill()
            self._global_kill_by_file = False

    def _call_runtime(self, route: Route, call: Call) -> Result:
        """Run the runtime, within ``runtime_timeout_seconds`` when one is set.

        Without a timeout this is the plain synchronous call. With one, the call
        runs in a daemon thread (so a hung runtime cannot keep the process alive
        at exit) and the number of runtime calls in flight is capped at
        ``RUNTIME_MAX_IN_FLIGHT``. It fails closed with RuntimeUnavailableError
        when no slot is free (no queueing), when the thread cannot start, and on
        timeout. A result that arrives after the timeout is dropped: nothing
        reads it, so it is never sealed or returned. Anything the runtime raises
        (any ``BaseException``) is stored by the worker and re-raised, the same
        object, by the waiting thread, as in the no-timeout mode; it never reaches
        ``threading.excepthook``.

        A slot is released exactly once per acquire: by the worker when the runtime
        returns, or at once if the worker could not be started.
        """
        timeout = self._runtime_timeout
        if timeout is None:
            return self._runtime.run(route, call)
        if not self._runtime_slots.acquire(blocking=False):
            raise RuntimeUnavailableError(_RUNTIME_BUSY)
        # Everything from here until the worker thread has really started is inside
        # the try statement, so ANY failure releases the slot, once. After the
        # thread has started the slot belongs to the worker: its ``finally``
        # releases it when the runtime returns, and nothing here may free it earlier.
        slot_released: threading.Lock | None = None
        thread: threading.Thread | None = None
        done_event: threading.Event
        outcome: list[Result | BaseException] = []
        start_failed = False
        try:
            # Taken (and never given back) by whoever releases the slot first, so
            # the worker and a failure path can never both release it.
            lock = threading.Lock()
            slot_released = lock
            done_event = threading.Event()
            outcome = []

            def release_slot() -> None:
                if lock.acquire(blocking=False):
                    self._runtime_slots.release()

            def worker() -> None:
                try:
                    try:
                        outcome.append(self._runtime.run(route, call))
                    except BaseException as exc:  # re-raised by the waiting thread, same object
                        outcome.append(exc)
                    done_event.set()
                finally:
                    release_slot()

            thread = threading.Thread(target=worker, name="ztax-gateway-runtime", daemon=True)
            thread.start()
        except BaseException as exc:
            if thread is not None and thread.ident is not None:
                # The worker thread began running: it owns the slot now.
                pass
            elif slot_released is None:
                # Not even the lock exists, so no worker can: release directly.
                self._runtime_slots.release()
            elif slot_released.acquire(blocking=False):
                # No worker ever ran: release, once.
                self._runtime_slots.release()
            if not isinstance(exc, Exception):
                raise  # KeyboardInterrupt, SystemExit: not ours to turn into a refusal
            start_failed = True
        # Raised after the except block, so it carries no __context__ (the error
        # from the failed start can hold OS text).
        if start_failed:
            raise RuntimeUnavailableError(_RUNTIME_NO_WORKER)
        if not done_event.wait(timeout):
            raise RuntimeUnavailableError(_RUNTIME_TIMED_OUT)
        result = outcome[0]
        if isinstance(result, BaseException):
            raise result
        return result

    def _run(self, route: Route, call: Call, timing: _CallTiming | None = None) -> Reply:
        """Run the routed model and shape its reply.

        ``timing`` receives the whole milliseconds spent in :meth:`_call_runtime`
        (including a wait for a slot or for the timeout), however that call ends.
        An ``Exception`` the Gateway has no name for, from the runtime or from the
        output check, is logged once by class name only and re-raised wrapped in a
        private error so the caller can write one evidence record for it.
        """
        unexpected: Exception  # set only by the except block below
        try:
            started = _monotonic_ns()
            try:
                result = self._call_runtime(route, call)
            finally:
                if timing is not None:
                    timing.ms = max(0, (_monotonic_ns() - started) // _NS_PER_MS)
            check_result(call.kind, result)
            if timing is not None:
                # Counts only, each checked here too: a runtime may return anything.
                timing.input_tokens = token_count(result.input_tokens)
                timing.output_tokens = token_count(result.output_tokens)
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
        except _EXPECTED_RUNTIME_FAILURES:
            raise
        except Exception as exc:
            # Class name only: the text and traceback can carry provider text.
            log.error(
                "gateway: runtime raised an unexpected error",
                extra={"error_type": type(exc).__name__},
            )
            unexpected = exc
        # Raised after the except block, so it carries no __context__ or __cause__.
        raise _UnexpectedRuntimeError(unexpected)

    # ------------------------------------------------------------------
    # Evidence helpers (Ch.17 §21)
    # ------------------------------------------------------------------

    def _seal_success(
        self,
        provenance: Provenance,
        input_bytes: bytes,
        reply: Reply,
        *,
        latency_ms: int = 0,
        input_tokens: int | None = None,
        output_tokens: int | None = None,
    ) -> str:
        """Build and write a successful-invocation evidence record.

        The outcome is derived from the authority outcome so that the
        evidence vocabulary (PROPOSE / PRIORITIZE / REVIEW / …) matches
        what the governed context declared.

        WP5 (Decision D-09): the prompt content hash is set here — only when
        the call actually reaches the model runtime.  BLOCK records (refused or
        killed before the runtime) must never carry the hash, because evidence
        must not claim a prompt was used when it was not.

        Returns the ``ai_invocation_id`` of the sealed record so the caller can
        emit a linked check event without touching shared state.
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
        # Latency is measured here. Tokens are the provider's own counts when it gave
        # valid ones, else 0. Cost is computed only when prices were configured and
        # both counts are present, else 0.0: no price is ever assumed.
        cost = 0.0
        pricing = self._token_pricing
        if pricing is not None and input_tokens is not None and output_tokens is not None:
            cost = pricing.cost_usd(input_tokens, output_tokens)
        builder.set_usage(
            UsageMetrics(
                latency_ms=latency_ms,
                input_tokens=input_tokens or 0,
                output_tokens=output_tokens or 0,
                monetary_cost_usd=cost,
            )
        )
        # WP5 Part A: resolve the route's prompt label to a PromptProfile and
        # set the content hash on the builder.  No registry or no matching
        # profile: leave the hash None and log one warning; never fail the call.
        if self._prompt_registry is not None:
            profile = self._prompt_registry.get(provenance.prompt_profile)
            if profile is not None:
                builder.set_prompt_content_hash(profile.content_hash)
            else:
                log.warning(
                    "gateway: no PromptProfile for label %r; prompt_content_hash will be None",
                    provenance.prompt_profile,
                )
        record = builder.build()
        self._write_sink(record)
        return record.ai_invocation_id

    def _seal_block(
        self,
        provenance: Provenance,
        input_bytes: bytes,
        *,
        refusal_code: str,
        original_error: GatewayError | None = None,
        detail: str = "",
        latency_ms: int = 0,
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
        builder.add_guardrail(GuardrailResult("governance.authority", refusal_code, detail))
        # Latency is set only for a call that reached the runtime; a refusal before
        # it keeps the zero default. Tokens and cost are never populated here.
        builder.set_usage(UsageMetrics(latency_ms=latency_ms))
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
        failed = False
        try:
            self._evidence_sink.write(record)
        except Exception as exc:
            # Class name only: the exception text and traceback can carry
            # provider or store text, which must not reach the logs.
            log.error(
                "gateway: evidence sink write failed (BLOCK record)",
                extra={
                    "use_case": record.caller_security_context_ref,
                    "error_type": type(exc).__name__,
                },
            )
            failed = True
        # Raised after the except block, so the sink's exception is not attached
        # to the caller's error as its ``__context__``.
        if failed and original_error is not None:
            raise original_error from None
        # residency / runtime-failure paths: the caller raises after us,
        # so simply return; the caller raises the right error.

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
            log.error(
                "gateway: evidence sink write failed",
                extra={
                    "use_case": record.caller_security_context_ref,
                    "error_type": type(exc).__name__,
                },
            )
            raise GatewayError(
                grpc.StatusCode.UNAVAILABLE, REASON_UNAVAILABLE, DETAIL_EVIDENCE_WRITE_FAILED
            ) from exc


def _valid_timeout(value: object) -> bool:
    """True for a positive, finite int or float (not a bool) that a wait can take.

    The upper bound is ``threading.TIMEOUT_MAX``: a larger value would be accepted
    here and then raise OverflowError when the wait starts. Never raises: an int too
    large for a float (``10**400``), or a subclass that raises, is simply not valid.
    """
    try:
        if isinstance(value, bool) or not isinstance(value, int | float):
            return False
        as_float = float(value)
        return math.isfinite(as_float) and 0 < as_float <= threading.TIMEOUT_MAX
    except Exception:
        # Anything odd (a subclass whose __float__ or __class__ raises, an int too
        # large for a float, ...) is simply not a valid timeout.
        return False


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
        except Exception as exc:
            log.error("gateway: unhandled error", extra={"error_type": type(exc).__name__})
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


def _parse_kill_state(raw: bytes) -> tuple[bool, frozenset[str]] | None:
    """``(global_kill, killed)`` from registry bytes, or None if unreadable.

    Only these two keys are read. A missing key means "not killed" (the schema
    treats both as optional and Go reads them the same way); a wrong type is
    unreadable.
    """
    try:
        doc = json.loads(raw.decode("utf-8"))
    except (UnicodeDecodeError, ValueError):
        return None
    if not isinstance(doc, dict):
        return None
    global_kill = doc.get("global_kill", False)
    killed = doc.get("killed", [])
    if not isinstance(global_kill, bool) or not isinstance(killed, list):
        return None
    if not all(isinstance(k, str) and k for k in killed):
        return None
    return global_kill, frozenset(killed)


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
              "permitted_data_classes": [str], # required; missing = ValueError
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

    The top-level ``"killed"`` (list of use case ids) and ``"global_kill"``
    (bool) keys are NOT read here. A running Gateway reads them live, from the
    same file, before every call (``Gateway._refresh_kill_state``), so a kill
    needs no restart.
    """
    doc: dict[str, Any] = json.loads(path.read_text(encoding="utf-8"))
    registry = UseCaseRegistry()
    routes: dict[str, Route] = {}
    for entry in doc.get("use_cases", []):
        uc_id = entry["use_case_id"]
        if "permitted_data_classes" not in entry:
            raise ValueError(
                f"load_config: use case {uc_id!r} is missing 'permitted_data_classes'"
            )
        dc_raw = entry["permitted_data_classes"]
        if not isinstance(dc_raw, list):
            raise ValueError(
                f"load_config: use case {uc_id!r} 'permitted_data_classes' "
                "must be a JSON array"
            )
        for label in dc_raw:
            if not isinstance(label, str):
                raise ValueError(
                    f"load_config: use case {uc_id!r} data class {label!r} is not a string"
                )
            if label not in _VALID_DATA_CLASSES:
                raise ValueError(
                    f"load_config: use case {uc_id!r} data class {label!r} "
                    f"is not a valid PRIV-001 label (expected one of {sorted(_VALID_DATA_CLASSES)})"
                )
        max_risk_tier = RiskTier(entry["max_risk_tier"])
        if max_risk_tier == RiskTier.T4:
            raise ValueError(
                f"load_config: use case {uc_id!r} declares max_risk_tier 'T4', "
                "which is refused unconditionally (REQ-0107)"
            )
        uc = UseCase(
            use_case_id=uc_id,
            owner=entry["owner"],
            description=entry["description"],
            max_risk_tier=max_risk_tier,
            max_authority=AuthorityOutcome(entry["max_authority"]),
            permitted_regions=frozenset(entry.get("permitted_regions", [])),
            permitted_data_classes=frozenset(dc_raw),
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
    gateway = Gateway(registry, routes, _runtime(), region, train, kill_state_path=Path(config))

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
