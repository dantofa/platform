---
name: dantofa-platform
description: >-
  Conventions, infrastructure, and reusable primitives of the dantofa platform (the
  base cluster stack every dantofa-provisioned cluster runs). Use when building or
  changing deployment artifacts for a project that runs on a dantofa cluster (local
  kind, DOKS preview/prod): Flux/Kubernetes manifests, Ingress, TLS, secrets, cluster
  provisioning, CI, or observability wiring. Covers domain/ingress/GitOps/secret
  conventions, the local/preview/prod cluster lifecycle, local app access, and the
  required logging/metrics/traces model — so you reuse the platform instead of
  rebuilding what it already provides.
---

# dantofa platform — downstream deployment skill

The dantofa platform (`dctl` + a Flux GitOps tree) provisions Kubernetes clusters and a
fixed base stack. Your downstream project deploys its app **onto** a platform cluster —
so **do not rebuild what the platform already provides**. This skill is the map: match
your task to a section, follow the convention, reuse the primitive.

## What the platform already provides (never duplicate these)

- **Provisioning**: `dctl` creates DOKS + local (kind) clusters, installs Flux, and
  reconciles the shared base stack.
- **Base stacks on every cluster**: cert-manager, External Secrets Operator (ESO),
  Velero (cluster backups), Kyverno (+ baseline governance policies), Trivy (image CVE
  scanning + gate), prometheus-operator CRDs, an always-on local Prometheus (metrics),
  local Loki (logs), local Tempo (traces), Grafana Alloy collectors, a cost-exporter, and
  the CloudNativePG operator (see "Postgres").
- **Ingress**: Cloudflare Tunnel (DOKS default) or Traefik + external-dns behind a DO
  LoadBalancer (`--dolb`); a local-only Traefik on kind, published on the host's real
  `:80`/`:443`.
- **TLS**: cert-manager — selfsigned by default, Let's Encrypt via Cloudflare DNS-01 on
  DOKS, and an in-cluster CA on kind. See "Which TLS issuer applies where".
- **Secrets**: Bitwarden via ESO.
- **Observability collection**: Alloy scrapes metrics/logs/traces to the local stores
  and (opt-in) forwards a curated subset to Grafana Cloud.

So: no app-shipped Prometheus/Loki/Tempo/Grafana, no app-shipped ingress controller or
cert-manager, no bespoke log/metric shipping. Emit signals the standard way and the
platform collects them.

## Project setup (devbox plugin)

Add the platform's **devbox plugin** (`github:dantofa/platform`) to your `devbox.json`.
It puts `dctl` + the CLI toolchain on `PATH` and, on every `devbox` shell init,
**materializes generated files into the project tree**, rev-pinned to the platform
version by `devbox.lock`:

- `.just/cluster.just` + `.just/.trivyignore-base` — the shared cluster-ops module your
  justfile `import`s. The hook also **ensures that import exists**: it creates a
  `justfile` if your project has none, or appends the import if it has one (matching on
  the import path, so a variant you wrote yourself is left alone). Idempotent, like the
  `.gitignore` entries.
- `.claude/skills/dantofa-platform/SKILL.md` — this skill (Claude Code auto-loads it).

Because they are **regenerated every init and pinned by `devbox.lock`**, they are build
output: **don't commit them** — a committed copy silently drifts from the pin. You don't
have to manage this by hand: the plugin's `init_hook` **adds their ignore entries to your
`.gitignore` for you** (idempotent, append-if-missing). Commit the pin, not its output.

- **Commit**: `.gitignore` (now carrying those entries), `devbox.json`, `devbox.lock`.
- **Plugin-managed ignores**: `.just/`, `.claude/skills/dantofa-platform/`, `.devbox/`,
  `.claude/settings.local.json`.
- It ignores the **specific** generated `.claude/` paths — **not** the whole `.claude/`
  directory — so a hand-authored `.claude/settings.json` or your own
  `.claude/skills/<name>/` stays committable.

## Domain conventions

- Every cluster has a `base_domain` (Flux cluster-var `${base_domain}`) — e.g.
  `local.dantofa.dev`, `preview.dantofa.dev`, `dantofa.dev`.
- **Route apps by PATH on `${base_domain}`, not per-app subdomains**: serve your app at
  `${base_domain}/<app>` (like echo at `/echo`, Grafana at `/grafana`). A per-app
  subdomain (`app.local.dantofa.dev`) sits two levels under the zone apex, outside
  Cloudflare Universal SSL (`*.dantofa.dev`); the apex + one level is covered.
