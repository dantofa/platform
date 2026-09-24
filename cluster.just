# cluster.just -- reusable cluster-ops recipes for the dantofa platform and its
# downstream projects, under one namespace: `just cluster <area> <action>`.
#
#   just cluster debug                              # snapshot cluster + Flux state
#   just cluster verify backup|restore|db-backup|image-scan  # verify platform infra
#   just cluster local  create|verify|delete|test   # kind cluster lifecycle
#   just cluster local  ca                          # export the local TLS trust anchor
#   just cluster doks   create|bootstrap|connect|delete|list  # DOKS cluster lifecycle
#
# Distributed via the platform Nix flake (`packages.cluster-just`): the platform
# imports it directly; a downstream repo materializes it (flake output / the devbox
# plugin, which also ensures the justfile imports it). These are
# PRIMITIVES -- compose your own e2e (create -> deploy+test your app -> delete) in
# your OWN recipe; the module deliberately does not call back into project recipes.
#
# Config is read from the environment with sane defaults (set in your dev shell /
# devbox / direnv; no just-variable overrides needed):
#   DCTL              how to invoke dctl (default: `nix run github:dantofa/platform#default --`)
#   BASE_DOMAIN       ingress FQDN for `cluster local` (default: local.dantofa.dev)
#   DOKS_CLUSTER      cluster name the `cluster doks` recipes act on (default: doks)
#   DOKS_BASE_DOMAIN  ingress FQDN for `cluster doks bootstrap` (default: doks.dantofa.dev)
#   KUBECONFIG        kubeconfig path (default: ./.kubeconfig)
#   LOCAL_CA_FILE     where `cluster local ca` writes the trust anchor (default: ./.local.pem)
#   TRIVYIGNORE_BASE  mandatory accepted-CVE file (default: ./.trivyignore-cluster)
#   TRIVYIGNORE_LOCAL optional extra accepted-CVE file (default: ./.trivyignore-cluster)
#   DB_BACKUP_BUCKET / DB_BACKUP_ENDPOINT   object store for `cluster verify db-backup`
#                     (default: the cluster's own backup-target ConfigMap)
#   DB_BACKUP_ACCESS_KEY_ID / DB_BACKUP_SECRET_ACCESS_KEY
#                     discrete S3 keys for the barman plugin, which cannot read
#                     Velero's credentials-file Secret (default: the local
#                     SeaweedFS pair -- set these on any other store)
#   DB_DRILL_NS       throwaway namespace for the drill (default: cnpg-pitr-drill)

# Dispatchers: `just cluster <area> <action>` -> `just cluster-<area>-<action>`,
# extending the platform's existing `local {{action}}` / `github {{action}}` pattern.
cluster area *args:
  just cluster-{{area}} {{args}}

cluster-verify action *args:
  just cluster-verify-{{action}} {{args}}

cluster-local action *args:
  just cluster-local-{{action}} {{args}}

cluster-doks action *args:
  just cluster-doks-{{action}} {{args}}

