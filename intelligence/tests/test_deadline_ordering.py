"""Assert that the timeout layers are strictly ordered:

    model socket (_TOTAL_SECONDS)
    < gateway runtime (GATEWAY_RUNTIME_TIMEOUT_SECONDS)
    < Go caller deadline (GO_DEADLINE_SECONDS)

The Go value comes from ZTAX_AI_GATEWAY_DEADLINE default "5s" in
backend/internal/platform/config/config.go and the agreed starting point
per Kaushal's reply of 23 Nov 2026.

These limits are starting points, to be re-checked after real models are tested.
"""
import os
import sys

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "tools"))

from groq_demo_runtime import GATEWAY_RUNTIME_TIMEOUT_SECONDS  # type: ignore[import-not-found]

from ztax_gateway import local_runtime

# Go caller outer deadline — from backend/internal/platform/config/config.go
# ZTAX_AI_GATEWAY_DEADLINE default "5s". Agreed: stays at 5 s (Kaushal, 23 Nov 2026).
GO_DEADLINE_SECONDS: float = 5.0


def test_model_socket_shorter_than_gateway() -> None:
    assert local_runtime._TOTAL_SECONDS < GATEWAY_RUNTIME_TIMEOUT_SECONDS, (
        f"Model socket total ({local_runtime._TOTAL_SECONDS} s) must be shorter than "
        f"gateway runtime timeout ({GATEWAY_RUNTIME_TIMEOUT_SECONDS} s)"
    )


def test_gateway_shorter_than_go() -> None:
    assert GATEWAY_RUNTIME_TIMEOUT_SECONDS < GO_DEADLINE_SECONDS, (
        f"Gateway runtime timeout ({GATEWAY_RUNTIME_TIMEOUT_SECONDS} s) must be shorter than "
        f"Go caller deadline ({GO_DEADLINE_SECONDS} s)"
    )


def test_full_ordering() -> None:
    assert local_runtime._TOTAL_SECONDS < GATEWAY_RUNTIME_TIMEOUT_SECONDS < GO_DEADLINE_SECONDS, (
        f"Required ordering: model ({local_runtime._TOTAL_SECONDS} s) "
        f"< gateway ({GATEWAY_RUNTIME_TIMEOUT_SECONDS} s) "
        f"< Go ({GO_DEADLINE_SECONDS} s)"
    )
