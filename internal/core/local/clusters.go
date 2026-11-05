// Package local holds the framework-free application logic for local (kind)
// development clusters. The kind/docker/flux/git surface is reached through the
// LocalClusterAPI interface, satisfied by the clients adapter.
package local

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	fluxcore "github.com/dantofa/platform/internal/core/flux"
)

// Defaults for local clusters and the OCI push flow.
const (
	DefaultClusterName  = "local"
	DefaultRegistryName = "kind-registry"
	DefaultRegistryPort = 5001
	DefaultArtifactName = "local"
	DefaultArtifactTag  = "latest"
	// PlatformSource is the provenance stamped on the embedded platform artifact.
	PlatformSource = "https://github.com/dantofa/platform"
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

// Ingress publishing. A local cluster puts its ingress on the host's REAL ports,
// so a bootstrapped cluster answers at its real URL (http://<base-domain>/… and
// https://<base-domain>/…) with nothing in the path — no `kubectl port-forward`,
// no per-tool resolver flags. The DNS side is a wildcard A record pointing the
// base domain at the loopback; these mappings put Traefik behind it.
//
// Deliberately fixed, not caller-configurable: a non-default host port would put
// a `:8443` back into every URL, which is the exact wart this removes. The cost is
// that only one local cluster can hold the ports at a time — the second `create`
// fails loudly on the port bind rather than coming up subtly unreachable.
const (
	// IngressHTTPHostPort / IngressHTTPSHostPort are the host ports published on
	// the loopback. IngressHTTPNodePort / IngressHTTPSNodePort are the Traefik
	// Service nodePorts they map onto — these MUST match the nodePorts pinned in
	// flux/local/traefik/release.yaml, which is the other half of this contract.
	IngressHTTPHostPort  = 80
	IngressHTTPNodePort  = 30080
	IngressHTTPSHostPort = 443
	IngressHTTPSNodePort = 30443
	// IngressListenAddress binds the published ports to the loopback only. The
	// DNS answer is 127.0.0.1, so binding the wildcard address would expose the
	// dev cluster on the LAN for no benefit.
	IngressListenAddress = "127.0.0.1"
)

// IngressPortMapping is one host-port → node-port publication of the local
// ingress: the neutral spec the kind adapter renders as an `extraPortMappings`
// entry (keeping the kind config format out of core).
type IngressPortMapping struct {
	// HostPort is the port bound on IngressListenAddress on the developer's host.
	HostPort int
	// NodePort is the Traefik Service nodePort inside the cluster it maps onto.
	NodePort int
}

// IngressPortMappings returns the host→node publications every local cluster
// gets. A NodePort Service answers on every node, so the caller only needs to
// attach these to one node.
func IngressPortMappings() []IngressPortMapping {
	return []IngressPortMapping{
		{HostPort: IngressHTTPHostPort, NodePort: IngressHTTPNodePort},
		{HostPort: IngressHTTPSHostPort, NodePort: IngressHTTPSNodePort},
	}
}

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
	// OCIRepository names the Flux source registered for the artifact on the
	// cluster; empty when none was (the platform artifact, or no cluster).
	OCIRepository string `json:"oci_repository,omitempty"`
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
	source, revision, err := client.GitProvenance(ctx)
	if err != nil {
		return PushResult{}, err
	}
	return pushArtifact(ctx, client, name, tag, path, source, revision, registryPort)
}

func pushArtifact(ctx context.Context, client LocalClusterAPI, name, tag, path, source, revision string, registryPort int) (PushResult, error) {
	reference := fmt.Sprintf("localhost:%d/%s:%s", registryPort, name, tag)
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

// PushPlatformArtifact publishes the platform's own flux/ tree (tree, rooted so
// paths keep their flux/ prefix -- the dctl binary's embedded copy) as an OCI
// artifact, stamped with the platform source and revision. It never reads the
// working directory, so it is correct from any project (a downstream's flux/
// can't be mistaken for the platform's).
func PushPlatformArtifact(ctx context.Context, client LocalClusterAPI, tree fs.FS, revision, name, tag string, registryPort int) (PushResult, error) {
	dir, err := os.MkdirTemp("", "dctl-platform-*")
	if err != nil {
		return PushResult{}, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if err := writeTree(tree, dir); err != nil {
		return PushResult{}, err
	}
	return pushArtifact(ctx, client, name, tag, dir, PlatformSource, revision, registryPort)
}

// writeTree copies the tree into dir, preserving relative paths.
func writeTree(tree fs.FS, dir string) error {
	return fs.WalkDir(tree, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		dst := filepath.Join(dir, filepath.FromSlash(p))
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		data, err := fs.ReadFile(tree, p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, data, 0o644)
	})
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

// RegisterSource registers the in-cluster OCIRepository that pulls the artifact
// `name` (create-or-update, idempotent), so a downstream needs no kubectl lookup of
// the registry URL: the registry's in-cluster address and the source's shape are
// this package's own knowledge (see InClusterArtifactURL). The source takes the
// artifact's name. The platform artifact is skipped (returns "", nil): bootstrap
// registers it under its own source name, and a second source for the same
// artifact would only duplicate it.
func RegisterSource(ctx context.Context, client LocalClusterAPI, eng fluxcore.Engine, registryName, name, tag string) (string, error) {
	if name == DefaultArtifactName {
		return "", nil
	}
	url, err := InClusterArtifactURL(ctx, client, registryName, name)
	if err != nil {
		return "", err
	}
	// Insecure: the in-cluster kind registry is plain HTTP.
	res, err := fluxcore.AddSource(ctx, eng, nil, fluxcore.SourceSpec{
		Type: fluxcore.SourceOCI, Name: name, URL: url, Revision: tag, Insecure: true,
	})
	if err != nil {
		return "", err
	}
	return res.Source, nil
}
