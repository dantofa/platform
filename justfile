# Reusable cluster-ops recipes (`just cluster debug|verify|local …`), shared with
# downstream projects via the flake (packages.cluster-just). Dogfooded here; the
# platform overrides the module's $DCTL default to `go run ./cmd/dctl` (flake
# devShell) so in-repo dev runs source, not the published artifact.
import 'cluster.just'

run *args:
  go run ./cmd/dctl {{args}}

test *args:
  go test ./... {{args}}

# Build the dctl binary into dist/, stamping the source-derived version the same
# way the flake does (<YYYY.MM.DD>+g<rev>, with -dirty on an unclean tree) so a
# local build reports it too.
build:
  #!/usr/bin/env bash
  set -euo pipefail
  version="$(git show -s --format=%cd --date=format:'%Y.%m.%d' HEAD)+g$(git rev-parse --short HEAD)"
  if [ -n "$(git status --porcelain)" ]; then version="$version-dirty"; fi
  # CGO_ENABLED=0 matches the flake package: a static binary with no libc link,
  # so dist/dctl behaves identically to the shipped artifact. Go's build cache
  # makes this incremental (unlike the hermetic `nix build`).
  CGO_ENABLED=0 go build -ldflags "-s -w -X github.com/dantofa/platform/internal/version.Version=$version" -o dist/dctl ./cmd/dctl

# Publish the flux/ GitOps tree as an OCI artifact to a registry (CI publishes it
# to ghcr.io on merge to master; `dctl {do} cluster bootstrap` pulls it by
# default). Mirrors `dctl local cluster push` but targets an external registry,
# whitelisting flux/ so the artifact's paths match the cluster flow. url carries
# the tag (oci://host/repo:tag); revision annotates the source commit. Pass
# registry creds via OCI_CREDS=user:token; without it flux uses the ambient
# keychain.
publish url revision:
  flux push artifact "{{url}}" --path . \
    --source "https://github.com/dantofa/platform" --revision "{{revision}}" \
    --ignore-paths "/*,!/flux/" ${OCI_CREDS:+--creds $OCI_CREDS}

# Regenerate the Cloudflare IPv4 allowlist in the Traefik LoadBalancer manifest
# from cloudflare.com/ips-v4 (the source of truth for who may reach the origin
# directly). Replaces the lines between the BEGIN/END cloudflare-ipv4 markers. Run
# by the cloudflare-acl workflow (which opens a PR on change); also runnable by
# hand. Idempotent: a no-op when the published list is unchanged.
cloudflare-acl:
  #!/usr/bin/env bash
  set -euo pipefail
  file=flux/ingress/traefik/release.yaml
  block="$(curl -fsS https://www.cloudflare.com/ips-v4 | sort -V | sed 's/^/          - /')"
  awk -v block="$block" '
    /# BEGIN cloudflare-ipv4/ { print; print block; skip=1; next }
    /# END cloudflare-ipv4/   { skip=0 }
    !skip
  ' "$file" > "$file.tmp"
  mv "$file.tmp" "$file"

