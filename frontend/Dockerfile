# ztax-web — the browser client (ADR-0019).
#
# Two stages, and the shipped one contains no JavaScript toolchain: the build
# produces static files and nginx serves them. Node exists at build time and
# nowhere else, so nothing in the running container can execute application code
# that was not compiled here.
#
# This is not a backend service and must not become one. ADR-0019 C7 is explicit
# that server-side rendering on our infrastructure enters the cell model —
# residency routing, security context, zone model, audited logging. Static files
# and a proxy pass do not; the moment this image renders on the server, it needs
# all of that.

ARG NODE_VERSION=22
ARG NODE_IMAGE=node:${NODE_VERSION}-alpine
ARG RUNTIME_IMAGE=nginxinc/nginx-unprivileged:1.27-alpine

# -----------------------------------------------------------------------------
FROM ${NODE_IMAGE} AS build

WORKDIR /src

# Dependencies first, so a source change does not re-resolve the graph.
# `npm ci` installs exactly the lockfile, which is the point of committing one.
COPY package.json package-lock.json ./
RUN npm ci

COPY . .

# Type errors fail the image, not just the pipeline. A build that emits a bundle
# from code the compiler rejected is a build nobody should deploy.
RUN npm run build

# -----------------------------------------------------------------------------
# ztax-web — nginx, unprivileged, serving static files only
# -----------------------------------------------------------------------------
FROM ${RUNTIME_IMAGE} AS ztax-web

# The unprivileged image runs as uid 101 and listens on 8080; no root anywhere
# in this container, and no capability to bind a privileged port.
#
# The config is a template: the entrypoint runs envsubst over /etc/nginx/templates
# at startup, so the cell address stays configuration rather than something baked
# into the image (ADR-0017 §2.2).
COPY nginx.conf.template /etc/nginx/templates/default.conf.template
COPY --from=build /src/dist /usr/share/nginx/html

# The default points at the compose service name. A deployment overrides it; it
# is an address, not a secret, so it travels as a plain environment variable.
ENV ZTAX_API_ORIGIN=http://ztax-core:8080

EXPOSE 8080

# No HEALTHCHECK: liveness is an HTTP probe against /healthz owned by the
# orchestrator, the same arrangement the ztax-core image documents.
