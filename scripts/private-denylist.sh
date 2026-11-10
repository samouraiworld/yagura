#!/usr/bin/env bash
# Optional extra gitleaks pass using a private rule file kept outside the repo.
# No-op when YAGURA_PRIVATE_DENYLIST is unset or empty. Once it is set, the
# check fails closed: a missing or unreadable file stops the commit.
set -euo pipefail

denylist="${YAGURA_PRIVATE_DENYLIST:-}"
if [[ -z "$denylist" ]]; then
  exit 0
fi
if [[ ! -f "$denylist" || ! -r "$denylist" ]]; then
  echo "private-denylist: YAGURA_PRIVATE_DENYLIST is set but $denylist is not a readable file" >&2
  exit 1
fi

exec gitleaks git --pre-commit --staged --no-banner --config "$denylist"
