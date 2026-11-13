"""Build the demo servers' Gateway: live kill flags, prompt profiles, check events.

``build_gateway`` is the one place the long-running demo mains
(``tools/serve_live_demo.py`` and ``tools/report_ai_http.py``) construct a
Gateway. It reads the prompt profiles once, at startup, from the file named by
``ZTAX_GATEWAY_PROMPT_PROFILES`` (no default path and no fallback), fails at
startup if any route's prompt-profile label has no profile, and passes the
registry file as ``kill_state_path`` so ``killed`` and ``global_kill`` are
read live. ``server.py`` has its own ``main()`` and is not built here.

Errors name an environment variable, a field or a route label, never prompt
text or file content.
"""

from __future__ import annotations

import logging
import os
from collections.abc import Callable
from pathlib import Path
from typing import Final

from .evidence_file_sink import EvidenceFileConfigError, default_evidence_sink
from .governance import UseCaseRegistry
from .invocation_check_event import LoggingCheckEventSink
from .production_gates import PromptProfileRegistry
from .prompt_profile_file import PromptProfileFileError, load_prompt_profiles
from .runtime import ModelRuntime, RoutingTable
from .server import Gateway, load_config

__all__: list[str] = ["ENV_PROMPT_PROFILES", "GatewayFactoryError", "build_gateway"]

log = logging.getLogger("ztax_gateway.gateway_factory")

ENV_PROMPT_PROFILES: Final = "ZTAX_GATEWAY_PROMPT_PROFILES"


class GatewayFactoryError(ValueError):
    """The Gateway cannot be built safely. Never carries prompt text."""


def build_gateway(
    config_path: Path,
    region: str,
    train: str,
    *,
    runtime_factory: Callable[[PromptProfileRegistry], ModelRuntime],
    route_transform: Callable[[UseCaseRegistry, RoutingTable], RoutingTable] | None = None,
    runtime_timeout_seconds: float | None = None,
) -> Gateway:
    """Load config and prompt profiles, check them, and return the Gateway.

    ``runtime_factory`` receives the loaded profiles and returns the runtime.
    If that runtime has a ``verify_prompts(use_cases, routes)`` method it is
    called, so a mismatch between the profile text and the text the runtime
    sends stops startup.

    ``runtime_timeout_seconds`` is passed to the Gateway unchanged: ``None`` (the
    default) keeps the runtime call without a time limit.
    """
    profiles_path = os.environ.get(ENV_PROMPT_PROFILES, "").strip()
    if not profiles_path:
        raise GatewayFactoryError(f"{ENV_PROMPT_PROFILES} is required and has no default")
    try:
        profiles = load_prompt_profiles(Path(profiles_path))
    except PromptProfileFileError as exc:
        raise GatewayFactoryError(str(exc)) from None

    registry, routes = load_config(config_path)
    if route_transform is not None:
        routes = route_transform(registry, routes)

    used: set[str] = set()
    for use_case_id in registry.ids():
        label = routes.route(use_case_id).prompt_profile
        if profiles.get(label) is None:
            raise GatewayFactoryError(f"no prompt profile for route {label!r}")
        used.add(label)
    if profiles.profile_ids() - used:
        log.warning("gateway_factory: a prompt profile is not used by any route")

    runtime = runtime_factory(profiles)
    verify = getattr(runtime, "verify_prompts", None)
    if verify is not None:
        verify(registry, routes)

    # Optional file copy of every evidence record (ZTAX_EVIDENCE_FILE_PATH). Off unless set.
    # A path that is set and not usable stops start-up with the sink's own fixed text.
    try:
        evidence_sink = default_evidence_sink()
    except EvidenceFileConfigError as exc:
        raise GatewayFactoryError(str(exc)) from None

    return Gateway(
        registry,
        routes,
        runtime,
        region,
        train,
        evidence_sink=evidence_sink,
        kill_state_path=config_path,
        prompt_profile_registry=profiles,
        check_event_sink=LoggingCheckEventSink(),
        runtime_timeout_seconds=runtime_timeout_seconds,
    )
