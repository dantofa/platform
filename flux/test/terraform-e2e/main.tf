# Throwaway module for `just cluster verify terraform` (issue #107). It lives in
# flux/ so the platform source carries it on every cluster type (the embedded OCI
# artifact on kind, the GitRepository on DOKS), and the e2e can apply it from a
# tenant namespace with no downstream repo involved.
#
# One small real provider on purpose: it makes the runner download a provider
# from the registry, which `terraform_data` alone would never exercise.
terraform {
  required_providers {
    random = {
      source  = "hashicorp/random"
      version = "3.7.2"
    }
  }
}

resource "random_pet" "name" {}

output "pet" {
  value = random_pet.name.id
}
