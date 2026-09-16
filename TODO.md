# TODO

## Production-readiness foundation

- [DONE] Cluster update verification: gate PRs on the _upgrade_ path, not just fresh convergence.
- [DONE] Configure cluster backups (Velero cluster-object backup + restore drill). The platform runs no database of its own, so **database recovery stays out of platform scope**: a downstream that owns a `Cluster` owns its PITR. The CloudNativePG **operator** was dropped in #24 for want of a consumer and restored in #47 — self-hosted Zitadel needs Postgres, and CNPG's CRDs are cluster-scoped, so leaving the operator to downstream makes whichever product ships first the de-facto owner of a shared dependency. The operator only: still no `Cluster` CRs, still no platform-owned database.
  - Velero must not back up a CNPG database: the `cnpg-exclude-from-velero-backup` Kyverno policy labels the `Cluster` CR, its instance pods and its PVCs `velero.io/exclude-from-backup`, so neither a live data directory is copied nor an empty PVC restored over a downstream's PITR path.
  - [DONE] Velero daily cluster-object backup to DigitalOcean Spaces bucket
- [DONE] Add a restore / DR drill: scheduled verification that both backups actually restore (extend the CI Velero probe from backup-only to backup+restore)
- [DONE] Deploy the observability stack — the backend the deferred control-loop / SLO / accuracy-alert items in ROADMAP.md depend on. Refactored from the initial all-in-one `kube-prometheus-stack` into a layered model: **unconditional collection + local retention (base)**, **opt-in visualization server (in-cluster Grafana and/or Grafana Cloud)**. Collection is Alloy-centric (scrape once, `remote_write` to a destinations list: local Prometheus always, remote opt-in with allowlist + `cluster`/`env` labels). Grafana is operator-managed so dashboards/datasources/alerts are CRDs that reconcile into an in-cluster **or** external (Grafana Cloud) instance via `instanceSelector`. Single cloud stack, clusters distinguished by labels; cost controlled by egress allowlist (a naive ship-everything setup was ~4× cluster cost).
  - [DONE] Deploy local Prometheus agent
  - [DONE] Add Grafana Cloud support as an opt-in remote destination
  - [DONE] Add a cluster cost metrics to Prometheus
  - [DONE] Add log collection to base monitoring agents
  - [DONE] Add trace collection to base monitoring agents
- [DONE] Add cost monitoring + budget alerts for DigitalOcean spend (clusters, LoadBalancers, Spaces); flag orphaned/leftover resources
- [DONE] Validate the Let's Encrypt TLS path (`--tls-issuer letsencrypt` + Cloudflare DNS-01) against a real DOKS cluster — built but never run
  - [DONE] Add a staging issuer variant: `--tls-issuer staging` (Let's Encrypt staging ACME server, same Cloudflare DNS-01 solver) so preview and prod select the CA per cluster; production LE stays on prod only, and its rate limits stay off the preview path. Both ACME issuers share one reusable layer, split into two ordered reconcile roots — `cloudflare-api-token` (the Cloudflare DNS-01 token ExternalSecret) then `letsencrypt` (the ClusterIssuer, `dependsOn` the token so it never races an absent secret) — with the issuer name + ACME endpoint substituted per cluster via `${tls_issuer}`/`${acme_server}` cluster-vars.
- [DONE] Add security monitoring gates
  - [DONE] Add Trivy deployment manifests
  - [DONE] Add image security gate
- [IN PROGRESS] Add base application policies
  - [DONE] Add base set of Kyverno governance policies
    - Add PodDisruptionBudget policy to the base platform so stacks survive node drains/upgrades

## UX

- [DONE] Create reusable justfile template for common operations in downstream projects (e.g. local/preview/prod clusters)
- [DONE] Add justfile plugin configuration for downstream project management
- [DONE] Move local tests from Cloudflare tunnels to local ingresses
- [DONE] Build mechanism for accessing local cluster ingresses
- [DONE] Add SKILLS.md file to devbox plugin so Claude agents on downstream projects can effectively use the primitives implemented here
- [DONE] Give the local (kind) cluster a TLS cert whose SANs actually match `${base_domain}`, so `https://` can be verified rather than only ignored
- [DEFERRED] Make the local cert publicly trusted, so tools work with TLS encryption out of the box. Only a real ACME cert stored in BWS achieves this
- Add a tenant/app-namespace onboarding template (namespace + quota + limits + isolation NetworkPolicy) — deferred until a downstream app informs its shape

## Monitoring

- Add alerting on the collected telemetry (backup age/failure, cert expiry, node/disk pressure, cost budget, app SLOs), wired to central Grafana Cloud alerting

## Platform

- [DEFERRED] Add support for GKE/EKS clusters
