run *args:
  uv run -- dantofa.cli {{args}}

test *args:
  uv run -- pytest {{args}}

# Build the sdist + wheel into dist/.
build:
  uv build

shellcheck:
  #!/usr/bin/env bash
  # Lint the shebang (script) recipes in this justfile with shellcheck. Line
  # recipes and any containing just-interpolations are not valid standalone
  # shell, so the second grep skips them (the char class avoids a literal
  # interpolation token here).
  for recipe in $(just --summary); do
    body="$(just --show "$recipe")"
    if printf '%s\n' "$body" | grep -Eq '^[[:space:]]*#!.*(bash|sh)' \
       && ! printf '%s\n' "$body" | grep -q '[{][{]'; then
      # Drop everything up to and including the (indented) shebang line; the
      # shell is given via -s, and indented bash is valid.
      printf '%s\n' "$body" | sed -n '/#!/,$p' | tail -n +2 | shellcheck -s bash -
    fi
  done

lint: shellcheck
  #!/usr/bin/env bash
  set -euo pipefail
  uv run -- ruff check .
  uv run -- basedpyright .
  uv run -- lint-imports
  actionlint
  yamllint .

format *args:
  uv run -- ruff format {{args}}

# All repository update operations live here. Currently: pin any newly-added
# GitHub Actions to a commit SHA, then refresh the pinned SHAs to the latest
# matching version (ratchet keeps them pinned). `pin` is idempotent on already
# -pinned refs, so it's safe to run every time before `update`.
update:
  #!/usr/bin/env bash
  set -euo pipefail
  find .github/workflows -name "*.yml" -print0 | xargs -0 -L 1 ratchet pin
  find .github/workflows -name "*.yml" -print0 | xargs -0 -L 1 ratchet update

sast:
  #!/usr/bin/env bash
  export SKYLOS_PRIVATE_DEPS_ALLOW=dantofa 
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
