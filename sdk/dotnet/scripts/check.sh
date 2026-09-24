#!/usr/bin/env sh
# The .NET SDK's gate. CI runs exactly this; so does a developer.
#
#   sh scripts/check.sh           needs the SDK pinned in global.json on PATH
#   sh scripts/check-docker.sh    the same, inside the pinned SDK image
#
# 1. Regenerate the model types and fail on any difference from the committed
#    file (ADR-0010 §2.1). A contract change this SDK has not been regenerated
#    for is a build failure rather than a discovery.
# 2. Restore in locked mode: the package graph is the committed one or nothing.
# 3. Build with warnings as errors.
# 4. Test.
set -eu

here=$(cd "$(dirname "$0")/.." && pwd)
cd "$here"

export DOTNET_NOLOGO=1
export DOTNET_CLI_TELEMETRY_OPTOUT=1

generated=src/ZoikoTax.Sdk/Generated/Models.g.cs
fresh=$(mktemp)
trap 'rm -f "$fresh"' EXIT

echo "==> regenerate $generated and compare"
sh scripts/generate.sh "$fresh"
if ! diff -u "$generated" "$fresh"; then
  echo >&2
  echo "error: $generated is not what the contract generates." >&2
  echo "       Run 'sh scripts/generate.sh' and commit the result; never edit it by hand." >&2
  exit 1
fi

echo "==> restore (locked)"
dotnet restore ZoikoTax.Sdk.sln --locked-mode

echo "==> build (warnings are errors)"
dotnet build ZoikoTax.Sdk.sln --no-restore --configuration Release -warnaserror

echo "==> test"
dotnet test ZoikoTax.Sdk.sln --no-build --configuration Release
