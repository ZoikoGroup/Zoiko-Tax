# The Governed Model Gateway.
#
# Three properties, and each is the same requirement the ztax-core image holds,
# for the same reasons:
#
#   Minimal     the runtime stage carries the interpreter and the package, and
#               no build tooling. ADR-0006 §2.1 puts this in its own trust zone;
#               a zone with a compiler in it is a larger zone.
#   Non-root    the Gateway sits in the authorization path of every AI call.
#   Configless  configuration is environment variables only (ADR-0017 §2.1),
#               and no credential is baked in — provider credentials live in a
#               separate vault namespace reachable only by this deployable
#               (ADR-0017 §2.5).

ARG PYTHON_VERSION=3.12
ARG BASE_IMAGE=python:${PYTHON_VERSION}-slim

# -----------------------------------------------------------------------------
FROM ${BASE_IMAGE} AS build

WORKDIR /src

# Dependencies resolve from pyproject; there are none at runtime today, and the
# stage exists so that adding one does not mean restructuring the image.
COPY pyproject.toml ./
COPY src/ ./src/

RUN pip install --no-cache-dir --target=/out .

# -----------------------------------------------------------------------------
# ztax-model-gateway
# -----------------------------------------------------------------------------
FROM ${BASE_IMAGE} AS ztax-model-gateway

# A dedicated unprivileged user rather than the image's default. Named rather
# than numeric so the intent survives a base-image change.
RUN groupadd --system ztax && useradd --system --gid ztax --no-create-home ztax

COPY --from=build /out /usr/local/lib/python3.12/site-packages

USER ztax:ztax

# No port yet: the transport waits on W2 lane K's contract pipeline, so this
# image currently carries the governance layer and nothing that listens. The
# EXPOSE and the entrypoint land with the generated server, and stating that
# here is better than exposing a port nothing serves.

# No HEALTHCHECK: liveness is an HTTP probe owned by the orchestrator, the same
# arrangement the ztax-core and ztax-web images document.

ENTRYPOINT ["python", "-c", "import ztax_gateway; print('ztax-model-gateway: governance layer loaded; transport pending W2 lane K')"]
