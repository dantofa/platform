package digitalocean

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/dantofa/platform/internal/core/retry"
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

// DefaultVolumeReapTimeout / DefaultVolumeReapInterval govern DeleteCluster's
// sweep of the cluster's tagged Volumes after the delete call (see
// reapTaggedVolumes): long enough for DigitalOcean to destroy the node Droplets
// and so detach every PVC's Volume, polled at a pace that matches how fast that
// actually happens.
const (
	DefaultVolumeReapTimeout  = 10 * time.Minute
	DefaultVolumeReapInterval = 10 * time.Second
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

// AssociatedResources are the DO Volumes, Volume Snapshots, and Load Balancers
// a cluster's PVCs and Services provisioned -- exactly what DO's plain cluster
// delete leaves behind as orphaned, billed resources (see DeleteCluster).
type AssociatedResources struct {
	VolumeIDs         []string `json:"volume_ids"`
	VolumeSnapshotIDs []string `json:"volume_snapshot_ids"`
	LoadBalancerIDs   []string `json:"load_balancer_ids"`
}

// ClusterAPI is the DO cluster surface this package depends on.
type ClusterAPI interface {
	List(ctx context.Context) ([]Cluster, error)
	Create(ctx context.Context, spec CreateSpec) (Cluster, error)
	Update(ctx context.Context, id string, spec UpdateSpec) (Cluster, error)
	AssociatedResources(ctx context.Context, id string) (AssociatedResources, error)
	DeleteSelective(ctx context.Context, id string, resources AssociatedResources) error
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

// ListClusters returns every cluster. The slice is never nil, so an empty result
// renders as a JSON `[]` rather than `null`.
func ListClusters(ctx context.Context, client ClusterAPI) ([]Cluster, error) {
	clusters, err := client.List(ctx)
	if err != nil {
		return nil, err
	}
	if clusters == nil {
		clusters = []Cluster{}
	}
	return clusters, nil
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

// ClusterDeleteAPI is the DO surface DeleteCluster depends on: the cluster
// operations plus the account-wide Volume listing and per-Volume delete its
// tag-based sweep needs.
type ClusterDeleteAPI interface {
	ClusterAPI
	ListVolumes(ctx context.Context) ([]Volume, error)
	// DeleteVolume deletes a Volume; an already-absent one is not an error.
	DeleteVolume(ctx context.Context, id string) error
}

// DeleteResult reports what DeleteCluster removed. Associated is what
// DigitalOcean listed for the selective delete; ReapedVolumeIDs are the tagged
// Volumes still present afterwards that DeleteCluster deleted itself. Their
// overlap tells which half failed: an ID in both was listed but not deleted by
// DigitalOcean, an ID only in ReapedVolumeIDs was never listed at all.
type DeleteResult struct {
	ID              string              `json:"id"`
	Associated      AssociatedResources `json:"associated_resources"`
	ReapedVolumeIDs []string            `json:"reaped_volume_ids"`
}

// VolumesNotReapedError is returned when a deleted cluster's tagged Volumes are
// still present once the sweep's timeout elapses.
type VolumesNotReapedError struct {
	ClusterID string
	VolumeIDs []string
}

func (e *VolumesNotReapedError) Error() string {
	return fmt.Sprintf("cluster %s deleted, but its volumes %v were still present after the sweep timed out", e.ClusterID, e.VolumeIDs)
}

// DeleteCluster deletes the named cluster together with its associated
// resources (Volumes, Volume Snapshots, LoadBalancers) in the same call -- a
// plain delete leaves those behind as orphaned, billed resources, since
// DigitalOcean never reaps a PVC's Volume or a Service's LoadBalancer on its
// own. Not a caller choice: every deletion reaps them. Idempotent: a missing
// cluster is a no-op, returning a zero result.
//
// The selective delete alone is not enough: CI runs leaked every PVC Volume
// that lived as long as the cluster (Prometheus/Loki/Tempo), each still carrying
// the cluster's k8s:<id> tag and detached, so DeleteCluster then sweeps that tag
// until no Volume carries it (see reapTaggedVolumes). The tag is what the DO CSI
// driver stamps on every Volume it creates, so the sweep holds regardless of
// whether DigitalOcean failed to list a Volume or listed it and failed to delete
// it.
func DeleteCluster(ctx context.Context, client ClusterDeleteAPI, name string, reapTimeout, reapInterval time.Duration) (DeleteResult, error) {
	clusters, err := client.List(ctx)
	if err != nil {
		return DeleteResult{}, err
	}
	existing, ok := resolve(clusters, name)
	if !ok {
		return DeleteResult{}, nil
	}
	resources, err := client.AssociatedResources(ctx, existing.ID)
	if err != nil {
		return DeleteResult{}, err
	}
	if err := client.DeleteSelective(ctx, existing.ID, resources); err != nil {
		return DeleteResult{}, err
	}
	result := DeleteResult{ID: existing.ID, Associated: resources}
	result.ReapedVolumeIDs, err = reapTaggedVolumes(ctx, client, existing.ID, reapTimeout, reapInterval)
	return result, err
}

// reapTaggedVolumes deletes every Volume tagged k8s:<clusterID> once it has
// detached (a Volume cannot be deleted while still attached to a node Droplet
// the cluster delete is tearing down), polling until none carry the tag or
// timeout elapses. A transient failure is retried on the next poll -- a dropped
// connection here must not leave the Volumes behind -- while any other error
// fails fast. Returns the IDs it deleted itself.
func reapTaggedVolumes(ctx context.Context, client ClusterDeleteAPI, clusterID string, timeout, interval time.Duration) ([]string, error) {
	tag := "k8s:" + clusterID
	reaped := []string{}
	deleted := map[string]bool{}
	deadline := time.Now().Add(timeout)
	for {
		remaining, err := sweepTaggedVolumes(ctx, client, tag, deleted, &reaped)
		switch {
		case err != nil && !retry.Transient(err):
			return reaped, err
		case err == nil && len(remaining) == 0:
			return reaped, nil
		case time.Now().After(deadline):
			if err != nil {
				return reaped, err
			}
			return reaped, &VolumesNotReapedError{ClusterID: clusterID, VolumeIDs: remaining}
		}
		select {
		case <-ctx.Done():
			return reaped, ctx.Err()
		case <-time.After(interval):
		}
	}
}

// sweepTaggedVolumes makes one pass of reapTaggedVolumes: it deletes each
// detached Volume carrying tag (recording it in reaped, once), and returns the
// IDs of those still attached.
func sweepTaggedVolumes(ctx context.Context, client ClusterDeleteAPI, tag string, deleted map[string]bool, reaped *[]string) ([]string, error) {
	volumes, err := client.ListVolumes(ctx)
	if err != nil {
		return nil, err
	}
	var remaining []string
	for _, v := range volumes {
		if !slices.Contains(v.Tags, tag) {
			continue
		}
		if len(v.DropletIDs) > 0 {
			remaining = append(remaining, v.ID)
			continue
		}
		if err := client.DeleteVolume(ctx, v.ID); err != nil {
			return nil, err
		}
		if !deleted[v.ID] {
			deleted[v.ID] = true
			*reaped = append(*reaped, v.ID)
		}
	}
	return remaining, nil
}
