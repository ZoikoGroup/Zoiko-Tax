#!/usr/bin/env sh
# Regenerate the model types from the contract.
#
#   sh scripts/generate.sh              writes src/ZoikoTax.Sdk/Generated/Models.g.cs
#   sh scripts/generate.sh <path>       writes <path> instead (the drift check uses this)
#
# The generator is NSwag, pinned in .config/dotnet-tools.json, reading the 3.1
# export rather than the 3.2 contract because no .NET generator reads 3.2 yet
# (ADR-0010 §2.2). Every option that shapes the output is spelled out here
# rather than left to a default, so that a generator upgrade that changes a
# default shows up as a diff in this file's review, not as a surprise in
# Models.g.cs.
#
# Why each non-obvious option:
#
#   GenerateClientClasses:false   DTOs only. The client is hand-written, because
#                                 the decisions in ZoikoTaxClient.cs (errors are
#                                 values, nothing retries, no tokens) are not
#                                 ones a generated client makes.
#   JsonLibrary:SystemTextJson    In-box. The SDK has no runtime dependencies.
#   DateTimeType:string           A Timestamp is RFC 3339 with exactly six
#                                 fractional digits (ADR-0011 §2.1 P2).
#                                 DateTimeOffset would re-encode it in another
#                                 form on the way back out, and two encodings of
#                                 one instant are two digests.
#   ExcludedTypeNames             The three list responses are inline schemas in
#                                 the contract, which NSwag names Response,
#                                 Response2 and Response3 by position. Those
#                                 names would change meaning if an operation were
#                                 added above them, so they are excluded and
#                                 declared by name in Lists.cs instead.
#   NewLineBehavior:LF            The same bytes on every platform, so the drift
#                                 check means the same thing everywhere.
set -eu

here=$(cd "$(dirname "$0")/.." && pwd)
out=${1:-"$here/src/ZoikoTax.Sdk/Generated/Models.g.cs"}

cd "$here"
dotnet tool restore >/dev/null
dotnet nswag openapi2csclient \
  /input:../../contracts/openapi/export/ztax.v1.3.1.yaml \
  /output:"$out" \
  /namespace:ZoikoTax.Sdk \
  /GenerateClientClasses:false \
  /GenerateClientInterfaces:false \
  /GenerateExceptionClasses:false \
  /GenerateDtoTypes:true \
  /ClassStyle:Poco \
  /JsonLibrary:SystemTextJson \
  /GenerateNullableReferenceTypes:true \
  /GenerateOptionalPropertiesAsNullable:true \
  /GenerateDataAnnotations:true \
  /DateTimeType:string \
  /ExcludedTypeNames:Response,Response2,Response3 \
  /NewLineBehavior:LF \
  >/dev/null
