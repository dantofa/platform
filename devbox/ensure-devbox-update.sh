#!/usr/bin/env bash
# Scaffold the caller of the platform's reusable devbox-update workflow into the
# consuming project (.github/workflows/devbox-update.yml), so devbox-pinned
# downstreams get a weekly `devbox.lock` bump PR. Run from the project root by the
# devbox plugin's init_hook, alongside ensure-renovate.sh -- same contract: the file
# is meant to be COMMITTED, so create it only when absent and never edit one that
# exists (the project may have tuned the schedule or pinned the workflow ref).
set -euo pipefail

file=".github/workflows/devbox-update.yml"

if [ -e "$file" ]; then
  exit 0
fi

mkdir -p "$(dirname "$file")"
cat > "$file" <<'YAML'
---
name: devbox-update
# Weekly `devbox update` PR, via the dantofa platform's reusable workflow. It pushes
# and opens the PR with a PAT read from Bitwarden Secrets Manager (key
# PLATFORM_UPDATE_TOKEN), so this repo needs only its BWS_ACCESS_TOKEN secret, and
# that token's machine account must be able to read the project holding the PAT.
# Point PLATFORM_UPDATE_BWS_PROJECT_ID at that project if it is not BWS_PROJECT_ID.
on:
  schedule:
    - cron: "0 5 * * 1"
  workflow_dispatch:
permissions: {}
jobs:
  update:
    permissions:
      contents: read
    # Tracks master; pin to a commit SHA if you want to review workflow changes.
    uses: dantofa/platform/.github/workflows/devbox-update.yml@master
    with:
      bws_project_id: ${{ vars.PLATFORM_UPDATE_BWS_PROJECT_ID || vars.BWS_PROJECT_ID }}
    secrets:
      bws_access_token: ${{ secrets.BWS_ACCESS_TOKEN }}
YAML
echo "dantofa-platform: created ${file} (commit it; it needs BWS_ACCESS_TOKEN and a PLATFORM_UPDATE_TOKEN secret in the bws project)"