- Never hardcode a hostname — use `${base_domain}` + a path.

## Ingress definitions

Plain `networking.k8s.io/v1` Ingress, **omitting `ingressClassName`** so the cluster's
default IngressClass routes it — one manifest works on every cluster type. Use
`host: ${base_domain}` (Flux substitutes it), a `/<app>` path prefix, and **no `tls:`
block** (the controller serves the default cert). Model on `flux/echo/ingress.yaml`:

```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: myapp
  namespace: myapp
spec:
  rules:
    - host: ${base_domain}
      http:
        paths:
          - path: /myapp
            pathType: Prefix
            backend:
              service:
                name: myapp
                port:
                  number: 80
```

## Flux GitOps conventions

- The platform reconciles from ONE source (a `GitRepository` on DOKS, an
  `OCIRepository` on kind), registered at bootstrap. Downstream layers its **own
  payload** as an additional reconcile root pointed at the same source.
- A **reconcile root** is a Flux `Kustomization` CR with `wait: true`, a
  source-agnostic `sourceRef` (`${source_kind}`/`${source_name}`), and
  `postBuild.substituteFrom` the `cluster-vars` ConfigMap. Add `dependsOn` to order
  your root after the platform layers your app needs (e.g. `ingress`, `eso-config`).
- **cluster-vars** (namespace `flux-system`) carries the per-cluster values you
  substitute as `${…}`. Use them; never hardcode:
  `base_domain`, `cluster_name`, `env` (`local`/`preview`/`prod`), `storage_class`,
  `dns_zone`, `tls_issuer`, `acme_server`, `source_kind`/`source_name`, and the
  `node_hourly_price` / `node_transfer_gib` / `block_storage_gib_hourly` /
  `lb_hourly_price` cost constants.
- PVCs must use `storageClassName: ${storage_class}` (→ `do-block-storage` on DOKS,
  `standard` on kind), never a hardcoded class.
- **A private payload repo** — if your manifests live somewhere the cluster cannot read
  anonymously, register your own source with a credential instead of making the repo
  public:

  ```sh
  # HTTPS + a forge PAT: dctl mints the credential Secret (default <name>-auth)
  dctl flux source create app --type git --url https://github.com/org/app \
      --revision main --token "$GITHUB_TOKEN"        # or $DCTL_SOURCE_TOKEN
  # ...or reference a Secret you created yourself (the only option for SSH keys):
  flux create secret git app-deploy-key --url ssh://git@github.com/org/app
  dctl flux source create app --type git --url ssh://git@github.com/org/app \
      --revision main --secret-ref app-deploy-key
  ```

  The same flags work for `--type oci` against a private registry (dctl mints a
  dockerconfigjson pull secret there). The auth flags are **declarative**: `source
  create` rewrites the whole source, so a later call without them leaves it
  unauthenticated — pass them on every invocation.

## Cluster lifecycle

Use `dctl` directly, or the shared `cluster.just` module — import it (materialized to
`.just/cluster.just` by the devbox plugin, or the flake output `packages.cluster-just`)
and compose YOUR own end-to-end flow over its primitives (the module never calls back
into your recipes):

- **Local (kind)** — `just cluster local create` (create + bootstrap) / `verify` /
  `delete` / `test`. Ingress is a local Traefik NodePort published on the host's real
  `:80`/`:443`; no Cloudflare, no tunnel.
- **Preview (DOKS, ephemeral)** —
  `dctl … cluster bootstrap --base-domain preview.dantofa.dev [--grafana-cloud]`.
  Ingress is the Cloudflare Tunnel, which terminates TLS at the edge, so **no
  `--tls-issuer` is needed** (passing one without `--dolb` is inert and warns). Treat
  as disposable.
- **Production (DOKS)** — create with `--ha` and a node size, then
  `dctl … cluster bootstrap --base-domain <domain> --dolb --tls-issuer letsencrypt --env prod [--grafana-cloud]`.
  Traefik + external-dns behind a DO LoadBalancer, a real Let's Encrypt cert.

Both DOKS flows are also available as `cluster.just` recipes —
`just cluster doks create|bootstrap|connect|delete|list` — which act on
`${DOKS_CLUSTER:-doks}` with `${DOKS_BASE_DOMAIN:-doks.dantofa.dev}`, so export
`DOKS_CLUSTER` to point them at a real cluster. Two deliberate differences from the
local namespace: there is **no `cluster doks test`** (the local one deletes the
cluster it made; on billed infrastructure that belongs in an explicit workflow), and
`cluster doks create` does **not** bootstrap, since a DOKS cluster is usually
bootstrapped more than once with different per-environment flags.

