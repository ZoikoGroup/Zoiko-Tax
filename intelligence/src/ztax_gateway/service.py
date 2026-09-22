"""The transport seam.

Nothing here serves traffic yet, and the reason is worth stating rather than
leaving as a gap somebody discovers.

ADR-0006 §2.2 makes ``contracts/schemas`` the canonical schema authority, with
protobuf *derived* from it and a CI job failing the build if a proto has drifted
or if a hand edit introduced a field with no canonical counterpart. Writing
``.proto`` files here by hand would create exactly the second schema authority
that clause exists to prevent — and it would do so in the component where the
two languages have to agree most precisely.

So the transport waits on W2 lane K's contract pipeline. What exists now is the
part that has to be right regardless of how bytes arrive: the governance gate in
``governance.py`` and the decimal boundary in ``decimal_wire.py``.

When the pipeline lands, the handler below is the shape of it. Every entry point
follows the same four steps, and the order is not negotiable:

    1. Reconstruct Provenance from the request metadata.
    2. authorise(registry, provenance) - refuse before touching the payload.
    3. Do the work.
    4. Log the crossing with the redacted context (ADR-0006 §2.7).

Step 2 precedes step 3 because a Gateway that inspected a payload before
deciding whether it was allowed to would have already processed data it may not
be permitted to see, in the region it may not be permitted to see it in.
"""

from __future__ import annotations

import logging
from collections.abc import Callable

from .governance import GovernanceRefusedError, UseCase, UseCaseRegistry, authorise
from .provenance import Provenance

log = logging.getLogger("ztax_gateway")


def guarded[T](
    registry: UseCaseRegistry,
    provenance: Provenance,
    work: Callable[[UseCase], T],
) -> T:
    """Run ``work`` only if the governance context permits it.

    This is the whole of the Gateway's contract with its callers, and it takes
    no options. There is no ``force``, no ``skip_checks`` and no ``override``,
    because ADR-0006 §2.5 puts the decision after the call leaves the caller —
    a parameter that could relax it would move the decision back.

    ``work`` receives the resolved ``UseCase`` so it can read the ceilings it is
    operating under. It cannot change them: ``UseCase`` is frozen.
    """
    try:
        use_case = authorise(registry, provenance)
    except GovernanceRefusedError as refusal:
        # A refusal is logged as a governance event, not as an error. It is the
        # Gateway working, and an operator reviewing AI activity needs to see
        # the refusals as clearly as the permits.
        log.info(
            "ai crossing refused",
            extra={"refusal": refusal.refusal.value, "detail": refusal.detail, **refusal.context},
        )
        raise

    log.info("ai crossing permitted", extra=provenance.redacted())
    return work(use_case)
