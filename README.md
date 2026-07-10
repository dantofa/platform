# dantofa platform

`dctl` — the dantofa platform control CLI (and, in time, its operator). A Go
tool that provisions and manages DigitalOcean / local Kubernetes clusters and
their platform infrastructure.

## Install

Distributed as a **Nix flake** (not published to a package index).

Run it once:

```bash
nix run github:dantofa/platform -- --help
```

Install into your profile:

```bash
nix profile install github:dantofa/platform
```

The build wraps `dctl` so kind, flux, docker, and git travel in its closure — no
separate install (a running Docker daemon is still a host prerequisite). Pin a
commit by appending it: `github:dantofa/platform/<rev>`.

Consume it as a flake input:

```nix
inputs.dantofa-platform.url = "github:dantofa/platform";
# per system: dantofa-platform.packages.${system}.default   # the wrapped dctl
```

`nix flake update` locks the latest `master` commit, and `dctl --version`
reports it as `0.0.0.dev<date>+g<sha>`.

## Usage

```bash
$ dctl --help
$ dctl --version
```

## Development

Requires [Nix](https://nixos.org/) with flakes. The flake dev shell provides the
Go toolchain (go, gopls, golangci-lint, gofumpt, govulncheck) plus generic
tooling (just, kind/flux/docker/git, kubectl, bws, linters). Enter it with `nix
develop` (or [`direnv`](https://direnv.net/)) — this also installs the
pre-commit hook. Copy `.env.example` to `.env` for local secrets (e.g. the
Bitwarden access token); the shell loads it automatically.

Common tasks run via [`just`](https://github.com/casey/just):

```bash
just run [args]    # go run ./cmd/dctl
just test          # go test ./...
just build         # build dist/dctl (version-stamped)
just lint          # gofumpt, go vet, golangci-lint, deadcode, actionlint, yamllint
just format        # gofumpt -w
just sast          # govulncheck
```

See [CLAUDE.md](CLAUDE.md) for contributor conventions and project constraints.