Verification is **cluster-type-agnostic**: `just cluster verify health` (all nodes
Ready + the whole GitOps tree reconciled), plus `backup|restore|image-scan` and
`just cluster debug`, all act on whatever `$KUBECONFIG` points at. So a downstream
e2e differs between local and DOKS by only the lifecycle line — connect, then run
the same gates.

A downstream e2e is your own recipe: `create → deploy your app → test → delete`.

### Which TLS issuer applies where

`--tls-issuer` is a **DOKS-only** flag, and even there it only takes effect with
`--dolb` — it names the issuer of the _Traefik default cert_, and without a
LoadBalancer ingress there is no such cert to issue. It defaults to `selfsigned`.

| Cluster shape                         | What signs the cert clients see                                        | `--tls-issuer`                              | What a client must do                                                |
| ------------------------------------- | ---------------------------------------------------------------------- | ------------------------------------------- | -------------------------------------------------------------------- |
| **kind (local)**                      | `local-ca` — a cert-manager CA minted in-cluster                       | not accepted                                | export the CA (`just cluster local ca`), or use `http://`            |
| **DOKS, default (Cloudflare Tunnel)** | Cloudflare's edge cert (publicly trusted)                              | **ignored** — warns if set to a non-default | nothing                                                              |
| **DOKS `--dolb`, preview**            | cert-manager `selfsigned` ClusterIssuer, behind Cloudflare **Full**    | `selfsigned` (default)                      | nothing — the edge does not verify the origin cert                   |
| **DOKS `--dolb`, production**         | Let's Encrypt via Cloudflare DNS-01, behind Cloudflare **Full strict** | `letsencrypt`                               | nothing — publicly trusted                                           |
| **DOKS `--dolb`, ACME dry-run**       | Let's Encrypt **staging** CA via DNS-01                                | `staging`                                   | trust the LE staging root — staging certs are _not_ publicly trusted |

Note that `selfsigned` and the local `local-ca` are not the same mechanism, despite
both being self-signed in origin: `selfsigned` issues a self-signed **leaf** (fine
behind Cloudflare Full, which does not verify the origin), while `local-ca` mints a
**CA** whose leaves rotate under one stable, exportable trust anchor.

## Accessing local apps (kind)

**Use the real URL. Any tool. No routing flags.** The local cluster publishes its
Traefik ingress on the host's real `:80`/`:443` (kind `extraPortMappings` → a NodePort
Service), and `local.dantofa.dev` + `*.local.dantofa.dev` resolve to `127.0.0.1` via a
wildcard DNS record. So `${base_domain}/myapp` works from curl, a browser, Playwright,
an HTTP client in a test, or any CLI you happen to be holding — nothing has to be told
where the cluster is, and there is no port-forward to babysit:

```
curl http://${base_domain}/myapp
```

Your app needs no DNS work either: declare an Ingress for `<app>.${base_domain}` (or a
path on `${base_domain}`) and the wildcard record already points at it.

### TLS: export the local CA once, then point tools at it

The local cluster runs its **own CA**. cert-manager mints a self-signed root
(`local-ca`, in `flux/local/ca`) and issues Traefik's default cert from it with the
real SANs — `local.dantofa.dev` and `*.local.dantofa.dev`. Unlike a stock Traefik
cert, this one can actually be **verified** rather than only ignored.

Export the trust anchor once per cluster:

```
just cluster local ca      # writes ./.local.pem and prints the exports
export SSL_CERT_FILE=$PWD/.local.pem CURL_CA_BUNDLE=$PWD/.local.pem \
       NODE_EXTRA_CA_CERTS=$PWD/.local.pem
```

Re-run it after recreating the cluster — the CA is minted per cluster, so a fresh
cluster means a fresh anchor. Leaf certs rotating (every 90d) does **not** stale it.

**How each stack consumes the anchor.** The env vars cover a lot, but not
everything — some stacks ignore them and need an explicit argument. Verified against
a live local cluster:

