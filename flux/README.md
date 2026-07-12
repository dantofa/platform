# `flux/` — the platform GitOps tree

Two reconcile roots, one per environment:

- `flux/cluster/` — the **remote/DOKS** root. `dctl do cluster bootstrap`
  installs Flux and registers a `platform` Kustomization pointing here
  (`path: ./flux/cluster`). Deploys the Velero backup stack.
- `flux/local/` — the **local/kind** root. `dctl local cluster bootstrap`
  installs Flux and registers a `platform` **OCIRepository** Kustomization
  pointing here (`path: ./flux/local`); `dctl local cluster push` publishes the
  artifact it pulls. Stands up an in-cluster SeaweedFS backup target so the same
  Velero stack works without a cloud provider.

Each root's `kustomization.yaml` lists one nested Flux `Kustomization` per stack
(e.g. `cluster/velero.yaml`); add a stack by dropping in `<stack>.yaml` + a
`<stack>/` directory. The credential/target names below are **provider-agnostic**
so the local target reuses the same contract with a different backend.

## Local backup target (SeaweedFS) — `flux/local`

`flux/local` reproduces the same two objects Velero consumes, backed by an
in-cluster S3 store instead of a cloud bucket. `dctl local cluster bootstrap`
creates the `velero` namespace imperatively (so no stack here declares it and the
reused `flux/cluster/velero` stack stays its sole Flux owner). Nested stacks:

1. `seaweedfs/operator/` — the SeaweedFS operator (+ CRDs) via its Helm chart, in
   its own `seaweedfs` namespace.
2. `seaweedfs/cluster/` — a single-node `Seaweed` cluster with the S3 gateway, a
   `Bucket`, and a static S3 identity (`seaweedfs_s3_config`), in the `velero`
   namespace. **Local test only:** the credentials are well-known and non-secret.
3. `backup/` — the `backup-credential` Secret (same keys as the S3 identity) and
   the `backup-target` ConfigMap (`endpoint` = the SeaweedFS S3 service), both in
   the `velero` namespace.
4. `velero.yaml` — `dependsOn` the `backup` stack and reconciles the shared
   `./flux/cluster/velero` stack with those coordinates substituted in.

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

### Layering (why two Kustomizations)

`flux create kustomization` (what bootstrap runs) cannot set
`postBuild.substituteFrom`, so substitution is pushed one level down:

- `./flux/cluster` (reconciled by `platform`) — no substitution. Applies the
  nested `velero` Kustomization.
- `./flux/cluster/velero` (reconciled by the nested `velero` Kustomization) —
  applies the namespace, the Velero `HelmRepository` and `HelmRelease`, and has
  `postBuild.substituteFrom` the `backup-target` ConfigMap, so the `${bucket}` /
  `${region}` / `${endpoint}` placeholders in `release.yaml` are filled from the
  coordinates the cluster was linked with.

Rotating the credential (`dctl do space link <bucket>` again) rewrites the
Secret in place; Velero picks it up without manifest changes.
