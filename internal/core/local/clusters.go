// Package local holds the framework-free application logic for local (kind)
// development clusters. The kind/docker/flux/git surface is reached through the
// LocalClusterAPI interface, satisfied by the clients adapter.
package local

import (
	"context"
	"fmt"
)

// Defaults for local clusters and the OCI push flow.
const (
	DefaultClusterName  = "local"
	DefaultRegistryName = "kind-registry"
	DefaultRegistryPort = 5001
	DefaultArtifactName = "local"
	DefaultArtifactTag  = "latest"
	// DefaultArtifactPath is the project root: the OCI artifact mirrors the repo
	// (so the flux/ prefix is present and manifest paths match the git-sourced
	// DOKS flow); the client whitelists just flux/ so it stays lean and never
	// packages source, secrets, or build outputs.
	DefaultArtifactPath = "."
	// DefaultWorkerNodes is the worker count a cluster gets unless overridden
	// (control-plane + 3 workers = 4 nodes).
	DefaultWorkerNodes = 3
	// DefaultControlPlaneNodes is the control-plane count unless overridden; >1
	// makes kind stand up an HA control plane behind a load balancer.
	DefaultControlPlaneNodes = 1

	// RegistryInClusterPort is the port the kind nodes reach the OCI registry
	// on (the in-cluster address an OCIRepository pulls from).
	RegistryInClusterPort = 5000
)

// InClusterArtifactURL is the oci:// URL an in-cluster OCIRepository uses to pull
// the artifact `PushArtifact` publishes (without the tag). It resolves the
// registry's IP on the kind network: cluster DNS (CoreDNS) cannot resolve the
// registry's docker container name, so a Flux pod must pull by IP.
func InClusterArtifactURL(ctx context.Context, client LocalClusterAPI, registryName, name string) (string, error) {
	ip, err := client.RegistryIP(ctx, registryName)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("oci://%s:%d/%s", ip, RegistryInClusterPort, name), nil
}

// LocalClusterAPI is the local-cluster surface this package depends on.
type LocalClusterAPI interface {
	List(ctx context.Context) ([]string, error)
	Create(ctx context.Context, name, registryName string, registryPort, controlPlanes, workers int) error
	Delete(ctx context.Context, name string) error
	GetKubeconfig(ctx context.Context, name string) (string, error)
	RegistryIP(ctx context.Context, registryName string) (string, error)
	GitProvenance(ctx context.Context) (source, revision string, err error)
	PushArtifact(ctx context.Context, url, path, source, revision string) error
	ReconcileSource(ctx context.Context, name string) error
}

// LocalClusterExistsError is returned when a cluster with the name already exists.
type LocalClusterExistsError struct{ Name string }

func (e *LocalClusterExistsError) Error() string {
	return fmt.Sprintf("a local cluster named %q already exists", e.Name)
}

// LocalClusterNotFoundError is returned when no local cluster matches the name.
type LocalClusterNotFoundError struct{ Name string }

func (e *LocalClusterNotFoundError) Error() string {
	return fmt.Sprintf("no local cluster named %q", e.Name)
}

// CreateResult reports the push endpoints of a freshly-created cluster.
type CreateResult struct {
	Name              string `json:"name"`
	Registry          string `json:"registry"`
	RegistryInCluster string `json:"registry_in_cluster"`
}

// PushResult reports a completed artifact push + reconcile.
type PushResult struct {
	Artifact   string `json:"artifact"`
	Path       string `json:"path"`
	Source     string `json:"source"`
	Revision   string `json:"revision"`
	Reconciled string `json:"reconciled"`
}

func contains(items []string, target string) bool {
	for _, i := range items {
		if i == target {
			return true
		}
	}
	return false
}

// ListClusters returns the names of the local clusters. The slice is never nil,
// so an empty result renders as a JSON `[]` rather than `null`.
func ListClusters(ctx context.Context, client LocalClusterAPI) ([]string, error) {
	clusters, err := client.List(ctx)
	if err != nil {
		return nil, err
	}
	if clusters == nil {
		clusters = []string{}
	}
	return clusters, nil
}

// CreateCluster creates a kind cluster (controlPlanes control-plane + workers
// worker nodes) wired to an internal OCI registry.
func CreateCluster(ctx context.Context, client LocalClusterAPI, name, registryName string, registryPort, controlPlanes, workers int) (CreateResult, error) {
	existing, err := client.List(ctx)
	if err != nil {
		return CreateResult{}, err
	}
	if contains(existing, name) {
		return CreateResult{}, &LocalClusterExistsError{Name: name}
	}
	if err := client.Create(ctx, name, registryName, registryPort, controlPlanes, workers); err != nil {
		return CreateResult{}, err
	}
	return CreateResult{
		Name:              name,
		Registry:          fmt.Sprintf("localhost:%d", registryPort),
		RegistryInCluster: fmt.Sprintf("%s:%d", registryName, RegistryInClusterPort),
	}, nil
}

// DeleteCluster deletes a local cluster. Idempotent: a missing cluster is a
// no-op. Returns the name deleted, or "" if none existed.
func DeleteCluster(ctx context.Context, client LocalClusterAPI, name string) (string, error) {
	existing, err := client.List(ctx)
	if err != nil {
		return "", err
	}
	if !contains(existing, name) {
		return "", nil
	}
	if err := client.Delete(ctx, name); err != nil {
		return "", err
	}
	return name, nil
}

// PushArtifact publishes path as an OCI artifact to the local registry and nudges
// Flux to pull it. Publishing is what matters; the reconcile is a best-effort
// immediate trigger — the OCIRepository may not exist yet (a first push, or
// bootstrap's own pre-push), and Flux re-pulls on its interval regardless. So a
// reconcile failure does not fail the push; the result's Reconciled is "" then.
func PushArtifact(ctx context.Context, client LocalClusterAPI, name, tag, path string, registryPort int) (PushResult, error) {
	reference := fmt.Sprintf("localhost:%d/%s:%s", registryPort, name, tag)
	source, revision, err := client.GitProvenance(ctx)
	if err != nil {
		return PushResult{}, err
	}
	if err := client.PushArtifact(ctx, "oci://"+reference, path, source, revision); err != nil {
		return PushResult{}, err
	}
	reconciled := name
	if err := client.ReconcileSource(ctx, name); err != nil {
		reconciled = ""
	}
	return PushResult{
		Artifact: reference, Path: path, Source: source,
		Revision: revision, Reconciled: reconciled,
	}, nil
}

// GetKubeconfig returns the kubeconfig for the named local cluster.
func GetKubeconfig(ctx context.Context, client LocalClusterAPI, name string) (string, error) {
	existing, err := client.List(ctx)
	if err != nil {
		return "", err
	}
	if !contains(existing, name) {
		return "", &LocalClusterNotFoundError{Name: name}
	}
	return client.GetKubeconfig(ctx, name)
}
