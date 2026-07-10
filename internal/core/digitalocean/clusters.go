package digitalocean

import (
	"context"
	"fmt"
	"time"
)

const runningState = "running"

// failedStates are terminal: a freshly-created cluster in one will never reach
// running.
var failedStates = map[string]bool{"error": true, "deleted": true, "deleting": true}

// DefaultWaitTimeout / DefaultPollInterval govern `create --wait`.
const (
	DefaultWaitTimeout  = 15 * time.Minute
	DefaultPollInterval = 10 * time.Second
)

// Cluster is a DOKS cluster as surfaced to the user.
type Cluster struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Region    string     `json:"region"`
	Version   string     `json:"version"`
	State     string     `json:"state,omitempty"`
	Endpoint  string     `json:"endpoint,omitempty"`
	Tags      []string   `json:"tags,omitempty"`
	NodePools []NodePool `json:"node_pools,omitempty"`
	CreatedAt string     `json:"created_at,omitempty"`
}

// NodePool is a cluster node pool as surfaced to the user.
type NodePool struct {
	ID        string `json:"id,omitempty"`
	Name      string `json:"name"`
	Size      string `json:"size"`
	Count     int    `json:"count"`
	AutoScale bool   `json:"auto_scale"`
	MinNodes  int    `json:"min_nodes"`
	MaxNodes  int    `json:"max_nodes"`
}

// NodePoolSpec describes a node pool to create.
type NodePoolSpec struct {
	Name      string
	Size      string
	Count     int
	MinNodes  int
	MaxNodes  int
	AutoScale bool
}

// CreateSpec is the neutral create request. The opinionated invariants
// (auto-upgrade, surge-upgrade, autoscaling) are baked in by the builders here,
// not by the client adapter, so no adapter can bypass them.
type CreateSpec struct {
	Name         string
	Region       string
	Version      string
	NodePools    []NodePoolSpec
	Tags         []string
	AutoUpgrade  bool
	SurgeUpgrade bool
	HA           bool
}

// UpdateSpec is the neutral update request. Tags is nil to leave tags untouched,
// or a (possibly empty) slice to replace them. Name is always re-sent.
type UpdateSpec struct {
	Name         string
	Tags         *[]string
	AutoUpgrade  bool
	SurgeUpgrade bool
	HA           bool
}

// ClusterAPI is the DO cluster surface this package depends on.
type ClusterAPI interface {
	List(ctx context.Context) ([]Cluster, error)
	Create(ctx context.Context, spec CreateSpec) (Cluster, error)
	Update(ctx context.Context, id string, spec UpdateSpec) (Cluster, error)
	Delete(ctx context.Context, id string) error
	Get(ctx context.Context, id string) (Cluster, error)
	GetKubeconfig(ctx context.Context, id string) (string, error)
}

// ClusterNotFoundError is returned when no cluster matches a name.
type ClusterNotFoundError struct{ Identifier string }

func (e *ClusterNotFoundError) Error() string {
	return fmt.Sprintf("no cluster found matching %q", e.Identifier)
}

// ClusterNotReadyError is returned when a cluster fails to reach running.
type ClusterNotReadyError struct {
	Name     string
	State    string
	TimedOut bool
}

func (e *ClusterNotReadyError) Error() string {
	reason := fmt.Sprintf("entered state %q", e.State)
	if e.TimedOut {
		reason = "timed out waiting"
	}
	return fmt.Sprintf("cluster %q did not become ready: %s", e.Name, reason)
}

// BuildNodePool assembles a node pool spec (opinionated: autoscaling is always on).
func BuildNodePool(name, size string, count, minNodes, maxNodes int) NodePoolSpec {
	return NodePoolSpec{
		Name: name, Size: size, Count: count,
		MinNodes: minNodes, MaxNodes: maxNodes, AutoScale: true,
	}
}

