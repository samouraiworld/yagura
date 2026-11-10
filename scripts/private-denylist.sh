#!/usr/bin/env bash
# Optional extra gitleaks pass using a private rule file kept outside the repo.
# No-op unless YAGURA_PRIVATE_DENYLIST points to an existing file.
set -euo pipefail

denylist="${YAGURA_PRIVATE_DENYLIST:-}"
if [[ -z "$denylist" || ! -f "$denylist" ]]; then
  exit 0
fi

exec gitleaks git --pre-commit --staged --no-banner --config "$denylist"