shellcheck:
  #!/usr/bin/env bash
  # Lint the shebang (script) recipes in this justfile with shellcheck. Line
  # recipes and any containing just-interpolations are not valid standalone
  # shell, so the second grep skips them (the char class avoids a literal
  # interpolation token here). Every recipe is checked before failing, so one
  # run reports all findings; any failure fails the recipe.
  set -euo pipefail
  failed=()
  for recipe in $(just --summary); do
    body="$(just --show "$recipe")"
    if printf '%s\n' "$body" | grep -Eq '^[[:space:]]*#!.*(bash|sh)' \
       && ! printf '%s\n' "$body" | grep -q '[{][{]'; then
      # Drop everything up to and including the (indented) shebang line; the
      # shell is given via -s, and indented bash is valid.
      if ! printf '%s\n' "$body" | sed -n '/#!/,$p' | tail -n +2 | shellcheck -s bash -; then
        failed+=("$recipe")
      fi
    fi
  done
  # Also lint standalone shell scripts shipped in the repo (not justfile recipes),
  # e.g. the devbox plugin smoke test.
  if ! shellcheck devbox/*.sh; then
    failed+=("devbox/*.sh")
  fi
  if [ "${#failed[@]}" -gt 0 ]; then
    echo "shellcheck failed: ${failed[*]}" >&2
    exit 1
  fi

lint: shellcheck
  #!/usr/bin/env bash
  set -euo pipefail
  # gofmt-strict (gofumpt): fail if any file needs reformatting.
  unformatted="$(gofumpt -l .)"
  if [ -n "$unformatted" ]; then
    echo "gofumpt: these files are not formatted:"; echo "$unformatted"; exit 1
  fi
  go vet ./...
  golangci-lint run
  # Whole-program dead-code (quality, not security): fail on any unreachable func.
  dead="$(go tool deadcode ./...)"
  if [ -n "$dead" ]; then echo "deadcode: unreachable functions:"; echo "$dead"; exit 1; fi
  actionlint
  yamllint .

format *args=".":
  gofumpt -w {{args}}

# Manual refresh of the pins Renovate does not own: Go modules and the Nix flake
# inputs (the flake tracks nixos-unstable, a rolling branch with no versions to
# PR, so it stays manual). GitHub Actions, Go modules, and the Flux manifest
# chart/image versions also get automated PRs from Renovate (the hosted Mend
# app; see .github/renovate.json5). NB: bumping Go deps changes go.sum, so the
# flake's `vendorHash` must be recomputed (set it to lib.fakeHash, `nix build`,
# copy the reported hash) — that applies to Renovate's gomod PRs too. Run this
# deliberately — freshness is a manual operation, never a merge gate.
update: && vendor-hash
  #!/usr/bin/env bash
  set -euo pipefail
  go get -u ./...
  go mod tidy
  nix flake update

# Recompute the flake vendorHash for the current go.sum: blank it to fakeHash so
# `nix build` reports the real hash, write that back, and confirm the package
# builds. Run by `just update`; also run standalone on a Renovate gomod PR, which
# changes go.sum but cannot recompute the hash itself.
vendor-hash:
  #!/usr/bin/env bash
  set -euo pipefail
  fake="sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
  sed -i "s|vendorHash = \"sha256-[^\"]*\";|vendorHash = \"$fake\";|" flake.nix
  got="$(nix build .#default 2>&1 | sed -n 's|.*got:[[:space:]]*\(sha256-[A-Za-z0-9+/=]*\).*|\1|p' | head -1 || true)"
  if [ -z "$got" ]; then
    echo "error: could not determine vendorHash from nix build output" >&2
    exit 1
  fi
  sed -i "s|vendorHash = \"sha256-[^\"]*\";|vendorHash = \"$got\";|" flake.nix
  nix build ".#default" >/dev/null

sast:
  #!/usr/bin/env bash
  set -uo pipefail
  out="$(govulncheck ./... 2>&1)"; status=$?
  echo "$out"
  [ "$status" -eq 0 ] && exit 0
  # govulncheck exits non-zero when a vulnerability is actually called. A
  # standard-library-only finding is fixed by bumping the (nix-pinned) Go
  # toolchain via `just update`, not by our code — and freshness is never a merge
  # gate here — so it warns rather than fails. Any finding in our modules/deps
  # still fails the gate.
  affected="$(echo "$out" | grep 'Your code is affected by')"
  [ -z "$affected" ] && exit 0
  if echo "$affected" | grep -qv 'Go standard library'; then exit 1; fi
  echo "::warning::govulncheck: only standard-library vulnerabilities affect this code; bump the Go toolchain via 'just update'."

# Full local (kind) e2e for the PLATFORM itself -- the platform's own compose recipe
# over the shared `cluster local` primitives, adding the platform self-test: echo
# reachable at /echo through the local Traefik ingress, with no Cloudflare/tunnel in
# the path, so traffic stays on the loopback (no WAN round-trip / 520 flakiness).
# There is no port-forward and no --resolve: the cluster publishes its ingress on the
# host's real :80/:443 (kind extraPortMappings -> Traefik NodePort) and
# ${base_domain} resolves to 127.0.0.1 via a wildcard DNS record, so the probe is a
# plain curl of the real URL -- exactly what any other tool would do.
# Requires the dev shell (kind/flux/kubectl/curl/jq/bws on PATH) and BWS_* in the
# environment (for the ESO secret-zero). Uses bash-local vars (not just-interpolation)
# so `just shellcheck` still lints it. A failed run leaves the cluster up for
# debugging; run `just cluster local delete`. It also asserts the run leaves the user's
# global kubeconfig exactly as it found it (absent counts): local credentials belong
# in the project's .kubeconfig only.
local-e2e:
  #!/usr/bin/env bash
  set -euo pipefail
  base_domain=local.dantofa.dev
  global_kubeconfig="$HOME/.kube/config"
  snapshot() { sha256sum "$global_kubeconfig" 2>/dev/null || echo absent; }
  global_before="$(snapshot)"
  just cluster local create
  if [ ! -s .kubeconfig ]; then
    echo "e2e FAILED: create did not write .kubeconfig" >&2
    exit 1
  fi
  if [ "$(snapshot)" != "$global_before" ]; then
    echo "e2e FAILED: create modified ${global_kubeconfig}" >&2
    exit 1
  fi
  just cluster local verify
  export KUBECONFIG=.kubeconfig
  echo "Waiting for Traefik to be ready..."
  kubectl -n traefik rollout status deploy/traefik --timeout=180s
  url="http://${base_domain}/echo"
  echo "Probing ${url} via the local ingress on the host's real port..."
  retries=12
  sleep=5
  for i in $(seq 1 "$retries"); do
    if body="$(curl -fsS --max-time 8 "$url")" \
      && printf '%s' "$body" | grep -q "$base_domain"; then
      echo "e2e OK: echo reachable via ${url}"
      break
    fi
    if [ "$i" -eq "$retries" ]; then
      echo "e2e FAILED: ${url} did not serve echo within $((retries * sleep))s" >&2
      exit 1
    fi
    echo "attempt ${i}/${retries}: not ready, retrying in ${sleep}s..."
    sleep "$sleep"
  done
  TF_E2E_KEEP=true just cluster verify terraform
  out="$(just cluster local delete)"
  printf '%s\n' "$out"
  if ! jq -e '.teardown.terraform_deleted >= 1' <<<"$out" >/dev/null; then
    echo "e2e FAILED: teardown did not drain the Terraform object" >&2
    exit 1
  fi
  if [ -e .kubeconfig ]; then
    echo "e2e FAILED: delete left .kubeconfig behind" >&2
    exit 1
  fi
  if [ "$(snapshot)" != "$global_before" ]; then
    echo "e2e FAILED: the run modified ${global_kubeconfig}" >&2
    exit 1
  fi

# Print how many non-deleted Cloudflare Tunnels are named <name> (via the bws
# project's CLOUDFLARE_API_TOKEN / CLOUDFLARE_ACCOUNT_ID). Internal helper for the
# local-e2e tunnel assertions.
_cf-tunnel-count name:
  #!/usr/bin/env bash
  set -euo pipefail
  # Capture the CF creds out of bws first (a nested `bash -c '...'` through
  # `bws run` mangles the quoting). The token goes to curl via stdin (-H @-),
  # never argv.
  token="$(bws run --project-id "$BWS_PROJECT_ID" -- printenv CLOUDFLARE_API_TOKEN)"
  account="$(bws run --project-id "$BWS_PROJECT_ID" -- printenv CLOUDFLARE_ACCOUNT_ID)"
  printf 'Authorization: Bearer %s\n' "$token" \
    | curl -fsS -H @- \
      "https://api.cloudflare.com/client/v4/accounts/$account/cfd_tunnel?name={{name}}&is_deleted=false" \
    | jq '.result | length'

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
