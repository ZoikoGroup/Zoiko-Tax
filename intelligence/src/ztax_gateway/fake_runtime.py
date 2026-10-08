"""A deterministic fake ModelRuntime for development and e2e testing.

This runtime returns canned, predictable results without calling any real
model provider. It is the only runtime that may be used without a reviewed
provider approval, vault credentials or an entry in the egress allow-list
(ADR-0006 §2.1, ADR-0017 §2.5).

Rules this module must never break (ADR-0006):
  - ``confidence`` is always a canonical decimal string in [0, 1]. Never a
    float. Never ``"1.0000000000001"``. The gateway wire encoder will reject
    anything else (``_CONFIDENCE`` in ``wire.py``).
  - Results land in ``Result``, which carries no fiscal types. There is no
    path from ``Result`` to any type under ``internal/domain/fiscal``
    (ADR-0006 §2.6).
  - Transient failures map to ``RuntimeUnavailableError``; a missing runtime
    maps to ``RuntimeNotConfiguredError``. No other exception crosses the
    seam.

Usage in local development::

    from ztax_gateway.fake_runtime import FakeRuntime
    gateway = Gateway(registry, routes, FakeRuntime(), region, train)

Usage in tests — inject the canned table::

    runtime = FakeRuntime(
        canned={
            ("classification-review", Kind.CLASSIFICATION_PROPOSAL): Result(
                proposed_code="GST-DIGITAL-SERVICE",
                confidence="0.9500",
            ),
        }
    )
"""

from __future__ import annotations

from .runtime import (
    Result,
    Route,
    RuntimeUnavailableError,
)
from .wire import Call, Kind

# ---------------------------------------------------------------------------
# Default canned results — one per Kind.
#
# These are the fallback answers returned when no use-case-specific entry is
# registered.  They satisfy ``check_result`` in runtime.py:
#   SUGGESTION        → text must be non-empty
#   EXTRACTION        → fields must be non-empty
#   CLASSIFICATION_PROPOSAL → proposed_code must be non-empty
#
# Confidence is "0.9000" — a canonical decimal string in [0, 1], never a
# float (ADR-0006 §2.3 and wire._CONFIDENCE).
# ---------------------------------------------------------------------------
_DEFAULT_SUGGESTION = Result(
    text="[fake] This is a canned suggestion from the fake runtime.",
    confidence="0.9000",
)

_DEFAULT_EXTRACTION = Result(
    fields={"extracted_key": "[fake] canned-value"},
    confidence="0.9000",
)

_DEFAULT_PROPOSAL = Result(
    proposed_code="FAKE-UNCLASSIFIED",
    confidence="0.9000",
)

_DEFAULTS: dict[Kind, Result] = {
    Kind.SUGGESTION: _DEFAULT_SUGGESTION,
    Kind.EXTRACTION: _DEFAULT_EXTRACTION,
    Kind.CLASSIFICATION_PROPOSAL: _DEFAULT_PROPOSAL,
}


class FakeRuntime:
    """A deterministic fake ModelRuntime for development and e2e testing.

    Accepts an optional ``canned`` mapping of ``(use_case_id, Kind)`` to the
    ``Result`` that should be returned.  Falls back to the module-level
    defaults when no entry matches.

    To simulate a transient failure, pass the use-case id strings to
    ``fail_on``.  When the call's use-case id is in ``fail_on``, the runtime
    raises ``RuntimeUnavailableError`` — the only retryable error
    (ADR-0006 §2.4).
    """

    def __init__(
        self,
        canned: dict[tuple[str, Kind], Result] | None = None,
        fail_on: frozenset[str] | None = None,
    ) -> None:
        """
        Args:
            canned: Optional ``{(use_case_id, Kind): Result}`` override table.
            fail_on: Optional set of use-case ids that always raise
                ``RuntimeUnavailableError``.  Simulates a transient provider
                failure for specific use cases.
        """
        self._canned: dict[tuple[str, Kind], Result] = dict(canned or {})
        self._fail_on: frozenset[str] = frozenset(fail_on or set())

    def run(self, route: Route, call: Call) -> Result:
        """Return the canned result for *call*, or the kind default.

        Raises:
            RuntimeUnavailableError: if the call's use case is in
                ``fail_on``.  This is the only retryable failure category
                (ADR-0006 §2.4, runtime.RuntimeUnavailableError).
        """
        use_case = call.governance.use_case
        if use_case in self._fail_on:
            raise RuntimeUnavailableError(
                f"fake runtime: simulated transient failure for {use_case!r}"
            )
        key = (use_case, call.kind)
        if key in self._canned:
            return self._canned[key]
        return _DEFAULTS[call.kind]