| Tool / runtime                          | How it takes the CA                                                                                                                                                         |
| --------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| curl                                    | `SSL_CERT_FILE` **or** `CURL_CA_BUNDLE` (both work)                                                                                                                         |
| Go `net/http`                           | `SSL_CERT_FILE`                                                                                                                                                             |
| Node `fetch` / undici                   | `NODE_EXTRA_CA_CERTS`                                                                                                                                                       |
| openssl `s_client`                      | `-CAfile <pem>`                                                                                                                                                             |
| wget                                    | `--ca-certificate=<pem>` — it does **not** read `SSL_CERT_FILE`                                                                                                             |
| Python stdlib (`urllib`, `http.client`) | explicit `ssl.create_default_context(cafile=…)` — it reads **neither** `SSL_CERT_FILE` nor `REQUESTS_CA_BUNDLE`, because `create_default_context()` loads the system bundle |
| Python `requests`                       | `REQUESTS_CA_BUNDLE` (or `verify="<pem>"`)                                                                                                                                  |
| Python `httpx`                          | `verify="<pem>"`                                                                                                                                                            |
| git                                     | `GIT_SSL_CAINFO`                                                                                                                                                            |

So: set the three env vars for the stacks that honour them, and pass the PEM
explicitly in Python and wget. Either way there is **one anchor** — no per-tool
trust configuration beyond naming the same file.

**Plain `http://` still needs nothing at all.** Vanilla Ingresses (no `tls:` block)
serve on the `web` (:80) entrypoint, so `http://${base_domain}/...` skips TLS
entirely. Prefer it unless you are testing something TLS-specific.

**Browsers are the exception.** Chromium uses its own NSS store and ignores all of
the above. Either import the CA once:

```
certutil -d sql:$HOME/.pki/nssdb -A -t 'C,,' -n dantofa-local -i .local.pem
```

or keep using `--ignore-certificate-errors` / Playwright's `ignoreHTTPSErrors: true`.

**Fallback — skipping verification.** If you have not exported the CA (or are in a
throwaway shell), every stack has a skip-verify switch:

| Tool / runtime              | How to skip verification                                                  |
| --------------------------- | ------------------------------------------------------------------------- |
| curl                        | `-k` / `--insecure`                                                       |
| wget                        | `--no-check-certificate`                                                  |
| HTTPie                      | `--verify=no`                                                             |
| Python `requests` / `httpx` | `verify=False`                                                            |
| Python `urllib`             | `context=ssl._create_unverified_context()`                                |
| Node `fetch` / undici       | `NODE_TLS_REJECT_UNAUTHORIZED=0` (process-wide; no per-request option)    |
| Node `axios`                | `httpsAgent: new https.Agent({ rejectUnauthorized: false })`              |
| Go `net/http`               | `&http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}` |
| Chromium / Chrome           | `--ignore-certificate-errors`                                             |
| Playwright                  | `ignoreHTTPSErrors: true` (context or `playwright.config`)                |
| Puppeteer                   | `acceptInsecureCerts: true` (older: `ignoreHTTPSErrors: true`)            |
| grpcurl                     | `-insecure`                                                               |
| k6                          | `insecureSkipTLSVerify: true`                                             |
| git                         | `GIT_SSL_NO_VERIFY=1`                                                     |

**Scope — local only.** The CA and these flags belong to the local cluster. Never let
either reach a config path that also runs against **preview or production** (both get
real Let's Encrypt certs, where a skip-verify flag would silently mask a broken
chain). Keep them in a local-only profile or on the command line — not in shared
client config or the default test setup.

### Convenience wrappers

Thin — they add the cert flag and nothing else. Calling the tool directly is equally
fine:

- `just cluster local curl https://${base_domain}/myapp` — curl with `--insecure`.
- `just cluster local chromium https://${base_domain}/myapp` — Chromium with the cert
  flag and a throwaway profile.
- `just cluster local playwright test` — exports `BASE_URL`; set
  `baseURL: process.env.BASE_URL` and `ignoreHTTPSErrors: true` in your
  `playwright.config`, and pin `@playwright/test` to the flake's `playwright-driver`.

Env knobs: `CHROMIUM`, `PLAYWRIGHT`.

**Caveat — in-cluster callers.** The DNS record answers `127.0.0.1` for everyone,
including pods. A pod calling `https://${base_domain}/...` hits _its own_ loopback,
not the ingress. From inside the cluster, call the Service directly
(`http://<svc>.<ns>.svc.cluster.local`) rather than the public hostname.

## Logging

Automatic. Alloy tails every pod's stdout/stderr → local Loki (and Grafana Cloud when
`--grafana-cloud`). **Just log to stdout/stderr** — no per-app log shipping, no
sidecar. Logs are labelled by `cluster`/`env`; query them in Grafana Cloud (Loki).

## Metrics

Cluster/node/pod metrics are collected automatically. Expose **your app's** metrics one
of two ways (both auto-scraped by Alloy — do NOT deploy your own Prometheus):

- A **`ServiceMonitor`/`PodMonitor`** (the prometheus-operator CRDs are installed) — the
  richer path (per-target relabeling, auth, metric filtering); or
