# CLAUDE.md

Guidance for working in this repository. Read this before adding dependencies,
tooling, or CI.

> **Migration in progress.** This project is being rewritten from a Python
> (Typer) CLI to **Go**. Phase 1 (toolchain, flake, packaging) is done; the
> command tree, `core`, and `clients` (Phase 2) are being ported. Sections
> describing those layers state the *intended* Go structure.

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
legitimately needs to *not* enforce an invariant, add a parameter deliberately.

## The two-tier tooling rule (important)

Dependencies are split by purpose:

- **Generic, language-agnostic dev tools live in the flake dev shell**
  (`devShells.default` in `flake.nix`) — the Go toolchain (`go`, `gopls`,
  `golangci-lint`, `gofumpt`, `govulncheck`) plus `just`, `actionlint`,
  `yamllint`, `shellcheck`, `gh`, `ratchet`, `pre-commit`, `kubectl`, `bws`, and
  the runtime CLIs (`kind`, `flux`, `docker`, `git`). All from the **same pinned
  nixpkgs as the package** (one resolver, one lockfile), and what CI uses too via
  `nix develop --command`. Enter with `nix develop` (or `direnv`). The runtime
  CLIs are shared with the package via the `runtimeTools` list — one source.
- **Go module deps go through `go.mod`/`go.sum`** — add with `go get`, tidy with
  `go mod tidy`. Analyzer tools that are part of the project (e.g. `deadcode`)
  are pinned via the go.mod **`tool` directive** and run with `go tool`.

Rule of thumb: *if it's the environment/toolchain, it's a flake dev-shell
package; if it's a Go dependency of the code, it's in `go.mod`.*

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

## Conventions & constraints

- **Requires Go 1.26.**
- **`just sast` is security-scoped** (`govulncheck`). Quality checks (dead code,
  style) live in `just lint`.
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
- **All repository update operations go through `just update`** — never bump
  pins by hand. It runs `ratchet upgrade` (refresh Action SHAs across majors),
  `go get -u ./…` + `go mod tidy`, then `nix flake update`. A go.sum change means
  the flake's `vendorHash` must be recomputed (set `lib.fakeHash`, `nix build`,
  copy the reported hash).
- **GitHub Actions are pinned to full commit SHAs** with a `# ratchet:owner/action@vX`
  marker. Dependabot (`.github/dependabot.yml`, `github-actions` + `gomod`) opens
  weekly PRs; `just update` is the manual path. Declare top-level `permissions: {}`
  with minimal per-job grants, `persist-credentials: false` on checkout, and
  `timeout-minutes` on jobs.

## Versioning & releasing

The version is **derived purely from the flake source**, not git tags (a flake
can't read tags). `flake.nix` computes `0.0.0.dev<lastModifiedDate>+g<shortRev>`
and stamps it into the binary via `-ldflags -X …/internal/version.Version`.
`just build` mirrors this from `git` for local builds. Do **not** add a static
version; the rev is the identifier.

- Consumers track this flake by rev (a Nix input on `master`); `nix flake update`
  locks the latest commit, and `dctl --version` names it. There is no
  version-bearing tag scheme.
- `nix.yml` asserts `dctl --version` matches `0.0.0.dev*+g*` (guards a broken
  version injection).

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
they pass. If you changed Go deps, run `go mod tidy` and recompute the flake
`vendorHash` — CI builds the package and will fail on a stale hash.
