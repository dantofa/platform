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

local action *args:
  just local-{{action}} {{args}}

# End-to-end suite for a real local (kind) cluster + the Cloudflare tunnel
# ingress. Unlike the `local` CI workflow (which uses in-cluster DNS), this
# exercises the live tunnel path so it can verify the teardown reaps the
# Cloudflare tunnel object. Requires the dev shell (kind/flux/kubectl/curl/jq/bws
# on PATH) and BWS_ACCESS_TOKEN / BWS_PROJECT_ID / BWS_ORGANIZATION_ID in the
# environment (the ESO secret-zero + the Cloudflare API assertions). Recipes use
# bash-local base_domain/cluster (not just-interpolation) so `just shellcheck`
# still lints them. `local-test` runs create -> verify, and only tears down on
# success (a failed verify leaves the cluster up for debugging; run
# `just local-delete` to clean up).

# Create the kind cluster and bootstrap the platform (Flux + ingress/tunnel +
# echo, which deploys by default on local). base_domain is a real Cloudflare zone
# so the tunnel publishes a resolvable record. bootstrap reads BWS_* from the env
# for the ESO secret-zero; local needs no DO/Spaces creds, so no `bws run` here.
local-create *args:
  #!/usr/bin/env bash
  set -euo pipefail
  : "${BWS_ACCESS_TOKEN:?set BWS_ACCESS_TOKEN (dev shell + bws login)}"
  : "${BWS_PROJECT_ID:?set BWS_PROJECT_ID}"
  : "${BWS_ORGANIZATION_ID:?set BWS_ORGANIZATION_ID}"
  base_domain=local.dantofa.dev
  go run ./cmd/dctl local cluster create --control-planes 1 --workers 2
  go run ./cmd/dctl local cluster bootstrap --base-domain "$base_domain" {{args}}

# Verify the running cluster: nodes ready, the whole GitOps tree reconciled, the
# Velero backup works, echo is reachable end-to-end through the Cloudflare tunnel,
# and the tunnel object exists (the pre-delete precondition for the teardown
# test).
local-verify:
  #!/usr/bin/env bash
  set -euo pipefail
  export KUBECONFIG=.kubeconfig
  base_domain=local.dantofa.dev
  cluster=local
  go run ./cmd/dctl local cluster connect
  kubectl wait --for=condition=Ready nodes --all --timeout=180s
  # Gate on the whole GitOps tree reconciling (generous timeout for cold pulls).
  go run ./cmd/dctl flux kustomization verify --wait --timeout 600s --kubeconfig .kubeconfig
  # Existing backup e2e.
  just verify-backup
  # End-to-end ingress: echo served at the apex through the tunnel + Cloudflare.
  retries=24
  sleep=10
  url="https://$base_domain"
  echo "Probing ${url} through the Cloudflare tunnel..."
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
  # Precondition for the teardown test: the tunnel object exists in Cloudflare.
  n="$(just _cf-tunnel-count "$cluster")"
  echo "cloudflare tunnels named $cluster: $n"
  test "$n" -ge 1

# Delete the cluster via the graceful teardown (drains DNS records, stops the
# tunnel controller, reaps the tunnel), then assert the tunnel object is actually
# gone from Cloudflare -- the leak this change fixes.
local-delete *args:
  #!/usr/bin/env bash
  set -euo pipefail
  cluster=local
  go run ./cmd/dctl local cluster delete {{args}}
  n="$(just _cf-tunnel-count "$cluster")"
  echo "cloudflare tunnels named $cluster after teardown: $n"
  test "$n" -eq 0

# Print how many non-deleted Cloudflare Tunnels are named <name> (via the bws
# project's CLOUDFLARE_API_TOKEN / CLOUDFLARE_ACCOUNT_ID). Internal helper for the
# local-verify / local-delete tunnel assertions.
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

