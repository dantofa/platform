# dantofa-saas

A small command-line utility built with [Typer](https://typer.tiangolo.com/).

## Install

Installed directly from this repository (not published to a package index),
through either channel.

### With uv / pipx (Python)

```bash
uv tool install git+https://github.com/dantofa/cli.git
# or: pipx install git+https://github.com/dantofa/cli.git
```

Run it once without installing:

```bash
uvx --from git+https://github.com/dantofa/cli.git dctl --help
```

Pin a version by appending a ref, e.g.
`git+https://github.com/dantofa/cli.git@v0.1.0`. This channel installs only
`dctl` — the CLIs it shells out to (kind, flux, docker, git) must already be on
your PATH.

### With Nix (bundles the cluster CLIs)

Run it once:

```bash
nix run github:dantofa/cli -- --help
```

Install into your profile:

```bash
nix profile install github:dantofa/cli
```

The Nix build wraps `dctl` so kind, flux, and docker travel in its closure — no
separate install (a running Docker daemon is still a host prerequisite). Pin a
commit by appending it: `github:dantofa/cli/<rev>`.

Consume it as a flake input:

```nix
inputs.dantofa-cli.url = "github:dantofa/cli";
# per system: dantofa-cli.packages.${system}.default   # the wrapped dctl
```

Or track it from a downstream devbox/flake project; `devbox update` (or `nix
flake update`) locks the latest `master` commit, and `dctl --version` reports it
as `0.0.0.dev<date>+g<sha>`.

## Usage

```bash
$ dctl
Hello, world!

$ dctl --name dantofa
Hello, dantofa!

$ dctl --help
```

## Development

Requires Python >= 3.13. Generic tooling (uv, just, linters, kind/flux/docker,
kubectl, bws) is provided by the flake dev shell, and
[uv](https://docs.astral.sh/uv/) manages the Python dependencies. Enter the
environment with `nix develop` (or [`direnv`](https://direnv.net/)) — this also
installs the pre-commit hook. Copy `.env.example` to `.env` for local secrets
(e.g. the Bitwarden access token); the shell loads it automatically.

Common tasks are run via [`just`](https://github.com/casey/just):

```bash
just run [args]    # run the CLI
just test          # run the test suite
just lint          # ruff, ty, actionlint, yamllint
just format        # ruff format
just sast          # skylos security scan
```

See [CLAUDE.md](CLAUDE.md) for contributor conventions and project constraints.
