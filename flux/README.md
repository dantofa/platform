# `flux/` — the platform GitOps tree

One source (`platform`), a **shared** stack tree, and per-cluster **reconcile
roots** that `dctl` applies:

- `flux/cluster/` — the **shared, source-agnostic** stacks every cluster loads
  (Velero backup + Kyverno policy engine). The `cluster` reconcile root points
  here (`path: ./flux/cluster`) on both DOKS and kind. Each nested stack's
  `sourceRef` uses `${source_kind}/${source_name}` so the same tree binds to a
  `GitRepository` (DOKS/git) or an `OCIRepository` (local) per how the cluster was
  bootstrapped.
- `flux/local/` — the **local/kind-only requirements** (an in-cluster SeaweedFS S3
  backend that stands in for a cloud bucket, plus the backup contract). The
  `local-requirements` reconcile root points here (`path: ./flux/local`);
  `dctl local cluster bootstrap`/`push` publishes the OCI artifact it pulls. It is
  never reconciled from git, so its nested Kustomizations hardcode `OCIRepository`.
  The `cluster` root `dependsOn` it, so the backup target exists before Velero.

Add a shared stack by dropping `<stack>.yaml` + a `<stack>/` directory into
`flux/cluster/` and listing it in `flux/cluster/kustomization.yaml` — one line, no
per-cluster wrappers.

## `cluster-vars` — per-cluster values

`dctl … cluster bootstrap` writes a `cluster-vars` ConfigMap in `flux-system` with
this cluster's identity: `source_kind`, `source_name`, `base_domain`,
`cluster_name`. The reconcile roots that reconcile a portable tree carry
`postBuild.substituteFrom` it, so the shared manifests (and downstream
Kustomizations that opt in) resolve those `${...}` tokens to per-cluster values.
It is the single source of cluster-scoped variables.

`dctl` applies the roots as Kustomization **CRs** (via client-go), not through
`flux create kustomization` — which can't express `postBuild.substituteFrom` or
`dependsOn`. Each root is created with `wait: true`, so it is Ready only once the
objects it applies are (what `dependsOn` and the `dctl flux kustomization verify`
gate rely on).

## Local backup target (SeaweedFS) — `flux/local`

`flux/local` reproduces the two objects Velero consumes, backed by an in-cluster
S3 store instead of a cloud bucket. `dctl local cluster bootstrap` creates the
`velero` namespace imperatively (so no stack here declares it and the shared
`flux/cluster/velero` stack stays its sole Flux owner). Nested stacks:

1. `seaweedfs/operator/` — the SeaweedFS operator (+ CRDs) via its Helm chart, in
   its own `seaweedfs` namespace.
2. `seaweedfs/cluster/` — a single-node `Seaweed` cluster with the S3 gateway, a
   `Bucket`, and a static S3 identity (`seaweedfs_s3_config`), in the `velero`
   namespace. **Local test only:** the credentials are well-known and non-secret.
3. `backup/` — the `backup-credential` Secret (same keys as the S3 identity) and
   the `backup-target` ConfigMap (`endpoint` = the SeaweedFS S3 service), both in
   the `velero` namespace.

The S3 identity in `seaweedfs/cluster/s3-config.yaml` and the Velero credential in
`backup/credential.yaml` must stay in sync.

## Backup stack (Velero) — `flux/cluster/velero`

Velero consumes two objects from the `velero` namespace, regardless of backend:

- Secret `backup-credential` (key `cloud`, an AWS-style credentials file) —
  consumed by Velero directly.
- ConfigMap `backup-target` (keys `bucket`, `region`, `endpoint`) — the
  backend-specific coordinates.

On DOKS these are written by `dctl` (which links a bucket and mints a
**bucket-scoped** Spaces key); the provider API token never enters the cluster.
On local they come from the `flux/local/backup` stack above.

### Two substitution scopes

Substitution happens at two levels, by scope:

- The **`cluster` root** (`./flux/cluster`) — `substituteFrom` the `cluster-vars`
  ConfigMap, filling `${source_kind}`/`${source_name}` (and, for the ingress
  stacks, `${base_domain}`) into the shared Kustomizations before they apply.
- The nested **`velero` Kustomization** (`./flux/cluster/velero`) —
  `substituteFrom` the `backup-target` ConfigMap, filling `${bucket}` /
  `${region}` / `${endpoint}` in `release.yaml` from the coordinates the cluster
  was linked with (namespace-scoped, so it lives one level down).

Rotating the credential (`dctl do space link <bucket>` again) rewrites the Secret
in place; Velero picks it up without manifest changes.
