# CLAUDE.md

Guidance for working in this repository. Read this before adding dependencies,
tooling, or CI.

## What this is

`dantofa-cli` is a Python CLI built with [Typer](https://typer.tiangolo.com/).
The command logic lives in `src/dantofa/cli/main.py`, exposed as a Typer `app`
and launched by `run()`.

It ships two console entry points (`[project.scripts]`), both pointing at
`dantofa.cli.main:run`:

- `dctl` — short, ergonomic name for installed use.
- `dantofa.cli` — so `uvx dantofa.cli` / `pipx run dantofa.cli` resolve without
  `--from` (the package name normalizes to `dantofa-cli`, and the executable is
  named `dantofa.cli`).

## Layout

- `src/dantofa/cli/` — package code. **`dantofa` is a PEP 420 implicit
  namespace package**: it has *no* `__init__.py`. Do not add one. `cli/` is a
  regular package (`__init__.py` present).
- `tests/` — pytest suite using Typer's `CliRunner`.
- Build backend is `hatchling`; the wheel is built from `src/dantofa`.

## The two-tier tooling rule (important)

Dependencies are split by purpose, and new tools must follow the same split:

- **Generic, language-agnostic dev tools live in devbox** (`devbox.json`) —
  e.g. `uv`, `just`, `actionlint`, `yamllint`. These are pinned via
  `devbox.lock` and are what CI uses too, so versions match local dev exactly.
- **Python project tools go through uv** as dev dependencies
  (`[dependency-groups].dev` in `pyproject.toml`) — e.g. `ruff`, `ty`,
  `skylos`, `pytest`, `pre-commit`. Add them with `uv add --dev <tool>`, never
  by hand-editing the dependency list.

Rule of thumb: *if it analyzes or runs the Python project, it's a uv dev
dependency; if it's general-purpose tooling for the environment, it's a devbox
package.*

Runtime dependencies (things the CLI imports at runtime, like `typer`) go in
`[project].dependencies`, added via `uv add <pkg>`.

## Commands — always via `just`

The justfile is the single source of truth for how tools are invoked. Don't
duplicate these commands elsewhere (CI and pre-commit both delegate to them):

- `just run [args]` — run the CLI.
- `just test [args]` — run pytest.
- `just lint` — `ruff check` + `ty check` + `actionlint` + `yamllint`.
- `just format [args]` — `ruff format`.
- `just sast` — skylos security scan (`--danger --secrets --sca`).

When you add or change a tool invocation, edit the relevant `just` target so
CI and pre-commit pick it up automatically.

## Conventions & constraints

- **Type checking is `ty`** (Astral), not mypy/pyright. Tools that assume
  mypy/pyright config (e.g. skylos `--quality`) will report false gaps — don't
  add config to satisfy them.
- **`just sast` is security-scoped**, not a code-quality grab-bag. Keep it on
  `--danger --secrets --sca`; do not add skylos `--all`/`--quality`, which emits
  noisy repo-policy nags unrelated to the source.
- **pre-commit delegates to `just` targets** (`.pre-commit-config.yaml`) so
  command definitions stay in one place. The hook is installed automatically by
  the devbox `init_hook` on shell entry.
- **CI runs through devbox** (`.github/workflows/`): `devbox run -- uv sync
  --locked`, then `devbox run -- just lint` / `just test`. Because it uses
  devbox, CI tooling versions match local. Keep CI steps as thin wrappers over
  `just`.
- **CI forces plain (uncolored) output** via workflow-level `env: { FORCE_COLOR:
  "", NO_COLOR: "1" }`. devbox's pseudo-terminal otherwise makes rich/Typer
  colorize, which mangles logs and breaks substring assertions on `--help`.
  `FORCE_COLOR` must be **empty** — any non-empty value (even `"0"`) still forces
  color, and `FORCE_COLOR` overrides `NO_COLOR`. Tests also strip ANSI
  defensively, so they pass regardless of color.
- Requires **Python >= 3.13**.
- **All repository update operations go through `just update`** — never bump
  pinned versions by hand. Today it runs `ratchet update` to refresh the
  GitHub Actions SHAs; future update operations belong in this target too.
- **GitHub Actions are pinned to full commit SHAs** (supply-chain hardening),
  with a `# ratchet:owner/action@vX` marker that `ratchet` uses to update them.
  Dependabot (`.github/dependabot.yml`) opens weekly PRs to bump them; `just
  update` is the manual path. Keep workflows passing `skylos --danger`: declare
  top-level `permissions: {}` with minimal per-job grants, set
  `persist-credentials: false` on checkout, and `timeout-minutes` on jobs.

## Versioning & releasing

The version is **derived from git tags** by `hatch-vcs` — there is no static
`version` in `pyproject.toml` (it is declared `dynamic`). Do **not** add a
hardcoded version back; the tag is the single source of truth.

- Cut a release by tagging a clean commit: `git tag v1.2.3 && git push --tags`.
  A clean checkout at that tag builds as version `1.2.3`. Pushing a `v*` tag
  also triggers `.github/workflows/release.yml`, which creates a GitHub Release
  with auto-generated notes (`gh release create --generate-notes`).
- Between tags you get dev versions like `1.2.4.dev3+g<hash>`; a dirty working
  tree adds a local suffix. This is expected.
- Release tags must point at a commit reachable from `master` (i.e. a merged,
  gated commit). Git/GitHub can't enforce this natively, so the release
  workflow verifies it with `git merge-base --is-ancestor` and refuses to
  release otherwise. Tag clean `master` commits only.
- `dctl --version` reports the installed package version via
  `importlib.metadata.version("dantofa-cli")` — keep that distribution name in
  sync if the project is ever renamed.
- CI checks out with `fetch-depth: 0` so tags/history are available to
  hatch-vcs. `uv.lock` records the local package as dynamic, so `uv sync
  --locked` does not break when the computed version changes.

## Repository configuration (as code)

Branch protection and repo settings are codified, not clicked. Source of truth:

- `.github/repo-config/ruleset-master.json` — the `master` branch ruleset:
  PR required (0 approvals — raise `required_approving_review_count` to 1 once a
  second reviewer exists), conversation resolution, required `lint`/`test`
  status checks (strict), linear history, squash-only merges, no force-push, no
  deletion.
- `.github/repo-config/repo-settings.json` — repo-level settings rulesets can't
  model: squash-only merge button, auto-delete head branches, auto-merge, and
  squash commit title/message = PR title/body.

Apply **locally** with `just github repo` (idempotent; upserts the ruleset by
name via `gh api`) whenever you change the JSON. It runs under your own `gh auth
login`, which has admin on the `dantofa` org — so no PAT, no CI secret, and no
workflow are needed. This is a manual step: there is no automatic drift
correction, so re-run it after editing the config. Never click the settings in
the UI, or the next apply reverts your change (the JSON wins).

To change protection, edit the JSON and re-apply — never click it in the UI, or
the next apply will revert your change (the JSON wins).

## Before you finish

Run `just lint` and `just test` (or `pre-commit run --all-files`) and make sure
they pass. If you touched dependencies, run `uv sync --locked` to confirm the
lockfile is current — CI fails on a stale lockfile.
