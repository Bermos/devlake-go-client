#!/usr/bin/env bash
# Writes a Markdown summary of API changes between the committed
# spec/openapi.json (git HEAD) and the working tree version to stdout.
# Output is capped so it fits in a pull request body.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OLD="$(mktemp)"
trap 'rm -f "$OLD"' EXIT
git -C "$ROOT" show HEAD:spec/openapi.json > "$OLD"

cd "$ROOT/tools"
# Both commands exit non-zero only on real failures (no --fail-on), so set -e
# surfaces a broken tool instead of reporting a misleading result.
BREAKING_JSON="$(go tool oasdiff breaking -f json "$OLD" ../spec/openapi.json)"
BREAKING_COUNT="$(jq '[.[]? | select(.level == 3)] | length' <<<"${BREAKING_JSON:-[]}")"
LOG="$(go tool oasdiff changelog -f markdown "$OLD" ../spec/openapi.json)"
BREAKING="no"
[ "$BREAKING_COUNT" -gt 0 ] && BREAKING="yes ($BREAKING_COUNT)"

echo "Breaking API changes: **$BREAKING**"
echo
if [ -z "$LOG" ] || grep -q "No changes detected" <<<"$LOG"; then
	echo "No API changes; only the DevLake version changed."
else
	printf '%s\n' "$LOG" | head -c 50000
	[ "${#LOG}" -gt 50000 ] && printf '\n\n_Changelog truncated._\n'
fi
