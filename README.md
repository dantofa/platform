# dantofa-saas

A small command-line utility built with [Typer](https://typer.tiangolo.com/).

## Install

The package is installed directly from this repository (it is not published to
a package index).

Install it as a tool:

```bash
uv tool install git+https://github.com/dantofa/cli.git
# or: pipx install git+https://github.com/dantofa/cli.git
```

Or run it once without installing:

```bash
uvx --from git+https://github.com/dantofa/cli.git dctl --help
```

To pin a specific version, append a ref, e.g.
`git+https://github.com/dantofa/cli.git@v0.1.0`.

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
