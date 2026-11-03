#!/usr/bin/env bash
# Assertions for the devbox-plugin smoke test (see .github/workflows/devbox.yml).
# Runs inside the synthetic consumer's devbox environment, CWD = the harness dir,
# after `devbox install` has applied the plugin (packages + init_hook + env).
set -euo pipefail

echo "--- toolchain on PATH (from the plugin's packages) ---"
for t in dctl kubectl velero flux jq just; do
  command -v "$t" >/dev/null || { echo "MISSING: $t"; exit 1; }
  echo "  ok $t"
done

echo "--- files materialized into ./.just (init_hook) ---"
test -f .just/cluster.just || { echo "cluster.just not materialized"; exit 1; }
test -f .just/.trivyignore-base || { echo ".trivyignore-base not materialized"; exit 1; }

echo "--- skill materialized for Claude Code (init_hook) ---"
test -f .claude/skills/dantofa-platform/SKILL.md || { echo "SKILL.md not materialized"; exit 1; }

echo "--- generated + local paths ignored, idempotently (init_hook) ---"
for p in '.kubeconfig' '.local.pem' '.just/' '.devbox/' '.claude/skills/dantofa-platform/'; do
  grep -qxF "$p" .gitignore || { echo ".gitignore missing entry: $p"; exit 1; }
  test "$(grep -cxF "$p" .gitignore)" -eq 1 || { echo ".gitignore duplicated entry: $p"; exit 1; }
  echo "  ok $p"
done

echo "--- env set + base ignore resolves to a real, non-empty file ---"
echo "DCTL=${DCTL:?DCTL not set by plugin}  TRIVYIGNORE_BASE=${TRIVYIGNORE_BASE:?TRIVYIGNORE_BASE not set}"
test -s "$TRIVYIGNORE_BASE"

echo "--- consumer justfile gained the cluster.just import (init_hook) ---"
# The harness seeded a justfile with its own recipe and no trailing newline, so
# this covers the append path AND that appending did not splice onto that line.
test -f justfile || { echo "justfile missing"; exit 1; }
test "$(grep -cF ".just/cluster.just" justfile)" -eq 1 \
  || { echo "import missing or duplicated in justfile"; exit 1; }
just --summary | tr ' ' '\n' | grep -qx 'app-build' \
  || { echo "the consumer's own recipe did not survive"; exit 1; }
echo "  ok import present once, app-build intact"

echo "--- ensure-justfile creates a justfile when the project has none ---"
probe="$(mktemp -d)"
( cd "$probe" && bash "$DEVBOX_PACKAGES_DIR/ensure-justfile.sh" >/dev/null )
test -f "$probe/justfile" || { echo "no justfile created"; exit 1; }
grep -qF ".just/cluster.just" "$probe/justfile" || { echo "created justfile lacks the import"; exit 1; }
# Idempotent: a second run must not add a second import.
( cd "$probe" && bash "$DEVBOX_PACKAGES_DIR/ensure-justfile.sh" >/dev/null )
test "$(grep -cF ".just/cluster.just" "$probe/justfile")" -eq 1 \
  || { echo "import duplicated on re-run"; exit 1; }
rm -rf "$probe"
echo "  ok created once, idempotent"

echo "--- consumer gained a Renovate config extending the platform preset (init_hook) ---"
test -f renovate.json5 || { echo "renovate.json5 not scaffolded"; exit 1; }
grep -qF "dantofa/platform//.github/renovate/preset" renovate.json5 \
  || { echo "scaffolded renovate.json5 lacks the preset"; exit 1; }
echo "  ok scaffolded"

echo "--- ensure-renovate creates once, never edits an existing config ---"
probe="$(mktemp -d)"
( cd "$probe" && bash "$DEVBOX_PACKAGES_DIR/ensure-renovate.sh" >/dev/null )
test -f "$probe/renovate.json5" || { echo "no renovate.json5 created"; exit 1; }
rm -rf "$probe"
probe="$(mktemp -d)"
mkdir -p "$probe/.github"
printf '{ "extends": ["config:recommended"] }\n' > "$probe/.github/renovate.json"
before="$(cat "$probe/.github/renovate.json")"
( cd "$probe" && bash "$DEVBOX_PACKAGES_DIR/ensure-renovate.sh" | grep -q 'does not extend' ) \
  || { echo "no hint for an existing config lacking the preset"; exit 1; }
test ! -e "$probe/renovate.json5" || { echo "created a second config next to an existing one"; exit 1; }
test "$(cat "$probe/.github/renovate.json")" = "$before" || { echo "existing config was modified"; exit 1; }
rm -rf "$probe"
echo "  ok created once, existing config untouched"

echo "--- consumer gained the devbox-update caller workflow (init_hook) ---"
test -f .github/workflows/devbox-update.yml || { echo "devbox-update.yml not scaffolded"; exit 1; }
grep -qF "dantofa/platform/.github/workflows/devbox-update.yml" .github/workflows/devbox-update.yml \
  || { echo "scaffolded workflow does not call the reusable workflow"; exit 1; }
echo "  ok scaffolded"

echo "--- ensure-devbox-update creates once, never edits an existing file ---"
probe="$(mktemp -d)"
( cd "$probe" && bash "$DEVBOX_PACKAGES_DIR/ensure-devbox-update.sh" >/dev/null )
test -f "$probe/.github/workflows/devbox-update.yml" || { echo "no workflow created"; exit 1; }
rm -rf "$probe"
probe="$(mktemp -d)"
mkdir -p "$probe/.github/workflows"
printf 'name: mine\n' > "$probe/.github/workflows/devbox-update.yml"
( cd "$probe" && bash "$DEVBOX_PACKAGES_DIR/ensure-devbox-update.sh" >/dev/null )
test "$(cat "$probe/.github/workflows/devbox-update.yml")" = "name: mine" \
  || { echo "existing workflow was modified"; exit 1; }
rm -rf "$probe"
echo "  ok created once, existing file untouched"

echo "--- just cluster … dispatch routes to the flat recipes ---"
just --dry-run cluster verify backup 2>&1 | grep -q 'cluster-verify'
just --dry-run cluster verify image-scan 2>&1 | grep -q 'cluster-verify'
just --dry-run cluster debug 2>&1 | grep -q 'cluster-debug'

echo "DEVBOX PLUGIN SMOKE OK"
