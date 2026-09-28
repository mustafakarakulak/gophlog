#!/bin/sh
# Prints the release notes for one version from a Keep a Changelog file: the
# body of its "## [X.Y.Z]" section, followed by the comparison link when the
# file defines one. Exits non-zero when the section is missing or empty.
#
# Usage: changelog-notes.sh <tag-or-version> [CHANGELOG.md]
set -eu

version="${1:?usage: changelog-notes.sh <tag-or-version> [CHANGELOG.md]}"
file="${2:-CHANGELOG.md}"
version="${version#v}"

# The section runs from its heading to the next "## [" heading or the first
# link reference definition ("[1.1.0]: https://..."), whichever comes first.
# Leading blank lines are dropped; $(...) drops the trailing ones.
body="$(awk -v heading="## [$version]" '
	index($0, heading) == 1 { found = 1; next }
	found && (/^## \[/ || /^\[[^ ]*\]: /) { exit }
	found { print }
' "$file" | sed '/./,$!d')"

if [ -z "$(printf '%s' "$body" | tr -d '[:space:]')" ]; then
	echo "error: $file has no \"## [$version]\" section, or it is empty" >&2
	exit 1
fi

printf '%s\n' "$body"

link="$(awk -v ref="[$version]: " 'index($0, ref) == 1 { print substr($0, length(ref) + 1); exit }' "$file")"
if [ -n "$link" ]; then
	printf '\n**Full changelog:** %s\n' "$link"
fi
