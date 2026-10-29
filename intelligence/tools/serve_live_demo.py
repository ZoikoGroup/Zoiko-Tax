"""Long-running Groq demo Gateway server -- DEMO ONLY.

DEMO ONLY. Never production. Synthetic public data only. No authority.

The long-running counterpart of ``ztax_gateway.server.main()``, with
``GroqDemoRuntime`` (``groq_demo_runtime.py``, this same directory)
substituted for whatever ``server.py``'s own ``_runtime()`` would otherwise
select. Reads the same ``ZTAX_GATEWAY_*`` environment variables
``server.main()`` reads -- config path, region, AI train version, address,
and whether to listen insecurely in a development environment -- so it is a
drop-in replacement for the ``ztax-model-gateway`` service's entrypoint
(see ``docker-compose.live-demo.yml``), and nothing else about the
deployment changes.

No edit to intelligence/src: everything below is built from ``server.py``'s
own public names (``Gateway``, ``load_config``, ``serve``), ``Route`` and
``RoutingTable`` from ``runtime.py``, and ``GroqDemoRuntime``, imported from
the sibling ``tools/`` module exactly as ``live_demo.py`` already does.

``GROQ_API_KEY`` and ``GROQ_MODEL`` are resolved first, by constructing
``GroqDemoRuntime`` before anything else runs. If either is missing, or
``GROQ_MODEL`` names a floating alias, a clear message -- never the key --
is printed to stderr and the process exits non-zero before the config file
is read, the Gateway is built, or any port is bound.

The route loaded from ``ZTAX_GATEWAY_CONFIG`` (``registry.live-demo.json``
for this script -- see ``docker-compose.live-demo.yml``) labels its model
and provider "model:groq-live-demo" / "provider:groq-demo": a static
placeholder, because ``load_config`` is a plain ``json.loads`` and a JSON
file cannot read an environment variable. ``_patched_routes`` below replaces
that placeholder in memory, after loading, with the real GROQ_MODEL value,
so the Provenance table shows the model actually running. This has no
effect on which model is actually called: ``route.model_profile`` is a
provenance label only (``server.py``'s ``_run``), never read by
``GroqDemoRuntime`` (``groq_demo_runtime.py`` resolves ``GROQ_MODEL`` itself,
independently of any route).
"""

from __future__ import annotations

import logging
import os
import sys
from pathlib import Path

from groq_demo_runtime import GroqDemoConfigError, GroqDemoRuntime

from ztax_gateway.governance import UseCaseRegistry
from ztax_gateway.runtime import Route, RoutingTable
from ztax_gateway.server import Gateway, load_config, serve

log = logging.getLogger("ztax_gateway.serve_live_demo")

_DEMO_WARNING = (
    "serve_live_demo: DEMO ONLY -- never production, synthetic public data "
    "only, no authority."
)

# Matches the static placeholder in registry.live-demo.json's route -- see
# the module docstring and _patched_routes below.
_LIVE_MODEL_PROFILE_PREFIX = "model:groq-live-demo"
_LIVE_PROVIDER_PROFILE = "provider:groq-demo"


def _env(name: str, default: str = "") -> str:
    return os.environ.get(name, default).strip()


def _patched_routes(registry: UseCaseRegistry, routes: RoutingTable, model: str) -> RoutingTable:
    """Relabel every loaded route's model/provider profile with *model*.

    Built only from RoutingTable's and UseCaseRegistry's own public methods
    (``ids()``, ``route()``) and a fresh ``Route``/``RoutingTable`` -- no
    edit to intelligence/src, and the registry file on disk is never
    rewritten. Every use case in this registry is relabelled uniformly,
    since registry.live-demo.json exists only to back this one live-demo
    script (see docker-compose.live-demo.yml).
    """
    patched: dict[str, Route] = {}
    for use_case_id in registry.ids():
        route = routes.route(use_case_id)
        patched[use_case_id] = Route(
            model_profile=f"{_LIVE_MODEL_PROFILE_PREFIX}:{model}",
            provider_profile=_LIVE_PROVIDER_PROFILE,
            prompt_profile=route.prompt_profile,
        )
    return RoutingTable(patched)


def main() -> int:
    """Run a Gateway from the environment, backed by a real Groq model.

    Mirrors ``ztax_gateway.server.main()``'s own structure, but resolves
    ``GroqDemoRuntime`` first -- before any ``ZTAX_GATEWAY_*`` variable is
    even read -- so a missing key or an aliased model never gets as far as
    loading the registry or binding a port.
    """
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s %(message)s")

    try:
        runtime = GroqDemoRuntime()
    except GroqDemoConfigError as exc:
        print(f"serve_live_demo: {exc}", file=sys.stderr)
        return 2

    config = _env("ZTAX_GATEWAY_CONFIG")
    region = _env("ZTAX_GATEWAY_REGION")
    train = _env("ZTAX_GATEWAY_AI_TRAIN")
    address = _env("ZTAX_GATEWAY_ADDRESS", "[::]:8443")
    if not (config and region and train):
        print(
            "serve_live_demo: ZTAX_GATEWAY_CONFIG, ZTAX_GATEWAY_REGION and "
            "ZTAX_GATEWAY_AI_TRAIN are required",
            file=sys.stderr,
        )
        return 2

    insecure = (
        _env("ZTAX_GATEWAY_INSECURE_LOCAL") == "true" and _env("ZTAX_ENVIRONMENT") == "development"
    )
    if not insecure:
        print(
            "serve_live_demo: refusing to listen -- this DEMO ONLY script never "
            "adds mTLS support, so it only serves when ZTAX_GATEWAY_INSECURE_LOCAL="
            "true and ZTAX_ENVIRONMENT=development",
            file=sys.stderr,
        )
        return 2

    model = _env("GROQ_MODEL")
    registry, routes = load_config(Path(config))
    routes = _patched_routes(registry, routes, model)
    gateway = Gateway(registry, routes, runtime, region, train)

    print(f"serve_live_demo: model={model!r} provider='groq-demo' address={address!r}")
    print(_DEMO_WARNING)

    server, _ = serve(gateway, address, None, insecure_local=True)
    log.info("serve_live_demo: serving", extra={"address": address, "region": region})
    server.wait_for_termination()
    return 0


if __name__ == "__main__":
    sys.exit(main())
