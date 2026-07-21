# TODO

## Wave 0: Production-readiness foundation

- [DONE] Cluster update verification: gate PRs on the _upgrade_ path, not just fresh convergence.
- Configure cluster backups (build on the CNPG + Velero operators already deployed)
  - [DONE] Velero daily cluster-object backup to DigitalOcean Spaces bucket
  - CloudNativePG WAL archiving + daily base backups to DigitalOcean Spaces bucket
- [DONE] Add a restore / DR drill: scheduled verification that both backups actually restore (extend the CI Velero probe from backup-only to backup+restore)
- Deploy the Grafana Stack (metrics, logging, alerting) — the observability backend the deferred control-loop / SLO / accuracy-alert items in ROADMAP.md depend on
- [DONE] Add cost monitoring + budget alerts for DigitalOcean spend (clusters, LoadBalancers, Spaces); flag orphaned/leftover resources (e.g. preview clusters)
- Validate the Let's Encrypt production TLS path (`--tls-issuer letsencrypt` + Cloudflare DNS-01) against a real DOKS cluster — built but never run
- CI capacity: provision larger / self-hosted runners and re-enable the `local` workflow (currently `workflow_dispatch`-only due to capacity) — also unblocks the restore drill

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