// BuildCreateSpec assembles a create spec around the single node pool the CLI
// creates (opinionated: auto-upgrade and surge-upgrade are always enabled).
func BuildCreateSpec(name, region, version string, pool NodePoolSpec, tags []string, ha bool) CreateSpec {
	if tags == nil {
		tags = []string{}
	}
	return CreateSpec{
		Name: name, Region: region, Version: version,
		NodePools: []NodePoolSpec{pool}, Tags: tags,
		AutoUpgrade: true, SurgeUpgrade: true, HA: ha,
	}
}

// BuildUpdateSpec assembles an update spec (opinionated: auto-upgrade and
// surge-upgrade are always re-asserted).
func BuildUpdateSpec(tags *[]string, ha bool) UpdateSpec {
	return UpdateSpec{Tags: tags, AutoUpgrade: true, SurgeUpgrade: true, HA: ha}
}

// ListClusters returns every cluster.
func ListClusters(ctx context.Context, client ClusterAPI) ([]Cluster, error) {
	return client.List(ctx)
}

// CreateCluster creates a cluster and returns what DigitalOcean reports back.
func CreateCluster(ctx context.Context, client ClusterAPI, spec CreateSpec) (Cluster, error) {
	return client.Create(ctx, spec)
}

func resolve(clusters []Cluster, name string) (Cluster, bool) {
	for _, c := range clusters {
		if c.Name == name {
			return c, true
		}
	}
	return Cluster{}, false
}

// UpdateCluster updates the named cluster's mutable fields. Clusters are
// identified by name; the id is resolved internally.
func UpdateCluster(ctx context.Context, client ClusterAPI, name string, spec UpdateSpec) (Cluster, error) {
	clusters, err := client.List(ctx)
	if err != nil {
		return Cluster{}, err
	}
	existing, ok := resolve(clusters, name)
	if !ok {
		return Cluster{}, &ClusterNotFoundError{Identifier: name}
	}
	spec.Name = name
	return client.Update(ctx, existing.ID, spec)
}

// GetKubeconfig returns the kubeconfig for the named cluster.
func GetKubeconfig(ctx context.Context, client ClusterAPI, name string) (string, error) {
	clusters, err := client.List(ctx)
	if err != nil {
		return "", err
	}
	existing, ok := resolve(clusters, name)
	if !ok {
		return "", &ClusterNotFoundError{Identifier: name}
	}
	return client.GetKubeconfig(ctx, existing.ID)
}

// WaitForRunning polls until the named cluster reaches running, returning it.
// It fails on a terminal state or when timeout elapses.
func WaitForRunning(ctx context.Context, client ClusterAPI, name string, timeout, interval time.Duration) (Cluster, error) {
	clusters, err := client.List(ctx)
	if err != nil {
		return Cluster{}, err
	}
	existing, ok := resolve(clusters, name)
	if !ok {
		return Cluster{}, &ClusterNotFoundError{Identifier: name}
	}
	deadline := time.Now().Add(timeout)
	for {
		cluster, err := client.Get(ctx, existing.ID)
		if err != nil {
			return Cluster{}, err
		}
		switch {
		case cluster.State == runningState:
			return cluster, nil
		case failedStates[cluster.State]:
			return Cluster{}, &ClusterNotReadyError{Name: name, State: cluster.State}
		case time.Now().After(deadline):
			return Cluster{}, &ClusterNotReadyError{Name: name, State: cluster.State, TimedOut: true}
		}
		select {
		case <-ctx.Done():
			return Cluster{}, ctx.Err()
		case <-time.After(interval):
		}
	}
}

// DeleteCluster deletes the named cluster. Idempotent: a missing cluster is a
// no-op. Returns the resolved id, or "" if none existed.
func DeleteCluster(ctx context.Context, client ClusterAPI, name string) (string, error) {
	clusters, err := client.List(ctx)
	if err != nil {
		return "", err
	}
	existing, ok := resolve(clusters, name)
	if !ok {
		return "", nil
	}
	if err := client.Delete(ctx, existing.ID); err != nil {
		return "", err
	}
	return existing.ID, nil
}
