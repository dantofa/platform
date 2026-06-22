# TODO

## Add a JSON lint/format check to `just lint`

Cover the hand-edited JSON that drives live config (`.github/repo-config/*.json`,
`devbox.json`) so typos are caught at lint time, not at `gh api` apply time.

- **Preferred:** `biome` via devbox (single binary, no Node), wired into the
  `just lint` target as `biome check .github/repo-config` (or
  `biome format --check`). Per the two-tier rule, biome is a generic tool → devbox.
  Note: biome will reformat inline objects like `{ "type": "deletion" }` onto
  multiple lines — a harmless one-time reformat.
- **Lighter alternative:** `jq -e .` syntax-only gate (no formatting opinions).
- **Skip:** JSON-schema validation — no readily available local schema for
  GitHub rulesets, not worth the weight.

pre-commit already delegates to `just`, so wiring it into `just lint` covers it
there too.
