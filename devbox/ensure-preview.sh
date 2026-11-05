#!/usr/bin/env bash
# Scaffold the caller of the platform's reusable preview workflow into the
# consuming project (.github/workflows/preview.yml). Run from the project root by
# the devbox plugin's init_hook, alongside the other ensure-*.sh scripts -- same
# contract: the file is meant to be COMMITTED, so create it only when absent and
# never edit one that exists.
#
# A preview creates a billed DOKS cluster per pull request, so the scaffold is OFF
# until the project opts in: the job runs only when the repository variable
# PR_PREVIEW_DOMAIN_SUFFIX is set (the domain previews are served under, which the
# project has to choose anyway). Until then the job shows as skipped.
set -euo pipefail

file=".github/workflows/preview.yml"

# Either extension counts: a project that already has .github/workflows/preview.yaml
# has its own preview, and a second workflow next to it would race it for the same
# cluster names.
if [ -e "$file" ] || [ -e "${file%.yml}.yaml" ]; then
  exit 0
fi

mkdir -p "$(dirname "$file")"
cat > "$file" <<'YAML'
---
name: preview
# One ephemeral DOKS cluster per pull request, via the dantofa platform's reusable
# workflow. It is OFF until you opt in: set the repository variable
# PR_PREVIEW_DOMAIN_SUFFIX (the domain previews are served under, e.g.
# preview.example.dev -> <repo>-pr42.preview.example.dev), and replace `deploy` and
# `verify` below with your project's commands. Needs the BWS_ACCESS_TOKEN secret and
# the BWS_PROJECT_ID / BWS_ORGANIZATION_ID variables. Every PR then creates (and on
# close deletes) a billed cluster.
on:
  pull_request:
    types: [opened, synchronize, reopened, closed]
permissions: {}
jobs:
  preview:
    if: vars.PR_PREVIEW_DOMAIN_SUFFIX != ''
    permissions:
      contents: read
      deployments: write
    # Tracks master; pin to a commit SHA if you want to review workflow changes.
    uses: dantofa/platform/.github/workflows/preview.yml@master
    with:
      cluster_prefix: ${{ github.event.repository.name }}-pr
      domain_suffix: ${{ vars.PR_PREVIEW_DOMAIN_SUFFIX }}
      bws_project_id: ${{ vars.BWS_PROJECT_ID }}
      bws_organization_id: ${{ vars.BWS_ORGANIZATION_ID }}
      deploy: just deploy
      verify: just verify
    secrets:
      bws_access_token: ${{ secrets.BWS_ACCESS_TOKEN }}
YAML
echo "dantofa-platform: created ${file} (commit it; it is disabled until PR_PREVIEW_DOMAIN_SUFFIX is set; replace its deploy/verify commands first)"