local-test: local-create local-verify && local-delete

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
  # Fully-qualified resource.group: CloudNativePG also defines a `Backup` CRD
  # (backups.postgresql.cnpg.io), so the bare `backup` short name is ambiguous and
  # kubectl would resolve it to the wrong group.
  phase="$(kubectl -n "$ns" get backups.velero.io "$backup" -o jsonpath='{.status.phase}')"
  echo "Backup $backup phase: $phase"
  if [ "$phase" != "Completed" ]; then
    velero backup describe "$backup" --namespace "$ns" --details || true
    # `velero backup logs` streams from object storage, which a CI runner can't
    # reach; the velero server pod logs carry the same backup errors and are
    # always reachable, so dump them for the actual failure reason.
    kubectl -n "$ns" logs deploy/velero --tail=200 || true
    velero backup logs "$backup" --namespace "$ns" || true
    # Capacity signals: node pressure, pod restarts/placement, last-terminated
    # reasons (OOMKilled), and recent Warning events (evictions, scheduling).
    echo "--- nodes ---"; kubectl get nodes -o wide || true
    echo "--- $ns pods ---"; kubectl -n "$ns" get pods -o wide || true
    echo "--- last terminated reasons ($ns) ---"
    kubectl -n "$ns" get pods -o jsonpath='{range .items[*]}{.metadata.name}{" -> "}{.status.containerStatuses[*].lastState.terminated.reason}{"\n"}{end}' || true
    echo "--- recent Warning events ---"
    kubectl get events -A --field-selector type=Warning --sort-by=.lastTimestamp | tail -30 || true
    exit 1
  fi

# Backup + restore drill: prove a backup can actually be RESTORED, not just taken
# (an untested backup is not a backup). Seeds a throwaway namespace, backs it up,
# deletes it to simulate loss, restores from the backup, and asserts the seeded
# marker returns. Run after verify-backup (which waits for Velero + an Available
# BackupStorageLocation). Fully-qualified resource.group on `backups`/`restores`
# because CloudNativePG also defines a `Backup` CRD (the short name is ambiguous).
verify-restore:
  #!/usr/bin/env bash
  set -euo pipefail
  ns=velero
  drill=velero-restore-drill
  kubectl -n "$ns" wait --for=jsonpath='{.status.phase}'=Available \
    backupstoragelocation/default --timeout=300s
  # Seed a throwaway namespace with a marker to back up and later assert on.
  kubectl create namespace "$drill" --dry-run=client -o yaml | kubectl apply -f -
  canary="restored-$(date +%s)"
  kubectl -n "$drill" create configmap restore-marker \
    --from-literal=canary="$canary" --dry-run=client -o yaml | kubectl apply -f -
  # Back up the drill namespace.
  backup="restore-drill-$(date +%s)"
  echo "Backing up namespace $drill as $backup..."
  velero backup create "$backup" --namespace "$ns" --include-namespaces "$drill" --wait || true
  bphase="$(kubectl -n "$ns" get backups.velero.io "$backup" -o jsonpath='{.status.phase}')"
  echo "Backup $backup phase: $bphase"
  if [ "$bphase" != "Completed" ]; then
    velero backup describe "$backup" --namespace "$ns" --details || true
    kubectl -n "$ns" logs deploy/velero --tail=200 || true
    kubectl delete namespace "$drill" --ignore-not-found >/dev/null 2>&1 || true
    exit 1
  fi
  # Simulate loss, then restore from the backup.
  echo "Deleting namespace $drill to simulate loss..."
  kubectl delete namespace "$drill" --wait --timeout=180s
  restore="restore-drill-$(date +%s)"
  echo "Restoring from $backup as $restore..."
  velero restore create "$restore" --namespace "$ns" --from-backup "$backup" --wait || true
  rphase="$(kubectl -n "$ns" get restores.velero.io "$restore" -o jsonpath='{.status.phase}')"
  echo "Restore $restore phase: $rphase"
  if [ "$rphase" != "Completed" ]; then
    velero restore describe "$restore" --namespace "$ns" --details || true
    kubectl -n "$ns" logs deploy/velero --tail=200 || true
    kubectl delete namespace "$drill" --ignore-not-found >/dev/null 2>&1 || true
    exit 1
  fi
  # Assert the seeded resource came back with its value.
  got="$(kubectl -n "$drill" get configmap restore-marker -o jsonpath='{.data.canary}' 2>/dev/null || true)"
  kubectl delete namespace "$drill" --ignore-not-found >/dev/null 2>&1 || true
  if [ "$got" != "$canary" ]; then
    echo "restore drill FAILED: marker not restored (got '$got', want '$canary')"; exit 1
  fi
  echo "restore drill OK: $drill restored from $backup with marker intact"

