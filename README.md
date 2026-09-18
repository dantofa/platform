# dantofa platform

`dctl` — the dantofa platform control CLI (and, in time, its operator). An opinionated bootstrapping tool for
DigitalOcean and local Kubernetes clusters and components.

It deploys a fixed platform toolset via Flux:

- **Base components:** cert-manager, External Secrets Operator, Velero, Kyverno, Trivy Operator, CloudNativePG operator (the operator only — declare your own Postgres `Cluster`).
- **Governance:** baseline Kyverno policies (`flux/cluster/kyverno-policies/`) enforce restricted Pod Security, required resource requests/limits, and no `:latest` on any namespace labelled `dantofa.dev/tenant` — the opt-in an app takes via the `tenant-namespace/` onboarding template (namespace + quota + limits + isolation NetworkPolicy). Platform components are unaffected (unlabelled).
- **Metrics, log & trace collection (all clusters):** Grafana Alloy + node-exporter + kube-state-metrics (k8s-monitoring) and the prometheus-operator CRDs — scraping to an always-on local Prometheus store, tailing pod logs to an always-on local Loki store, and receiving app OTLP traces into an always-on local Tempo store (all short retention). A cost-exporter publishes the cluster's DO price constants (node/storage/LB/transfer, priced from the DO Sizes API at bootstrap) as `dantofa_*` metrics for a central cost dashboard.
- **DOKS:** Cloudflare Tunnel controller by default (outbound-only, no LoadBalancer); with `--dolb`, Traefik + external-dns behind a DO LoadBalancer instead.
- **kind (local):** Traefik ingress on a NodePort Service published to the host's real `:80`/`:443` (kind `extraPortMappings`) — with `*.local.dantofa.dev` resolving to `127.0.0.1`, host tools reach the cluster at its real URL with no port-forward and no per-tool resolver flags. No Cloudflare/tunnel, so local test traffic stays on the loopback. Plus SeaweedFS.
- **With `--grafana-cloud`:** a curated metrics subset (including the `dantofa_*` cost series) is forwarded to Grafana Cloud, labelled by `cluster`/`env`. Visualization is Grafana-Cloud-only — the platform ships data, not dashboards; dashboards/alerts are managed centrally (see `dashboards/` for the reference cost dashboard, ready for Grafana Cloud Git Sync or Terraform).

## Install

Distributed as a **Nix flake** (not published to a package index).

Run it once:

```bash
nix run github:dantofa/platform -- --help
```

Pin a commit by appending it: `github:dantofa/platform/<rev>`.

Consume it as a flake input:

```nix
inputs.dantofa-platform.url = "github:dantofa/platform";
# per system: dantofa-platform.packages.${system}.default   # the wrapped dctl
```

## Usage

```bash
$ dctl --help
$ dctl --version
```

## Just

The project also ships a reusable [`just`](https://github.com/casey/just)
module — `cluster.just` — that downstream projects can import to operate any
cluster they provision:

```bash
just cluster debug                                     # snapshot cluster + Flux state
just cluster verify health|backup|restore|image-scan   # verify any cluster, whatever provisioned it
just cluster local  create|bootstrap|verify|delete|test|list  # kind cluster lifecycle
just cluster local  ca                                 # export the local TLS trust anchor
just cluster local  chromium|playwright|curl [args]    # a browser/curl wired to the cluster ingress
just cluster doks   create|bootstrap|connect|delete|list      # DOKS cluster lifecycle
```

Two ways to consume it, both rev-pinned via your lockfile:

- **devbox** — `include` the plugin:

  ```json
  { "include": ["github:dantofa/platform?dir=devbox"] }
  ```

- **Nix flake** — the module, base ignore list, and agent skill are flake outputs
  (`packages.cluster-just`, `packages.trivyignore-base`, `packages.skills`);
  materialize them into your dev shell.

Either way `.just/cluster.just` lands in your project — with **devbox the plugin also
ensures your justfile imports it** (creating a justfile if you have none, appending the
import if you do; idempotent either way), so you only write your own compose recipe. On
the bare-flake path, add the import yourself. The platform's agent skill lands in
`.claude/skills/dantofa-platform/SKILL.md` so Claude agents on your project discover the
platform's conventions/infrastructure and reuse them (see [`SKILLS.md`](SKILLS.md)):

```just
import '.just/cluster.just'

# Your own compose recipe over the shared primitives:
e2e:
  just cluster local create
  just deploy-my-app
  just test-my-app
  just cluster local delete
```

Config comes from the environment (`DCTL`, `BASE_DOMAIN`, `TRIVYIGNORE_BASE` /
`TRIVYIGNORE_LOCAL`, and for the browser recipes `CHROMIUM` / `PLAYWRIGHT` /
`INGRESS_PORT`) with sane defaults.

## Development

Requires [Nix](https://nixos.org/) with flakes. Enter the dev shell with `nix develop` (or
[`direnv`](https://direnv.net/)) — this also installs the pre-commit hook. Copy
`.env.example` to `.env` for local secrets (e.g. the Bitwarden access token); the
shell loads it automatically.

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
