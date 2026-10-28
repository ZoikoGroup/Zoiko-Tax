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
own public names (``Gateway``, ``load_config``, ``serve``) and
``GroqDemoRuntime``, imported from the sibling ``tools/`` module exactly as
``live_demo.py`` already does.

``GROQ_API_KEY`` and ``GROQ_MODEL`` are resolved first, by constructing
``GroqDemoRuntime`` before anything else runs. If either is missing, or
``GROQ_MODEL`` names a floating alias, a clear message -- never the key --
is printed to stderr and the process exits non-zero before the config file
is read, the Gateway is built, or any port is bound.
"""

from __future__ import annotations

import logging
import os
import sys
from pathlib import Path

from groq_demo_runtime import GroqDemoConfigError, GroqDemoRuntime

from ztax_gateway.server import Gateway, load_config, serve

log = logging.getLogger("ztax_gateway.serve_live_demo")

_DEMO_WARNING = (
    "serve_live_demo: DEMO ONLY -- never production, synthetic public data "
    "only, no authority."
)


def _env(name: str, default: str = "") -> str:
    return os.environ.get(name, default).strip()


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

    registry, routes = load_config(Path(config))
    gateway = Gateway(registry, routes, runtime, region, train)

    model = _env("GROQ_MODEL")
    print(f"serve_live_demo: model={model!r} provider='groq-demo' address={address!r}")
    print(_DEMO_WARNING)

    server, _ = serve(gateway, address, None, insecure_local=True)
    log.info("serve_live_demo: serving", extra={"address": address, "region": region})
    server.wait_for_termination()
    return 0


if __name__ == "__main__":
    sys.exit(main())
