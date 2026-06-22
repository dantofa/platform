run *args:
  uv run -- dantofa.cli {{args}}

test *args:
  uv run -- pytest {{args}}

lint:
  #!/usr/bin/env bash
  set -euo pipefail
  uv run -- ruff check .
  uv run -- ty check
  actionlint
  yamllint .

format *args:
  uv run -- ruff format {{args}}

# All repository update operations live here. Currently: refresh the pinned
# GitHub Actions SHAs to the latest matching version (ratchet keeps them pinned).
update:
  find .github/workflows -name "*.yml" | xargs -L 1 ratchet update

sast:
  uv run -- skylos --format concise --danger --secrets --sca src

github action:
  just github-{{action}}

github-repo:
  #!/usr/bin/env bash
  set -euo pipefail
  config_dir=".github/repo-config"
  repo="${GITHUB_REPOSITORY:-$(gh repo view --json nameWithOwner --jq .nameWithOwner)}"
  echo "Applying repository configuration to $repo"
  echo "==> repository settings"
  gh api -X PATCH "repos/$repo" --input "$config_dir/repo-settings.json" >/dev/null
  echo "==> master branch ruleset"
  ruleset_id="$(gh api "repos/$repo/rulesets" --jq '.[] | select(.name == "master") | .id')"
  if [ -n "$ruleset_id" ]; then
    echo "    updating existing ruleset (id $ruleset_id)"
    gh api -X PUT "repos/$repo/rulesets/$ruleset_id" --input "$config_dir/ruleset-master.json" >/dev/null
  else
    echo "    creating ruleset"
    gh api -X POST "repos/$repo/rulesets" --input "$config_dir/ruleset-master.json" >/dev/null
  fi
  echo "Done."
