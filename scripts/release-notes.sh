#!/usr/bin/env bash
#
# Extract the CHANGELOG section for a release, and fail if it is not there.
#
# The release workflow runs this before GoReleaser and feeds the result to
# `--release-notes`, so a tag without a written entry stops the release instead
# of publishing a list of commit subjects nobody can act on.
#
# Usage:
#   scripts/release-notes.sh v1.4.1            # writes to stdout
#   scripts/release-notes.sh v1.4.1 notes.md   # writes to a file

set -euo pipefail

if [[ $# -lt 1 ]]; then
	echo "usage: $0 <tag> [output-file]" >&2
	exit 2
fi

tag="$1"
out="${2:-}"
version="${tag#v}"
changelog="$(dirname "$0")/../CHANGELOG.md"

if [[ ! -f "$changelog" ]]; then
	echo "release-notes: $changelog not found" >&2
	exit 1
fi

# Take everything under `## [x.y.z]` up to the next `## [` heading. Link
# reference definitions at the foot of the file start at column 0 with `[`, so
# they are dropped too.
notes="$(
	awk -v want="## [$version]" '
		index($0, want) == 1 { collecting = 1; next }
		collecting && /^## \[/ { exit }
		collecting && /^\[[^]]+\]: / { next }
		collecting { print }
	' "$changelog"
)"

# Trim leading and trailing blank lines.
notes="$(printf '%s\n' "$notes" | sed -e '/./,$!d' | sed -e :a -e '/^\n*$/{$d;N;};/\n$/ba')"

if [[ -z "${notes//[$'\n\t ']/}" ]]; then
	cat >&2 <<EOF
release-notes: no CHANGELOG entry for $version.

Every release needs one. Add a section to CHANGELOG.md before tagging:

  ## [$version] - $(date +%Y-%m-%d)

  ### Fixed

  - What changed, and why it mattered.

and a link reference at the foot of the file:

  [$version]: https://github.com/openobserve/terraform-provider-openobserve/releases/tag/$tag
EOF
	exit 1
fi

if [[ -n "$out" ]]; then
	printf '%s\n' "$notes" >"$out"
	echo "release-notes: wrote $(printf '%s\n' "$notes" | wc -l | tr -d ' ') lines for $version to $out" >&2
else
	printf '%s\n' "$notes"
fi
