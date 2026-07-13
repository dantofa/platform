# TODO

- **Chainsaw integration tests.** Adopt [Kyverno Chainsaw](https://kyverno.github.io/chainsaw/)
  to express the `local`/`preview` integration flows as declarative assertion
  tests (bootstrap -> SeaweedFS/Velero up -> BackupStorageLocation Available ->
  backup Completed), replacing the `just verify-backup` shell + `kubectl wait`
  glue. Chainsaw is a CI test runner (not shipped in `dctl`); wire it into the
  `local.yml`/`preview.yml` workflows. Complements the shipped
  `dctl flux kustomization verify` gate, which stays kstatus-based.
