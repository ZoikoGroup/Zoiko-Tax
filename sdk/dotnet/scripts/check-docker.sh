#!/usr/bin/env sh
# Run scripts/check.sh inside the pinned .NET SDK image, for a machine with
# no SDK installed. The image is pinned by digest so that "the gate passed
# locally" and "the gate passed in CI" refer to the same compiler.
#
# NUGET_CACHE may name a host directory to reuse the package cache between
# runs; unset, packages are fetched each time.
set -eu

image="mcr.microsoft.com/dotnet/sdk:8.0.425@sha256:78235e09001f52b6592c458ac010775ebac6725422e80cd0c1650590f67b2743"

here=$(cd "$(dirname "$0")/.." && pwd)
# The repository root, because the generator reads the contract from
# contracts/, outside this directory. `pwd -W` gives a Windows path under Git
# Bash, which is what Docker Desktop mounts; elsewhere it does not exist.
repo=$(cd "$here/../.." && (pwd -W 2>/dev/null || pwd))

cache_mount=""
if [ -n "${NUGET_CACHE:-}" ]; then
  cache_mount="-v ${NUGET_CACHE}:/root/.nuget/packages"
fi

# MSYS_NO_PATHCONV stops Git Bash rewriting the container paths.
# shellcheck disable=SC2086
MSYS_NO_PATHCONV=1 docker run --rm \
  -v "$repo:/repo" $cache_mount \
  -w /repo/sdk/dotnet \
  "$image" sh scripts/check.sh
