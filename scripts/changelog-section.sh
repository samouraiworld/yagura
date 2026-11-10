#!/usr/bin/env bash
# Print the body of the "## [<version>]" section of CHANGELOG.md (no heading).
# Fails when the section is missing or holds only blank lines.
set -euo pipefail

if [[ $# -ne 1 ]]; then
  echo "usage: $0 <version>" >&2
  exit 2
fi

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
status=0
awk -v ver="$1" '
  index($0, "## [" ver "]") == 1 { found = 1; next }
  found && index($0, "## [") == 1 { exit }
  found { print; if (NF) body = 1 }
  END { if (!found) exit 1; if (!body) exit 3 }
' "$root/CHANGELOG.md" || status=$?
case $status in
  0) ;;
  3) echo "section [$1] in CHANGELOG.md is empty" >&2; exit 1 ;;
  *) echo "section [$1] not found in CHANGELOG.md" >&2; exit 1 ;;
esac
