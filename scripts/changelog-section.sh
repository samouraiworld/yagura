#!/usr/bin/env bash
# Print the body of the "## [<version>]" section of CHANGELOG.md (no heading).
set -euo pipefail

if [[ $# -ne 1 ]]; then
  echo "usage: $0 <version>" >&2
  exit 2
fi

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
awk -v ver="$1" '
  index($0, "## [" ver "]") == 1 { found = 1; next }
  found && index($0, "## [") == 1 { exit }
  found { print }
  END { if (!found) exit 1 }
' "$root/CHANGELOG.md" || { echo "section [$1] not found in CHANGELOG.md" >&2; exit 1; }
