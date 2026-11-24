# Tenant / app namespace template

The paved road for onboarding a downstream app onto a dantofa cluster. It is **not**
reconciled by the platform (no `example-app` namespace is created on every cluster) —
it's a reusable base a downstream repo instantiates, mirroring the `dashboards/`
reference artifact.

## The convention

A namespace opts into platform governance with one label:

```yaml
metadata:
  labels:
    dantofa.dev/tenant: <app-name>
```

That label is the trigger for the base-stack **Kyverno policies**
(`flux/cluster/kyverno-policies/`), which then enforce on the namespace's Pods:

- **restricted Pod Security Standard** (non-root, no privilege escalation, dropped
  capabilities, seccomp `RuntimeDefault`, no host namespaces/paths),
- **resource requests + memory limits** on every container, and
- **no `:latest` image tag** (an explicit tag or digest is required).

Platform namespaces don't carry the label, so nothing above touches the platform's
own (occasionally privileged) components — a tenant is enforced only once it opts in.

To run Terraform (tofu-controller) in the namespace, add a second label,
`dantofa.dev/terraform: enabled`; see the "Terraform" section of SKILLS.md. The tenant
label's value also names the `flux-system` source your `Terraform` objects may use.

## What this template ships

| File | Purpose |
| --- | --- |
| `namespace.yaml` | the `dantofa.dev/tenant`-labelled Namespace |
| `resourcequota.yaml` | caps total CPU/memory/pods/PVCs for the tenant |
| `limitrange.yaml` | per-container default requests/limits + ceiling |
| `networkpolicy.yaml` | denies ingress from other tenants; allows same-namespace + non-tenant (ingress/monitoring); egress open |

## Using it downstream

Copy this directory into your app repo (or reference it remotely), then in
`kustomization.yaml` change `namespace:` and the Namespace name/label to your app and
add your workloads as extra `resources`. Validate with `kubectl kustomize .`.

## Notes / follow-ups
- Enforcement lives in Kyverno (single source of truth). Native Pod Security Admission
  labels are optional defense-in-depth if you want API-server-level enforcement too.
- Egress is intentionally open (apps call external APIs — LLM providers, scrape
  targets); add egress rules per app if you need tighter control.
- Auto-scaffolding these objects from the label alone (a Kyverno `generate` policy)
  is a possible future enhancement once validated on a live cluster.
