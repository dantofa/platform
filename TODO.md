# TODO

## Forks

- Add Saas repository with initial agent framework. Interesting features:
  -- Dev+Ops of RAG apps (ISO chatbot)
  -- Dev+Ops of Cloudflare workers/pages web apps
  -- Dev+Ops of Android/iPhone applications
  -- Dev+Ops of Web scraping/analytics apps for used car markets in CR and VE
  -- Dev+Ops of Web scraping/analytics apps for real state market in CR and VE
  -- Dev+Ops of Web scraping/analytics apps for TikTok sentiment analytics in CR and VE
  -- Dev+Ops of Elixir game server backend

## Platform

- [DONE] Add Trivy deployment manifests
- [DONE] Add CloudNativePG deployment manifests (Zitadel requirement)
- Add Grafana deployment manifests (optional)
- Add Zitadel deployment manifests (optional)

## CLI

- Add support for git repository initialization to cli
- Add operator interface to cli utility

## Tooling

- Adopt [Kyverno Chainsaw](https://kyverno.github.io/chainsaw/)
  to express the `local`/`preview` integration flows as declarative assertion
  tests (bootstrap -> SeaweedFS/Velero up -> BackupStorageLocation Available ->
  backup Completed), replacing the `just verify-backup` shell + `kubectl wait`
  glue. Chainsaw is a CI test runner (not shipped in `dctl`); wire it into the
  `local.yml`/`preview.yml` workflows. Complements the shipped
  `dctl flux kustomization verify` gate, which stays kstatus-based.