# Generic cluster health gate, independent of how the cluster was provisioned:
# every node Ready, then the whole GitOps tree reconciled. This is the part of a
# cluster verification that is NOT cluster-type-specific -- `cluster local verify`
# composes it after connecting to kind, and a DOKS/preview flow composes it after
# `cluster doks connect`, so both gate on exactly the same conditions. Acts on
# whatever ${KUBECONFIG:-.kubeconfig} points at.
cluster-verify-health:
  #!/usr/bin/env bash
  set -euo pipefail
  export KUBECONFIG="${KUBECONFIG:-.kubeconfig}"
  # shellcheck disable=SC2206
  dctl=(${DCTL:-nix run github:dantofa/platform#default --})
  kubectl wait --for=condition=Ready nodes --all --timeout="${NODE_TIMEOUT:-180s}"
  # Generous default: a cold cluster pulls every image before a root reports Ready.
  "${dctl[@]}" flux kustomization verify --wait --timeout "${FLUX_TIMEOUT:-600s}" \
    --kubeconfig "$KUBECONFIG"

# Integration check (CI): assert Flux installed Velero and a backup completes.
# Targets the cluster in $KUBECONFIG; run after bootstrapping the backup stack.
cluster-verify-backup:
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
  # Fully-qualified resource.group (backups.velero.io) so the short `backup` name
  # can never resolve to a different group's Backup CRD if one is later installed.
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
# marker returns. Run after `cluster verify backup` (which waits for Velero + an
# Available BackupStorageLocation). Fully-qualified resource.group on
# `backups`/`restores` (backups.velero.io / restores.velero.io) so the short names
# can never resolve to a different group's CRD if one is later installed.
cluster-verify-restore:
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

# CNPG point-in-time-recovery drill: prove the barman-cloud plugin can archive a
# database and bring it BACK, not merely that its controller is Running. Same
# principle as `cluster verify restore` one layer down -- seed a marker, destroy
# the database, recover it, assert the marker returns -- applied to the half of
# recovery Velero deliberately does not cover: Velero owns the objects around a
# database, CNPG owns the database itself (see the cnpg-exclude-from-velero-backup
# policy).
#
# What it would catch that nothing else does: a chart bump that breaks CNPG-I
# registration or the ObjectStore API. "Kustomization Ready" only means the plugin
# Deployment rolled out, which is true of a plugin that archives nothing.
#
# Creates only throwaway resources in $DB_DRILL_NS and deletes them on the way out
# -- the platform ships no `Cluster` CRs and this does not change that.
#
# Storage coordinates default to the cluster's own backup target (the backup-target
# ConfigMap written for Velero) under a drill-specific prefix, so on kind this runs
# against SeaweedFS with no configuration. Credentials are separate and explicit:
# the plugin wants discrete accessKeyId/secretAccessKey, which Velero's
# credentials-file Secret does not hold, so they come from the environment and
# default to the local SeaweedFS pair. Point them at a real key on any other store.
cluster-verify-db-backup:
  #!/usr/bin/env bash
  set -euo pipefail
  export KUBECONFIG="${KUBECONFIG:-.kubeconfig}"
  ns="${DB_DRILL_NS:-cnpg-pitr-drill}"
  store=drill-store
  src=drill
  restored=drill-restored
  marker="pitr-$(date +%s)"
  # A fresh destination prefix per run, not a fixed one. CNPG runs
  # barman-cloud-check-wal-archive before it will enable archiving, and that check
  # fails against a server directory that already holds another run's data -- so a
  # fixed prefix passes once and then fails every run after it, which is a trap
  # rather than a test. The archive is left behind under its prefix; on kind the
  # cluster is throwaway, and on a long-lived store the prefix is a few tens of MB
  # to drop.
  prefix="${ns}/${marker}"

  # Storage class follows the cluster (standard on kind, do-block-storage on DOKS);
  # cluster-vars carries it already, so the drill does not need its own knob.
  sc="${DB_DRILL_STORAGE_CLASS:-$(kubectl -n flux-system get configmap cluster-vars -o jsonpath='{.data.storage_class}')}"
  bucket="${DB_BACKUP_BUCKET:-$(kubectl -n velero get configmap backup-target -o jsonpath='{.data.bucket}')}"
  endpoint="${DB_BACKUP_ENDPOINT:-$(kubectl -n velero get configmap backup-target -o jsonpath='{.data.endpoint}')}"
  # No region: SeaweedFS ignores it and the endpoint carries the rest. A store that
  # requires one (real S3) needs it added to the Secret and referenced from
  # s3Credentials.region.
  key_id="${DB_BACKUP_ACCESS_KEY_ID:-localbackup}"
  key_secret="${DB_BACKUP_SECRET_ACCESS_KEY:-localbackupsecret}"
  : "${bucket:?no bucket: set DB_BACKUP_BUCKET or run against a cluster with a backup-target ConfigMap}"
  echo "PITR drill: s3://${bucket}/${prefix} via ${endpoint} (storageClass ${sc})"

  # The namespace is deleted on the way out either way, so a failing run has to
  # print its post-mortem first. What breaks here is almost never visible in the
  # kubectl error: a wrong key, an unreachable endpoint or a prefix that already
  # holds another run's archive all surface as a Cluster condition or a line in the
  # plugin log, while the drill just reports a timeout.
  cleanup() {
    status=$?
    if [ "$status" -ne 0 ]; then
      echo "--- drill resources ---"
      kubectl -n "$ns" get cluster.postgresql.cnpg.io,objectstore,backup.postgresql.cnpg.io,pods 2>&1 || true
      echo "--- cluster conditions ---"
      kubectl -n "$ns" get cluster.postgresql.cnpg.io -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{range .status.conditions[*]}  {.type}={.status} {.reason} {.message}{"\n"}{end}{end}' 2>&1 || true
      echo "--- recent events ---"
      kubectl -n "$ns" get events --sort-by=.lastTimestamp 2>&1 | tail -20 || true
      echo "--- barman plugin log ---"
      kubectl -n cnpg-system logs deploy/plugin-barman-cloud --tail=50 2>&1 || true
      echo "--- instance logs ---"
      kubectl -n "$ns" logs -l cnpg.io/podRole=instance --all-containers --tail=50 2>&1 || true
    fi
    kubectl delete namespace "$ns" --wait=false >/dev/null 2>&1 || true
  }
  trap cleanup EXIT

  # The plugin and the operator must be up before anything below means anything.
  kubectl -n cnpg-system wait --for=condition=Ready --timeout=600s helmrelease/cloudnative-pg
  kubectl -n cnpg-system wait --for=condition=Ready --timeout=600s helmrelease/plugin-barman-cloud

  kubectl create namespace "$ns" --dry-run=client -o yaml | kubectl apply -f -
  kubectl -n "$ns" create secret generic drill-object-store \
    --from-literal=ACCESS_KEY_ID="$key_id" \
    --from-literal=ACCESS_SECRET_KEY="$key_secret" \
    --dry-run=client -o yaml | kubectl apply -f -

  # The ObjectStore is the plugin's own CRD -- the thing that proves it is
  # registered and reconciling, not just deployed.
  object_store="
  apiVersion: barmancloud.cnpg.io/v1
  kind: ObjectStore
  metadata:
    name: ${store}
    namespace: ${ns}
  spec:
    configuration:
      destinationPath: s3://${bucket}/${prefix}
      endpointURL: ${endpoint}
      s3Credentials:
        accessKeyId:
          name: drill-object-store
          key: ACCESS_KEY_ID
        secretAccessKey:
          name: drill-object-store
          key: ACCESS_SECRET_KEY
      wal:
        compression: gzip"
  printf '%s\n' "$object_store" | kubectl apply -f -

  source_cluster="
  apiVersion: postgresql.cnpg.io/v1
  kind: Cluster
  metadata:
    name: ${src}
    namespace: ${ns}
  spec:
    instances: 1
    storage:
      size: 1Gi
      storageClass: ${sc}
    plugins:
      - name: barman-cloud.cloudnative-pg.io
        isWALArchiver: true
        parameters:
          barmanObjectName: ${store}"
  printf '%s\n' "$source_cluster" | kubectl apply -f -

  echo "Waiting for the source cluster to come up..."
  kubectl -n "$ns" wait --for=jsonpath='{.status.phase}'='Cluster in healthy state' \
    cluster.postgresql.cnpg.io/"$src" --timeout=600s
  echo "Waiting for continuous archiving..."
  kubectl -n "$ns" wait --for=condition=ContinuousArchiving \
    cluster.postgresql.cnpg.io/"$src" --timeout=300s

  # Seed, checkpoint, and force a WAL switch so a segment is actually archived
  # rather than sitting half-written in pg_wal.
  kubectl -n "$ns" exec "${src}-1" -c postgres -- psql -U postgres -d app -c \
    "CREATE TABLE drill(marker text); INSERT INTO drill VALUES ('${marker}'); CHECKPOINT; SELECT pg_switch_wal();" >/dev/null
  echo "Seeded marker ${marker}"

  # A base backup through the plugin (method: plugin) -- the other half of a PITR
  # set; WAL alone cannot restore without one.
  base_backup="
  apiVersion: postgresql.cnpg.io/v1
  kind: Backup
  metadata:
    name: drill-base
    namespace: ${ns}
  spec:
    cluster:
      name: ${src}
    method: plugin
    pluginConfiguration:
      name: barman-cloud.cloudnative-pg.io"
  printf '%s\n' "$base_backup" | kubectl apply -f -
  echo "Waiting for the base backup to complete..."
  kubectl -n "$ns" wait --for=jsonpath='{.status.phase}'=completed \
    backup.postgresql.cnpg.io/drill-base --timeout=600s

  # Destroy the database: the Cluster and its volumes, the way a lost namespace
  # would. Everything that survives from here comes out of object storage.
  echo "Destroying the source cluster..."
  kubectl -n "$ns" delete cluster.postgresql.cnpg.io "$src" --timeout=300s
  kubectl -n "$ns" delete pvc --all --timeout=300s

  recovered_cluster="
  apiVersion: postgresql.cnpg.io/v1
  kind: Cluster
  metadata:
    name: ${restored}
    namespace: ${ns}
  spec:
    instances: 1
    storage:
      size: 1Gi
      storageClass: ${sc}
    bootstrap:
      recovery:
        source: ${src}
    externalClusters:
      - name: ${src}
        plugin:
          name: barman-cloud.cloudnative-pg.io
          parameters:
            barmanObjectName: ${store}
            serverName: ${src}"
  printf '%s\n' "$recovered_cluster" | kubectl apply -f -

  echo "Waiting for the recovered cluster..."
  kubectl -n "$ns" wait --for=jsonpath='{.status.phase}'='Cluster in healthy state' \
    cluster.postgresql.cnpg.io/"$restored" --timeout=600s

  got="$(kubectl -n "$ns" exec "${restored}-1" -c postgres -- \
    psql -U postgres -d app -tAc "SELECT marker FROM drill;" | tr -d '[:space:]')"
  if [ "$got" != "$marker" ]; then
    echo "PITR drill FAILED: recovered database has marker '${got}', want '${marker}'" >&2
    exit 1
  fi
  echo "PITR drill OK: ${marker} survived a destroy/recover cycle through the barman-cloud plugin"

# Image security gate: fail if a PLATFORM image (one we deploy) carries a CRITICAL
# vulnerability not accepted in the merged ignore list. DOKS-managed namespaces
# (kube-system) are excluded -- those images are DigitalOcean's to patch. Gated on
# CRITICAL only; HIGH is visibility-only (trivy-operator dashboards + Renovate
# bumps) and reported here but does not fail the build, since platform images are
# third-party charts we can only fix by bumping. Reads the trivy-operator
# VulnerabilityReports (kubectl+jq, no runner-side image pulls); run after the
# stack has reconciled. Reads KUBECONFIG (falls back to ./.kubeconfig). Accepted
# CVEs are the union of a mandatory base file (TRIVYIGNORE_BASE -- the platform's
# suppressions every downstream inherits) and an optional local file
# (TRIVYIGNORE_LOCAL -- the downstream's own app-image CVEs); suppression is
# applied here so trivy-operator's own reports stay complete.
cluster-verify-image-scan:
  #!/usr/bin/env bash
  set -euo pipefail
  export KUBECONFIG="${KUBECONFIG:-.kubeconfig}"
  crd=vulnerabilityreports.aquasecurity.github.io
  base_ignore="${TRIVYIGNORE_BASE:-.trivyignore-cluster}"
  extra_ignore="${TRIVYIGNORE_LOCAL:-.trivyignore-cluster}"
  # trivy-operator scans asynchronously (a fresh cluster's first scans include a
  # trivy-DB download), so wait until the report count is non-zero and stops
  # growing (stable across two polls) before evaluating. Fail closed if no report
  # ever appears -- a gate that silently passes an unscanned cluster is worthless.
  echo "Waiting for trivy-operator VulnerabilityReports to stabilize..."
  deadline=$((SECONDS + 480))
  prev=-1
  stable=0
  while :; do
    n="$( { kubectl get "$crd" -A --no-headers 2>/dev/null || true; } | wc -l | tr -d ' ')"
    echo "  reports: $n"
    if [ "$n" -gt 0 ] && [ "$n" = "$prev" ]; then
      stable=$((stable + 1))
      [ "$stable" -ge 2 ] && break
    else
      stable=0
    fi
    if [ "$SECONDS" -ge "$deadline" ]; then
      [ "$n" -eq 0 ] && { echo "no VulnerabilityReports after 480s (trivy-operator did not scan); failing closed"; exit 1; }
      echo "report count still changing after 480s; evaluating the $n reports present"
      break
    fi
    prev="$n"
    sleep 20
  done
  # DOKS-managed namespaces, excluded from the gate (images we cannot patch).
  ex='["kube-system"]'
  # Accepted CVE IDs = union of the base + optional local ignore files (deduped;
  # missing files skipped, same file listed twice collapses via unique).
  acc="$( { for f in "$base_ignore" "$extra_ignore"; do [ -f "$f" ] && cat "$f"; done; } \
    | jq -Rn '[inputs | select(test("^[[:space:]]*(#|$)") | not)] | unique')"
  reports="$(kubectl get "$crd" -A -o json)"
  # Informational (non-gating): distinct HIGH CVEs in platform images.
  high="$(jq --argjson ex "$ex" '[ .items[]
    | select((.metadata.namespace) as $ns | $ex | index($ns) | not)
    | (.report.vulnerabilities // [])[] | select(.severity=="HIGH") | .vulnerabilityID
  ] | unique | length' <<<"$reports")"
  echo "Suppressed CVEs: $(jq 'length' <<<"$acc"); HIGH CVEs in platform images (not gated): $high"
  # Gate: unsuppressed CRITICAL in platform images. One TSV row each: CVE PACKAGE NS IMAGE.
  findings="$(jq -r --argjson acc "$acc" --argjson ex "$ex" '
    .items[]
    | .metadata.namespace as $ns
    | select($ex | index($ns) | not)
    | ((.report.artifact.repository // "?") + ":" + (.report.artifact.tag // "")) as $img
    | (.report.vulnerabilities // [])[]
    | select(.severity == "CRITICAL")
    | select(.vulnerabilityID as $id | ($acc | index($id)) | not)
    | [.vulnerabilityID, (.resource // "?"), $ns, $img] | @tsv
  ' <<<"$reports" | sort -u)"
  if [ -n "$findings" ]; then
    echo "IMAGE SCAN GATE FAILED -- unsuppressed CRITICAL vulnerabilities in platform images:"
    printf 'CVE\tPACKAGE\tNAMESPACE\tIMAGE\n'
    printf '%s\n' "$findings"
    echo
    echo "Bump the offending chart to a fixed image; if no fix exists yet, add the CVE ID to your .trivyignore-cluster with a reason."
    exit 1
  fi
  echo "IMAGE SCAN GATE PASSED -- no unsuppressed CRITICAL vulnerabilities in platform images."

# Create the kind cluster (1 control-plane + 2 workers), then bootstrap the platform
# onto it. `*args` pass through to bootstrap (e.g. --grafana-cloud). Uses $DCTL (default:
# `nix run github:dantofa/platform#default --`; the platform overrides it to
# `go run ./cmd/dctl`).
cluster-local-create *args:
  #!/usr/bin/env bash
  set -euo pipefail
  # shellcheck disable=SC2206
  dctl=(${DCTL:-nix run github:dantofa/platform#default --})
  "${dctl[@]}" local cluster create --control-planes 1 --workers 2
  just cluster-local-bootstrap {{args}}

# Bootstrap the platform (Flux + ingress + the shared stacks) onto an already-created
# cluster — split from create so you can (re-)bootstrap without recreating, e.g. to
# add --grafana-cloud. Reads BWS_* for the ESO secret-zero (local needs no DO/Spaces
# creds); uses $DCTL and $BASE_DOMAIN.
cluster-local-bootstrap *args:
  #!/usr/bin/env bash
  set -euo pipefail
  : "${BWS_ACCESS_TOKEN:?set BWS_ACCESS_TOKEN (dev shell + bws login)}"
  : "${BWS_PROJECT_ID:?set BWS_PROJECT_ID}"
  : "${BWS_ORGANIZATION_ID:?set BWS_ORGANIZATION_ID}"
  base_domain="${BASE_DOMAIN:-local.dantofa.dev}"
  # shellcheck disable=SC2206
  dctl=(${DCTL:-nix run github:dantofa/platform#default --})
  "${dctl[@]}" local cluster bootstrap --base-domain "$base_domain" {{args}}

# Generic health verification of the running cluster: kubeconfig fetched, all
# nodes Ready, the whole GitOps tree reconciled, then the platform-infra checks
# (Velero backup + the image-security gate). App-level verification is the
# consumer's own compose recipe.
cluster-local-verify:
  #!/usr/bin/env bash
  set -euo pipefail
  export KUBECONFIG=.kubeconfig
  # shellcheck disable=SC2206
  dctl=(${DCTL:-nix run github:dantofa/platform#default --})
  "${dctl[@]}" local cluster connect
  # Everything past `connect` is cluster-type-agnostic -- same gates as any other
  # cluster type runs.
  just cluster-verify-health
  just cluster-verify-backup
  just cluster-verify-image-scan

# Delete the cluster via the graceful teardown (drains DNS records, stops the
# ingress controller, reaps any tunnel). Uses $DCTL.
cluster-local-delete *args:
  #!/usr/bin/env bash
  set -euo pipefail
  # shellcheck disable=SC2206
  dctl=(${DCTL:-nix run github:dantofa/platform#default --})
  "${dctl[@]}" local cluster delete {{args}}

# List the local (kind) clusters on this machine. A thin pass-through to dctl, kept
# for verb-completeness alongside create/delete so the namespace reads the same on
# both cluster types. Uses $DCTL.
cluster-local-list:
  #!/usr/bin/env bash
  set -euo pipefail
  # shellcheck disable=SC2206
  dctl=(${DCTL:-nix run github:dantofa/platform#default --})
  "${dctl[@]}" local cluster list

# Generic smoke: create -> verify -> delete (delete runs after, on success only).
# Consumers wanting an app-inclusive e2e write their own recipe composing these.
cluster-local-test: cluster-local-create cluster-local-verify && cluster-local-delete

# Export the local cluster's CA (the trust anchor cert-manager mints, from which
# Traefik's default cert is issued) to a file, and print how to point tools at it.
# This is what replaces per-tool skip-verify flags: one anchor, understood by every
# OpenSSL/Go/Python/Node HTTP stack via the standard CA-bundle env vars. Re-run it
# after recreating the cluster -- the CA is minted per cluster, so a fresh cluster
# means a fresh anchor. Config: ${KUBECONFIG:-.kubeconfig}, ${LOCAL_CA_FILE:-.local.pem}.
cluster-local-ca:
  #!/usr/bin/env bash
  set -euo pipefail
  export KUBECONFIG="${KUBECONFIG:-.kubeconfig}"
  out="${LOCAL_CA_FILE:-.local.pem}"
  # The CA Certificate is minted asynchronously after the cluster reconciles, so
  # give it a moment on a freshly-bootstrapped cluster rather than failing hard.
  for _ in $(seq 1 30); do
    if kubectl -n cert-manager get secret local-ca >/dev/null 2>&1; then break; fi
    sleep 2
  done
  if ! kubectl -n cert-manager get secret local-ca >/dev/null 2>&1; then
    echo "no local-ca secret in the cert-manager namespace -- is the cluster bootstrapped?" >&2
    exit 1
  fi
  kubectl -n cert-manager get secret local-ca -o jsonpath='{.data.tls\.crt}' | base64 -d > "$out"
  if [ ! -s "$out" ]; then
    echo "local-ca secret held no certificate" >&2
    exit 1
  fi
  abs="$(cd "$(dirname "$out")" && pwd)/$(basename "$out")"
  echo "local CA written to $abs"
  echo
  echo "Trust it for every tool in this shell:"
  echo "  export SSL_CERT_FILE=$abs CURL_CA_BUNDLE=$abs REQUESTS_CA_BUNDLE=$abs NODE_EXTRA_CA_CERTS=$abs GIT_SSL_CAINFO=$abs"
  echo
  echo "Browsers use their own store; import once with:"
  echo "  certutil -d sql:\$HOME/.pki/nssdb -A -t 'C,,' -n dantofa-local -i $abs"

# --- DOKS (DigitalOcean) cluster lifecycle -------------------------------------
#
# The `cluster doks` namespace mirrors `cluster local` for the verbs where a DOKS
# analogue is meaningful. Two deliberate differences from the local namespace:
#
#   * There is no `cluster doks test`. The local one is create -> verify -> delete;
#     on DOKS that provisions and destroys BILLED infrastructure behind one word.
#     Compose it yourself in a workflow where the intent is explicit.
#   * `cluster doks create` does NOT bootstrap (the local one does). A DOKS cluster
#     takes minutes to come up and is usually bootstrapped more than once with
#     different flags per environment (--dolb / --tls-issuer / --env), so the two
#     stay separate steps.
#
# Every recipe acts on ${DOKS_CLUSTER:-doks} -- a scratch-cluster default, never a
# production name -- so a mistyped or forgotten override cannot touch a real
# cluster. Point them elsewhere by exporting DOKS_CLUSTER. Needs
# DIGITALOCEAN_ACCESS_TOKEN in the environment (dctl reads it directly).

# List the DOKS clusters on the account. Uses $DCTL.
cluster-doks-list:
  #!/usr/bin/env bash
  set -euo pipefail
  # shellcheck disable=SC2206
  dctl=(${DCTL:-nix run github:dantofa/platform#default --})
  "${dctl[@]}" digitalocean cluster list

# Fetch the cluster's kubeconfig so the cluster-type-agnostic recipes
# (`cluster verify health|backup|restore|image-scan`, `cluster debug`) can run
# against it -- the DOKS counterpart of `dctl local cluster connect`. `*args` pass
# through. Config: ${DOKS_CLUSTER:-doks}, ${KUBECONFIG:-.kubeconfig}.
cluster-doks-connect *args:
  #!/usr/bin/env bash
  set -euo pipefail
  cluster="${DOKS_CLUSTER:-doks}"
  kubeconfig="${KUBECONFIG:-.kubeconfig}"
  # shellcheck disable=SC2206
  dctl=(${DCTL:-nix run github:dantofa/platform#default --})
  "${dctl[@]}" digitalocean cluster connect "$cluster" --output "$kubeconfig" {{args}}

# Create the DOKS cluster. --wait so it is actually running when the recipe
# returns, since `cluster doks bootstrap` needs a reachable API server. `*args`
# pass through to create (--ha, --node-pool-size, --region, --node-pool-min/max,
# --version, --tag). Config: ${DOKS_CLUSTER:-doks}.
cluster-doks-create *args:
  #!/usr/bin/env bash
  set -euo pipefail
  cluster="${DOKS_CLUSTER:-doks}"
  # shellcheck disable=SC2206
  dctl=(${DCTL:-nix run github:dantofa/platform#default --})
  echo "Creating DOKS cluster '${cluster}' (billed infrastructure)..."
  "${dctl[@]}" digitalocean cluster create --name "$cluster" --wait {{args}}

# Bootstrap the platform onto an already-created DOKS cluster. `*args` pass through
# and carry the per-environment decisions (--dolb, --tls-issuer, --env,
# --grafana-cloud); dctl reads the bws secret-zero and the Spaces region from the
# environment itself. Config: ${DOKS_CLUSTER:-doks},
# ${DOKS_BASE_DOMAIN:-doks.dantofa.dev}.
cluster-doks-bootstrap *args:
  #!/usr/bin/env bash
  set -euo pipefail
  cluster="${DOKS_CLUSTER:-doks}"
  base_domain="${DOKS_BASE_DOMAIN:-doks.dantofa.dev}"
  # shellcheck disable=SC2206
  dctl=(${DCTL:-nix run github:dantofa/platform#default --})
  "${dctl[@]}" digitalocean cluster bootstrap "$cluster" --base-domain "$base_domain" {{args}}

# Delete the DOKS cluster via the graceful teardown (drains DNS records, stops the
# ingress controller, reaps any tunnel). Destroys billed infrastructure, so the
# target is echoed before acting -- read it. `*args` pass through (--force,
# --no-teardown). Config: ${DOKS_CLUSTER:-doks}.
cluster-doks-delete *args:
  #!/usr/bin/env bash
  set -euo pipefail
  cluster="${DOKS_CLUSTER:-doks}"
  # shellcheck disable=SC2206
  dctl=(${DCTL:-nix run github:dantofa/platform#default --})
  echo "Deleting DOKS cluster '${cluster}' and its ingress DNS records..."
  "${dctl[@]}" digitalocean cluster delete "$cluster" {{args}}

# Launch Chromium against the local cluster. The cluster publishes its ingress on
# the host's real :80/:443 and ${BASE_DOMAIN} resolves to 127.0.0.1, so the real URL
# just works — this recipe only adds what is still cluster-specific: trusting the
# self-signed Traefik cert, and a fresh --user-data-dir so the flags apply even when
# Chromium is already running. `*args` pass through (e.g. a URL to open). Assumes a
# bootstrapped local cluster. Config: ${CHROMIUM:-chromium}.
cluster-local-chromium *args:
  #!/usr/bin/env bash
  set -euo pipefail
  # shellcheck disable=SC2206
  chromium=(${CHROMIUM:-chromium})
  "${chromium[@]}" --ignore-certificate-errors --user-data-dir="$(mktemp -d)" {{args}}

# Run Playwright against the local cluster. Nothing has to be injected into the
# browser any more (the real URL resolves and connects on its own) — set
# `baseURL: https://${BASE_DOMAIN}` and `ignoreHTTPSErrors: true` in your
# playwright.config and run Playwright directly; this recipe just exports BASE_URL
# for a config that reads it. Pin @playwright/test to the flake's playwright-driver.
# `*args` pass through (e.g. `test`). Config: ${PLAYWRIGHT:-npx playwright}.
cluster-local-playwright *args:
  #!/usr/bin/env bash
  set -euo pipefail
  BASE_URL="https://${BASE_DOMAIN:-local.dantofa.dev}"
  export BASE_URL
  # shellcheck disable=SC2206
  playwright=(${PLAYWRIGHT:-npx playwright})
  "${playwright[@]}" {{args}}

# curl the local cluster ingress at its real URL. Only --insecure is still needed
# (Traefik serves a self-signed cert locally); resolution and routing need nothing.
# `*args` pass through (the URL + any flags). Assumes a bootstrapped local cluster.
cluster-local-curl *args:
  #!/usr/bin/env bash
  set -euo pipefail
  curl --insecure {{args}}

# Snapshot cluster + Flux state into ./preview-diagnostics for CI to upload as
# artifacts on a failed run (the cluster is destroyed afterwards, so this is the
# only surviving record). Everything is best-effort: a partial capture from a
# half-provisioned or unreachable cluster is still worth keeping, so this recipe
# never fails the run. Reads KUBECONFIG (falls back to ./.kubeconfig).
cluster-debug:
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
  # cert-manager issuance chain: ClusterIssuer readiness, and the ACME
  # Certificate -> CertificateRequest -> Order -> Challenge chain that stalls
  # when DNS-01 (or the solver token secret) is not right. Plus the controller
  # log (a Running pod, so not covered by the not-settled loop below).
  kubectl get certificaterequests,orders,challenges -A -o wide \
    >"$out/cert-manager-chain.txt" 2>&1 || true
  kubectl describe clusterissuers,certificates,certificaterequests,orders,challenges -A \
    >"$out/cert-manager-describe.txt" 2>&1 || true
  kubectl -n cert-manager logs deploy/cert-manager --tail=400 \
    >"$out/logs_cert-manager.txt" 2>&1 || true
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
