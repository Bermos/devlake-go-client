#!/usr/bin/env bash
# Prints the newest DevLake release tag, stable or pre-release, by tag date.
#
# DevLake keeps shipping fixes in pre-releases (e.g. v1.0.3-beta19 after
# v1.0.3), so semver ordering is not meaningful; the newest tag wins.
# Only vX.Y.Z and vX.Y.Z-(alpha|beta|rc)N tags are considered.
set -euo pipefail

REPO="${DEVLAKE_REPO:-https://github.com/apache/incubator-devlake.git}"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

git clone --quiet --bare --filter=tree:0 "$REPO" "$WORK/devlake.git"
git -C "$WORK/devlake.git" for-each-ref --sort=-creatordate --format='%(refname:short)' refs/tags |
	grep -E '^v[0-9]+\.[0-9]+\.[0-9]+(-(alpha|beta|rc)[0-9]+)?$' |
	head -1
