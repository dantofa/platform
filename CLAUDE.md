# CLAUDE.md

Guidance for working in this repository. Read this before adding dependencies,
tooling, or CI.

## What this is

`dantofa/platform` is a Go tool — the `dctl` control CLI (and, later, its
Kubernetes operator) — that provisions and manages DigitalOcean / local (kind)
clusters and their platform infrastructure. The binary is `dctl`
(`cmd/dctl`), built with [cobra](https://github.com/spf13/cobra). Distribution
is **Nix-flake-only** (no pip/uvx channel).

## Layout (three layers → Go packages)

- `cmd/dctl/` — the binary entrypoint; wires the cobra root and injects the
  build-stamped version (`internal/version`).
- `internal/commands/` — **presentation**: cobra commands only.
- `internal/core/` — **application logic**: framework-free (domain types, Go
  interfaces, the opinionated builders). Imports neither cobra nor `clients`.
- `internal/clients/` — **adapters**: `godo` (DO REST / clusters),
  `aws-sdk-go-v2` (Spaces S3), `os/exec` (kind/flux/docker/git). SDK types live
  here and never leak into `core`.

## Architecture: thin command layer (important)

**Cobra commands MUST NOT implement application logic.** A command only declares
flags/args, calls a function from `internal/core` (through an interface the
`clients` adapter satisfies), and renders the result (output + exit code). All
real work — computation, I/O, validation, external calls — lives in `core`,
which never imports cobra or a client SDK.

Why: the logic stays unit-testable without the CLI, and the CLI is a swappable
adapter (the future operator reuses the same `core`). Write the `core` function
first, then a thin command that calls it.

This is **enforced**: golangci-lint's `depguard` (config in `.golangci.yml`)
fails CI if anything under `internal/core` imports the CLI framework or
`internal/clients`. It replaces the Python `import-linter` contract.

Idiom: use **methods** where a type must satisfy an interface or carry state
(the client adapters); use **free functions** (incl. generics / `samber/lo`) for
stateless transforms and helpers.

### Opinionated core builders

The DigitalOcean cluster spec builders in `core` are deliberately **opinionated**
— not neutral DO API wrappers. They bake fixed product invariants into every
spec (a single autoscaling node pool; auto-upgrade and surge-upgrade always
enabled; HA enable-only, never emitted as `false`) rather than exposing them as
caller choices. `core` builds a **neutral spec** with the invariants baked in;
the `clients` adapter mechanically maps that spec to the `godo` request type
(keeping godo out of `core` and the invariants impossible to bypass from any
adapter). Do not "tidy" these into pass-through builders: if a caller ever
legitimately needs to _not_ enforce an invariant, add a parameter deliberately.

## GitOps tree (`flux/`)

Flux reconciles the platform stacks from one source (`platform`). `dctl … cluster
bootstrap` installs Flux, registers the source (a `GitRepository` on DOKS, an
`OCIRepository` on kind), and applies the **reconcile roots** as Kustomization CRs
via client-go — not `flux create kustomization`, which can't express
`postBuild.substituteFrom` or `dependsOn` — each with `wait: true`, so a root is
Ready only once the objects it applies are (what `dependsOn` and the `dctl flux
kustomization verify` gate rely on).

- `flux/cluster/` — the shared, source-agnostic stacks every cluster loads (the
  `cluster` root, `path: ./flux/cluster`): **cert-manager, External Secrets
  Operator, Velero, Kyverno, Trivy**. A per-cluster-type **ingress** layer loads
  after (`flux/ingress/traefik` + `external-dns` on DOKS, `flux/ingress/tunnel` on
  kind), plus the ACME issuer layer (`flux/ingress/letsencrypt`) for
  `--tls-issuer letsencrypt|staging`.
- `flux/local/` — kind-only requirements (the `local-requirements` root): an
  in-cluster SeaweedFS S3 store standing in for a cloud bucket. OCI-only; the
  `cluster` root `dependsOn` it so the backup target exists before Velero.

Add a shared stack by dropping `<stack>.yaml` + a `<stack>/` directory into
`flux/cluster/` and listing it in `flux/cluster/kustomization.yaml` (one line) —
**and add the deployed tool to the toolset list in `README.md`** (that list is the
single user-facing record of what the platform deploys; keep it in sync).

**cluster-vars + substitution.** Bootstrap writes a `cluster-vars` ConfigMap
(`source_kind`, `source_name`, `base_domain`, `cluster_name`, `tls_issuer`,
`acme_server`, `dns_zone`, …) in `flux-system`; substituting roots carry
`postBuild.substituteFrom` it, so the portable manifests resolve `${…}` to
per-cluster values. Substitution is **two-scoped**: the `cluster` root fills
`${source_kind}`/`${base_domain}`/etc. from `cluster-vars`; the nested `velero`
Kustomization fills `${bucket}`/`${region}`/`${endpoint}` from the `backup-target`
ConfigMap (namespace-scoped, one level down). On DOKS `dctl` writes `backup-target`
plus the `backup-credential` Secret (a bucket-scoped Spaces key — the DO token
never enters the cluster); on kind both come from `flux/local`.

**Secrets.** Secrets that are a source of truth (API tokens, machine-account
credentials) come from Bitwarden via ESO ExternalSecrets against the `bitwarden`
ClusterSecretStore. **Disposable, cluster-local secrets** (no external source of
truth — e.g. a generated admin password) are minted **in-cluster** by an ESO
`Password` generator (`generators.external-secrets.io`), consumed by an
ExternalSecret via `dataFrom.sourceRef.generatorRef` — no bitwarden entry, no
manual step. (No stack currently uses this — it backed the in-cluster Grafana admin
password, dropped when visualization moved to Grafana Cloud — but it stays documented
as the sanctioned pattern for disposable secrets.)

**Secret-zero exceptions (planted by `dctl`, not ESO).** Two credentials cannot come
from ESO, because ESO itself is reconciled _from_ the source they unlock: the Bitwarden
machine-account token (`ProvisionESOAccessToken`) and a **private source credential**.
`dctl flux source create --secret-ref` points a source at a Secret created out of band
(checked to exist first — the flux CLI does not validate the reference, it just blocks on
readiness until its timeout); `--token` mints it (`AddSource` → `SourceSecretStore`) — an
Opaque `username`/`password` Secret for a `GitRepository`, a
`kubernetes.io/dockerconfigjson` pull secret for an `OCIRepository` (the only shape its
`secretRef` honours). SSH deploy keys stay
`--secret-ref`-only: minting one needs a `known_hosts` host-key scan plus an out-of-band
registration on the forge, which `flux create secret git` already does well. The auth
flags are **declarative** — `source create` rewrites the whole object, so omitting them
on a later call strips the credential; pass them every time.

## The two-tier tooling rule (important)

Dependencies are split by purpose:

- **Generic, language-agnostic dev tools live in the flake dev shell**
  (`devShells.default` in `flake.nix`) — the Go toolchain (`go`, `gopls`,
  `golangci-lint`, `gofumpt`, `govulncheck`) plus `just`, `actionlint`,
  `yamllint`, `shellcheck`, `gh`, `pre-commit`, `kubectl`, `bws`, `velero`, and
  the runtime CLIs (`kind`, `flux`, `docker`, `git`). All from the **same pinned
  nixpkgs as the package** (one resolver, one lockfile), and what CI uses too via
  `nix develop --command`. Enter with `nix develop` (or `direnv`). The runtime
  CLIs are shared with the package via the `runtimeTools` list — one source.
- **Go module deps go through `go.mod`/`go.sum`** — add with `go get`, tidy with
  `go mod tidy`. Analyzer tools that are part of the project (e.g. `deadcode`)
  are pinned via the go.mod **`tool` directive** and run with `go tool`.

Rule of thumb: _if it's the environment/toolchain, it's a flake dev-shell
package; if it's a Go dependency of the code, it's in `go.mod`._

## Commands — always via `just`

The justfile is the single source of truth for how tools are invoked (CI and
pre-commit both delegate to it):

- `just run [args]` — `go run ./cmd/dctl`.
- `just test [args]` — `go test ./...`.
- `just build` — `go build` into `dist/dctl`, ldflags-stamping the source-derived
  version. CI's `build` workflow runs this and smoke-tests the binary.
- `just lint` — `gofumpt` (format check) + `go vet` + `golangci-lint` +
  `go tool deadcode` (whole-program dead code) + `actionlint` + `yamllint` +
  `shellcheck` (the last on this justfile's shebang recipes only).
- `just format [args]` — `gofumpt -w`.
- `just sast` — `govulncheck ./...` (security-scoped; keep it that way — dead
  code and other quality checks belong in `just lint`, not here).

When you add or change a tool invocation, edit the relevant `just` target so CI
and pre-commit pick it up automatically.

### The reusable `cluster` module (`cluster.just`)

Cluster-ops recipes live in `cluster.just` (imported by the justfile) under one
namespace — `just cluster debug` (state snapshot), `just cluster verify
backup|restore|image-scan` (platform-infra checks), `just cluster local
create|verify|delete|test` (kind lifecycle) — via nested dispatchers that extend
the `local {{action}}` / `github {{action}}` pattern. It is **shared with
downstream projects**: the flake exposes it as `packages.cluster-just` (and the
base accepted-CVE list as `packages.trivyignore-base`), so a downstream repo
materializes and `import`s it, rev-pinned via its `flake.lock`.

Two rules keep it portable:

- **Config via env with defaults, never `just` interpolation in bodies.** Recipes
  read `${DCTL:-nix run github:dantofa/platform#default --}`, `${BASE_DOMAIN:-…}`,
  `${TRIVYIGNORE_BASE:-…}` etc. as bash (so `just shellcheck` still lints them; a
  `{{arg}}` body is skipped by the linter). The platform overrides `$DCTL` to
  `go run ./cmd/dctl` in the flake devShell.
- **Primitives, not hooks.** The module never calls back into project recipes;
  consumers write their own compose recipe (create → deploy+test app → delete).
  The platform's `local-e2e` is exactly that — its echo/tunnel self-test wrapping
  the shared `cluster local` primitives.

`cluster verify image-scan` merges a mandatory base ignore file (the platform's
suppressions every downstream inherits) with an optional local one — so downstream
only tracks its own app-image CVEs.

## Conventions & constraints

- **Requires Go 1.26.**
- **`just sast` is security-scoped** (`govulncheck`). Quality checks (dead code,
  style) live in `just lint`.
- **Cluster calls ride out transient failures** (`internal/core/retry`). A managed
  API server drops a connection now and then, so a poll loop or a pre-delete read
  must not return on the first error: retry while `retry.Transient` says the
  failure was transport-level, and fail fast otherwise so a real misconfiguration
  (RBAC, bad kubeconfig) still surfaces immediately instead of costing the whole
  timeout. Getting this wrong on the teardown path does not fail safe — it leaves
  a running cluster behind.
- **pre-commit delegates to `just` targets** (`.pre-commit-config.yaml`); the
  hook is installed by the dev shell's `shellHook` on `nix develop` entry
  (skipped under `CI`).
- **CI runs through the flake dev shell** (`.github/workflows/`): install Nix,
  then `nix develop --command just lint` / `test` / `sast` / `build`. A separate
  Go module/build cache (`actions/cache`) sits alongside the nix-store cache
  because `GOMODCACHE`/`GOCACHE` live in `$HOME`. CLI-invoking workflows
  (`local`, `preview`, `teardown`) run the shipped artifact via
  `nix run .#default`; `preview`/`teardown` also use `nix develop --command bws …`
  for secret injection. `nix.yml` builds the packaged artifact and asserts the
  version is stamped.
- **CI forces plain (uncolored) output** via `env: { FORCE_COLOR: "", NO_COLOR:
"1" }` — `FORCE_COLOR` must be **empty** (any non-empty value, even `"0"`,
  forces color and overrides `NO_COLOR`).
- **Dependency updates come from Renovate** (the hosted Mend app, config in
  `.github/renovate.json5`) — the single automated path, replacing Dependabot and
  the ratchet-based Action pinning. It opens PRs for GitHub Actions (kept
  SHA-pinned via `helpers:pinGitHubActionDigests`), Go modules, and the Flux
  manifests (`HelmRelease` chart versions + the Velero plugin image via the `flux`
  manager, and the SeaweedFS `Seaweed` CRD image via a `customManagers` regex).
  A Renovate gomod PR changes `go.sum`, so the flake's `vendorHash` must be
  recomputed on it — Renovate cannot do that itself. The **`vendor-hash` workflow**
  (`.github/workflows/vendor-hash.yml`) does it automatically: on a Renovate PR
  touching `go.mod`/`go.sum` it runs `just vendor-hash` and pushes the result back
  to the PR branch. That push uses the `ACTIONS_TOKEN` secret (a
  contents:write fine-grained PAT or GitHub App token), not `GITHUB_TOKEN`, because
  the strict ruleset must re-run `lint`/`test` on the new commit and `GITHUB_TOKEN`
  pushes don't re-trigger workflows. Run `just vendor-hash` by hand only for a
  non-Renovate `go.sum` change.
- **`just update` is the manual path for what Renovate does not own**: `go get -u
./…` + `go mod tidy`, then `nix flake update` (the flake tracks `nixos-unstable`,
  a rolling branch with no versions to PR, so it stays manual). It runs
  `just vendor-hash` at the end, so the `vendorHash` is recomputed automatically.
- **GitHub Actions are pinned to full commit SHAs** with a `# vX.Y.Z` version
  comment (maintained by Renovate). Declare top-level `permissions: {}` with
  minimal per-job grants, `persist-credentials: false` on checkout, and
  `timeout-minutes` on jobs.

## Versioning & releasing

The version is **derived purely from the flake source**, not git tags (a flake
can't read tags). `flake.nix` computes a CalVer `<YYYY.MM.DD>+g<shortRev>` (the
commit's date + short rev; `-dirty` on an unclean tree) and stamps it into the
binary via `-ldflags -X …/internal/version.Version`. `just build` mirrors this
from `git` for local builds. Do **not** add a static version; the rev is the
identifier. (Real semver is deferred to the operator phase, where container
image tags / CRD versions force it.)

- Consumers track this flake by rev (a Nix input on `master`); `nix flake update`
  locks the latest commit, and `dctl --version` names it. There is no
  version-bearing tag scheme.
- `nix.yml` asserts `dctl --version` matches `*.*.*+g*` (guards a broken version
  injection — the bare "dev" fallback fails it).

## Repository configuration (as code)

Branch protection and repo settings are codified, not clicked:

- `.github/repo-config/ruleset-master.json` — the `master` ruleset: PR required,
  conversation resolution, required `lint`/`test` checks (strict), linear
  history, squash-only merges, no force-push, no deletion.
- `.github/repo-config/repo-settings.json` — squash-only merge, auto-delete head
  branches, auto-merge, squash title/message = PR title/body.

Apply **locally** with `just github repo` (idempotent; upserts via `gh api`)
after editing the JSON. Runs under your own `gh auth login` (admin on the
`dantofa` org) — no PAT/CI secret. Manual, no drift correction: re-run after
edits, and never click the settings in the UI (the JSON wins).

## Before you finish

Run `just lint` and `just test` (or `pre-commit run --all-files`) and make sure
they pass. If you changed Go deps, run `go mod tidy` and `just vendor-hash` to
recompute the flake `vendorHash` — CI builds the package and will fail on a stale
hash.
