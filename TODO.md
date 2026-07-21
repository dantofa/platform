# TODO

## Wave 0: Production-readiness foundation

- Configure cluster backups (build on the CNPG + Velero operators already deployed)
  - CloudNativePG WAL archiving + daily base backups to DigitalOcean Spaces bucket `dantofa-postgres-backups` (nyc3)
  - Velero daily cluster-object backup to DigitalOcean Spaces bucket `dantofa-velero-backups` (nyc3); excludes PVC data (CNPG owns that)
- Add a restore / DR drill: scheduled verification that both backups actually restore (extend the CI Velero probe from backup-only to backup+restore)
- Deploy the Grafana Stack (metrics, logging, alerting) — the observability backend the deferred control-loop / SLO / accuracy-alert items in ROADMAP.md depend on
- Add cost monitoring + budget alerts for DigitalOcean spend (clusters, LoadBalancers, Spaces); flag orphaned/leftover resources (e.g. preview clusters)
- Validate the Let's Encrypt production TLS path (`--tls-issuer letsencrypt` + Cloudflare DNS-01) against a real DOKS cluster — built but never run
- CI capacity: provision larger / self-hosted runners and re-enable the `local` workflow (currently `workflow_dispatch`-only due to capacity) — also unblocks the restore drill
- Cluster update verification: gate PRs on the _upgrade_ path, not just fresh convergence. Preview only proves a brand-new cluster converges from the PR branch; it never exercises the transition from the running state, so it misses immutable-field changes (Service/StatefulSet/Deployment selectors), `prune: true` deletions of removed manifests, and CRD/chart upgrade breakage. Bootstrap the preview cluster on `master`, repoint its Flux source to the PR HEAD, and re-verify it reconciles cleanly.

## Downstream projects

- Add Saas repository with initial agent framework. Interesting features:
  -- Dev+Ops of RAG apps (ISO chatbot)
  -- Dev+Ops of Cloudflare workers/pages web apps
  -- Dev+Ops of Android/iPhone applications
  -- Dev+Ops of Web scraping/analytics apps for used car markets in CR and VE
  -- Dev+Ops of Web scraping/analytics apps for real state market in CR and VE
  -- Dev+Ops of Web scraping/analytics apps for TikTok sentiment analytics in CR and VE
  -- Dev+Ops of Elixir game server backend
- Add CLI/Operator repository for git repository management

## Platform

- [DONE] Add Trivy deployment manifests
- [DONE] Add CloudNativePG deployment manifests (Zitadel requirement)
- Add Zitadel deployment manifests (optional)
- Create reusable justfile template for common operations in downstream projects (e.g. local/preview/prod clusters)
- Create reusable github actions for common workflows in downstream projects
- Add image security gates
- Add support for GKE/EKS clusters

## Tooling

- Adopt [Kyverno Chainsaw](https://kyverno.github.io/chainsaw/)
  to express the `local`/`preview` integration flows as declarative assertion
  tests (bootstrap -> SeaweedFS/Velero up -> BackupStorageLocation Available ->
  backup Completed), replacing the `just verify-backup` shell + `kubectl wait`
  glue. Chainsaw is a CI test runner (not shipped in `dctl`); wire it into the
  `local.yml`/`preview.yml` workflows. Complements the shipped
  `dctl flux kustomization verify` gate, which stays kstatus-based.
