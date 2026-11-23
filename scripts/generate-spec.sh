#!/usr/bin/env bash
# Regenerates spec/swagger.json from a DevLake git ref and normalizes it into
# spec/openapi.json. Usage: scripts/generate-spec.sh <devlake-ref>
#
# Requires: git, go (GOTOOLCHAIN=auto is used to fetch the toolchain DevLake
# needs). The swag version is read from DevLake's own Makefile so the spec is
# produced exactly as DevLake would produce it.
set -euo pipefail

REF="${1:-$(cat spec/DEVLAKE_VERSION)}"
REPO="${DEVLAKE_REPO:-https://github.com/apache/incubator-devlake.git}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

echo ">> cloning DevLake $REF"
git clone --quiet --depth 1 --branch "$REF" "$REPO" "$WORK/devlake"
BACKEND="$WORK/devlake/backend"

SWAG_VERSION="$(grep -oE 'swaggo/swag/cmd/swag@v[0-9.]+' "$BACKEND/Makefile" | head -1 | cut -d@ -f2)"
SWAG_VERSION="${SWAG_VERSION:-v1.16.6}"
DEVLAKE_GO="$(awk '/^toolchain /{print $2; exit} /^go /{g="go"$2} END{if(g!="")print g}' "$BACKEND/go.mod" | head -1)"
echo ">> using swag $SWAG_VERSION with $DEVLAKE_GO"

export GOTOOLCHAIN="$DEVLAKE_GO"
export GOBIN="$WORK/bin"
(cd "$BACKEND" && go install "github.com/swaggo/swag/cmd/swag@$SWAG_VERSION")
(cd "$BACKEND" && go mod download && "$GOBIN/swag" init --quiet --parseDependency --parseInternal \
	-o "$WORK/docs" -g ./server/api/api.go -d .)
unset GOTOOLCHAIN GOBIN

cp "$WORK/docs/swagger.json" "$ROOT/spec/swagger.json"
echo "$REF" > "$ROOT/spec/DEVLAKE_VERSION"

echo ">> normalizing to OpenAPI 3"
(cd "$ROOT/tools" && go run ./specprep \
	-in ../spec/swagger.json -out ../spec/openapi.json \
	-overrides ../spec/overrides.json -version "$REF")
echo ">> done: spec/openapi.json for DevLake $REF"
