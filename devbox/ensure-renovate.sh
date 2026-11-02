#!/usr/bin/env bash
# Scaffold a Renovate config in the consuming project that extends the platform's
# preset (.github/renovate/preset.json), so platform bumps arrive as PRs. Run from
# the project root by the devbox plugin's init_hook, alongside ensure-justfile.sh.
#
# Unlike the files under .just/, this one is meant to be COMMITTED -- Renovate reads
# it from the repo, not from a devbox shell. So the contract is stricter: create it
# only when the project has no Renovate config at all, and never edit one that
# exists (rewriting hand-written JSON5 is how a script destroys a project's
# settings). An existing config that lacks the preset gets a hint instead.
set -euo pipefail

preset="github>dantofa/platform//.github/renovate/preset"

# Every location Renovate reads its repo config from.
existing=""
for candidate in renovate.json renovate.json5 .github/renovate.json .github/renovate.json5 \
  .gitlab/renovate.json .gitlab/renovate.json5 .renovaterc .renovaterc.json .renovaterc.json5; do
  if [ -f "$candidate" ]; then
    existing="$candidate"
    break
  fi
done

if [ -z "$existing" ]; then
  cat > renovate.json5 <<JSON5
{
  \$schema: "https://docs.renovatebot.com/renovate-schema.json",
  // Platform bumps (dantofa/platform) arrive as PRs via this preset.
  extends: ["config:recommended", "${preset}"],
}
JSON5
  echo "dantofa-platform: created renovate.json5 extending the platform preset (commit it)"
  exit 0
fi

if ! command grep -qF "$preset" "$existing"; then
  echo "dantofa-platform: ${existing} does not extend the platform Renovate preset; add \"${preset}\" to its extends"
fi
