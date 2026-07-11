# `flux/` — the platform GitOps tree

Flux reconciles this directory on a bootstrapped cluster. `dctl do cluster
bootstrap` installs Flux and registers a `platform` Kustomization pointing here
(`path: ./flux`); local clusters reconcile the same tree from the OCI artifact
`dctl local cluster push` publishes.

## Backup stack (Velero -> DigitalOcean Spaces)

The credential never round-trips through the DO API token: `dctl` links a
bucket, mints a **bucket-scoped** Spaces key, and writes two objects into the
`velero` namespace —

- Secret `spaces-backup-credentials` (key `cloud`, an AWS-style credentials
  file) — consumed by Velero directly.
- ConfigMap `spaces-backup-target` (keys `bucket`, `region`, `endpoint`) — the
  cluster-specific coordinates.

### Layering (why two Kustomizations)

`flux create kustomization` (what bootstrap runs) cannot set
`postBuild.substituteFrom`, so substitution is pushed one level down:

- `./flux` (this dir, reconciled by `platform`) — no substitution. Applies the
  namespace, the Velero `HelmRepository`, and the nested `velero` Kustomization.
- `./flux/velero` (reconciled by the nested `velero` Kustomization) — has
  `postBuild.substituteFrom` the `spaces-backup-target` ConfigMap, so the
  `${bucket}` / `${region}` / `${endpoint}` placeholders in `helmrelease.yaml`
  are filled from the coordinates the cluster was linked with.

Rotating the credential (`dctl do space link <bucket>` again) rewrites the
Secret in place; Velero picks it up without manifest changes.
