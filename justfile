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

# Integration check (CI): assert Flux installed Velero and a backup completes.
# Targets the cluster in $KUBECONFIG; run after bootstrapping the backup stack
# (local: `local cluster bootstrap` + `push`; preview: `do cluster bootstrap`).
verify-backup:
  #!/usr/bin/env bash
  set -euo pipefail
  ns=velero
  echo "Waiting for Flux to install the Velero HelmRelease..."
  for _ in $(seq 1 90); do
    if kubectl -n "$ns" get helmrelease velero >/dev/null 2>&1; then break; fi
    sleep 10
  done
  kubectl -n "$ns" wait --for=condition=Ready --timeout=600s helmrelease/velero
  echo "Waiting for the BackupStorageLocation to become Available..."
  kubectl -n "$ns" wait --for=jsonpath='{.status.phase}'=Available \
    backupstoragelocation/default --timeout=300s
  echo "Issuing a test backup..."
  kubectl -n default create configmap velero-ci-probe \
    --from-literal=ok=1 --dry-run=client -o yaml | kubectl apply -f -
  backup="ci-verify-$(date +%s)"
  velero backup create "$backup" --namespace "$ns" --include-namespaces default --wait || true
  phase="$(kubectl -n "$ns" get backup "$backup" -o jsonpath='{.status.phase}')"
  echo "Backup $backup phase: $phase"
  if [ "$phase" != "Completed" ]; then
    velero backup describe "$backup" --namespace "$ns" --details || true
    velero backup logs "$backup" --namespace "$ns" || true
    exit 1
  fi

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