# Snapshot cluster + Flux state into ./preview-diagnostics for CI to upload as
# artifacts on a failed preview run (the cluster is destroyed afterwards, so
# this is the only surviving record). Everything is best-effort: a partial
# capture from a half-provisioned or unreachable cluster is still worth keeping,
# so this recipe never fails the run. Reads KUBECONFIG (falls back to
# ./.kubeconfig).
capture-diagnostics:
  #!/usr/bin/env bash
  set -uo pipefail
  out=preview-diagnostics
  mkdir -p "$out"
  export KUBECONFIG="${KUBECONFIG:-.kubeconfig}"
  if [ ! -s "$KUBECONFIG" ] || ! kubectl cluster-info >"$out/cluster-info.txt" 2>&1; then
    echo "cluster API unreachable via '$KUBECONFIG' (never provisioned or still coming up); in-cluster state not captured" \
      | tee "$out/README.txt"
    exit 0
  fi
  # Flux reconciliation state + failure reasons.
  flux get all -A >"$out/flux-get-all.txt" 2>&1 || true
  kubectl get kustomizations,helmreleases,helmrepositories,gitrepositories,ocirepositories \
    -A -o wide >"$out/flux-resources.txt" 2>&1 || true
  kubectl describe kustomizations,helmreleases -A >"$out/flux-describe.txt" 2>&1 || true
  # Secrets plumbing (ESO / cert-manager) - a common bootstrap failure point.
  kubectl get externalsecrets,clustersecretstores,clusterissuers,certificates \
    -A -o wide >"$out/secrets-resources.txt" 2>&1 || true
  kubectl describe externalsecrets,clustersecretstores -A >"$out/secrets-describe.txt" 2>&1 || true
  # Workloads, scheduling, events.
  kubectl get pods -A -o wide >"$out/pods.txt" 2>&1 || true
  kubectl get nodes -o wide >"$out/nodes.txt" 2>&1 || true
  kubectl get events -A --sort-by=.lastTimestamp >"$out/events.txt" 2>&1 || true
  # Logs (current + previous container) for every pod not settled Running/Completed.
  kubectl get pods -A --no-headers 2>/dev/null \
    | awk '$4!="Running" && $4!="Completed"{print $1" "$2}' \
    | while read -r ns pod; do
        kubectl -n "$ns" logs "$pod" --all-containers --tail=200 \
          >"$out/logs_${ns}_${pod}.txt" 2>&1 || true
        kubectl -n "$ns" logs "$pod" --all-containers --previous --tail=200 \
          >"$out/logs_${ns}_${pod}_previous.txt" 2>&1 || true
      done
  # Flux reconciler logs (carry the reconcile errors themselves).
  for c in source-controller kustomize-controller helm-controller; do
    kubectl -n flux-system logs "deploy/$c" --tail=300 \
      >"$out/logs_flux-system_${c}.txt" 2>&1 || true
  done
  # Ingress + DNS path: the Ingress status carries the LoadBalancer address
  # external-dns reads, and external-dns' own log says whether it found the zone
  # and created/updated the record (a Running pod, so not covered by the loop
  # above). The cloudflare-tunnel controller is the local-cluster equivalent.
  kubectl get ingress -A -o wide >"$out/ingresses.txt" 2>&1 || true
  kubectl describe ingress -A >"$out/ingress-describe.txt" 2>&1 || true
  kubectl -n external-dns logs deploy/external-dns --tail=400 \
    >"$out/logs_external-dns.txt" 2>&1 || true
  kubectl -n tunnel logs deploy/cloudflare-tunnel-ingress-controller --tail=400 \
    >"$out/logs_tunnel-controller.txt" 2>&1 || true
  echo "diagnostics captured to $out/"