- **Annotation autodiscovery** on the pod:
  `k8s.grafana.com/scrape: "true"` + `k8s.grafana.com/metrics.portNumber: "<port>"`
  (path defaults to `/metrics`).

## Traces — REQUIRED

Every app **must** emit **OpenTelemetry traces**. Send **OTLP** to the in-cluster Alloy
receiver (the `applicationObservability` collector); it forwards traces → local Tempo
(and Grafana Cloud), while app OTLP metrics/logs fan out to Prometheus/Loki.

- Point your SDK at the alloy-receiver Service in the `monitoring` namespace — OTLP
  gRPC `:4317` or HTTP `:4318` (confirm the Service name with
  `kubectl -n monitoring get svc`; set `OTEL_EXPORTER_OTLP_ENDPOINT`).
- For LLM/RAG/agent apps, follow the **OpenTelemetry GenAI semantic conventions** (model,
  provider, input/output tokens, etc.) — the platform's per-query cost/latency/accuracy
  analytics are built on those span attributes, so they are not optional.

## Governance (tenant namespaces)

Label your namespace `dantofa.dev/tenant: <app>` to opt into the platform's Kyverno
baseline. It then **enforces** on your Pods: **restricted Pod Security** (run non-root,
drop all capabilities, seccomp `RuntimeDefault`, no host namespaces/paths), **cpu +
memory requests and a memory limit** on every container, and **no `:latest`** image tag
(use a tag or digest). Non-compliant Pods are rejected at admission — design images and
manifests to comply.

## Postgres

The platform ships the **CloudNativePG operator** and its **barman-cloud plugin** — the
operators only. Do not install either yourself: both register cluster-scoped CRDs, so a
second copy collides with everyone else's on a shared cluster. Declare your own `Cluster`
CR in your own reconcile root, the same split as prometheus-operator (platform ships the
CRDs, you ship the `ServiceMonitor`).

The barman-cloud plugin is what makes PITR possible at all — CNPG 1.26+ moved backup
support out of the core operator, so an operator without it can run Postgres but cannot
archive WAL. The platform ships no `ObjectStore`, so you still point your database at a
bucket yourself.

**Your database is not in the Velero backup, and that is enforced.** The
`cnpg-exclude-from-velero-backup` Kyverno policy labels your `Cluster` CR, its instance
pods and its PVCs `velero.io/exclude-from-backup`, so Velero skips them entirely. Two
reasons, both measured rather than assumed:

- Velero's file-system backup would otherwise copy a **live** data directory — a large,
  crash-inconsistent artifact that looks like a database backup and is not one.
- Restoring those objects is worse than not having them. Velero cannot capture the volume
  contents, but it *can* restore a PVC object with CNPG's `cnpg.io/pvcStatus: ready`
  annotation intact. The operator then reads an empty volume as an initialized PGDATA,
  starts an instance against it, and the pod crash-loops forever (`unable to move 'pg_wal'
  directory to the attached volume`) with the `Cluster` stuck in *Waiting for the instances
  to become active*. It never self-heals.

**What a namespace restore gives you** is everything around the database: your app, its
Services, and the CNPG-generated Secrets — the CA, the server certificate and the
application credentials — so connection strings and client trust survive unchanged. The
database itself is simply absent. Bring it back by letting Flux re-apply your `Cluster`
with a `bootstrap.recovery` stanza pointing at your own WAL archive; nothing needs to be
deleted or untangled first.

What that leaves you: **database recovery is yours**. If you need PITR, configure CNPG's
own WAL archiving and base backups against object storage you own — the platform's
`backup-credential` is scoped to the Velero namespace and is not yours to use. Without
that, a destroyed cluster means a lost database, however green the Velero backups look.

The exclusion is by object, not by volume name, so it covers volumes you add later —
`walStorage`, `tablespaces:` (`tbs-<name>`) and anything a future CNPG version mounts. It
uses Kyverno's add-if-absent anchor, so setting `velero.io/exclude-from-backup: "false"`
yourself (on the `Cluster`, or via its `inheritedMetadata`) still wins — at which point the
two failure modes above are yours to manage.

## Secrets

Source-of-truth secrets (API tokens, credentials) come from **Bitwarden via ESO**: an
`ExternalSecret` against the `bitwarden` ClusterSecretStore. Disposable, cluster-local
secrets (e.g. a generated password) use the ESO `Password` generator — no Bitwarden
entry. Never commit secrets; never place the DigitalOcean token in a cluster.
