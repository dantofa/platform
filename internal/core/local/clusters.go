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
	DefaultArtifactPath = "flux/"

	registryInClusterPort = 5000
)

// LocalClusterAPI is the local-cluster surface this package depends on.
type LocalClusterAPI interface {
	List(ctx context.Context) ([]string, error)
	Create(ctx context.Context, name, registryName string, registryPort int) error
	Delete(ctx context.Context, name string) error
	GetKubeconfig(ctx context.Context, name string) (string, error)
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

// ListClusters returns the names of the local clusters.
func ListClusters(ctx context.Context, client LocalClusterAPI) ([]string, error) {
	return client.List(ctx)
}

// CreateCluster creates a kind cluster wired to an internal OCI registry.
func CreateCluster(ctx context.Context, client LocalClusterAPI, name, registryName string, registryPort int) (CreateResult, error) {
	existing, err := client.List(ctx)
	if err != nil {
		return CreateResult{}, err
	}
	if contains(existing, name) {
		return CreateResult{}, &LocalClusterExistsError{Name: name}
	}
	if err := client.Create(ctx, name, registryName, registryPort); err != nil {
		return CreateResult{}, err
	}
	return CreateResult{
		Name:              name,
		Registry:          fmt.Sprintf("localhost:%d", registryPort),
		RegistryInCluster: fmt.Sprintf("%s:%d", registryName, registryInClusterPort),
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

// PushArtifact publishes path as an OCI artifact to the local registry and
// reconciles the Flux OCIRepository named name.
func PushArtifact(ctx context.Context, client LocalClusterAPI, name, tag, path string, registryPort int) (PushResult, error) {
	reference := fmt.Sprintf("localhost:%d/%s:%s", registryPort, name, tag)
	source, revision, err := client.GitProvenance(ctx)
	if err != nil {
		return PushResult{}, err
	}
	if err := client.PushArtifact(ctx, "oci://"+reference, path, source, revision); err != nil {
		return PushResult{}, err
	}
	if err := client.ReconcileSource(ctx, name); err != nil {
		return PushResult{}, err
	}
	return PushResult{
		Artifact: reference, Path: path, Source: source,
		Revision: revision, Reconciled: name,
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
