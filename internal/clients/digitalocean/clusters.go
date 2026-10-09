package digitalocean

import (
	"context"
	"fmt"
	"os"

	"github.com/digitalocean/godo"

	core "github.com/dantofa/platform/internal/core/digitalocean"
)

const tokenEnv = "DIGITALOCEAN_ACCESS_TOKEN"

// perPage requests DigitalOcean's max page size to minimise round-trips.
const perPage = 200

// ClusterClient is a semantic wrapper over godo's Kubernetes operations. It
// returns core domain types and translates godo errors into APIError so callers
// stay free of the SDK's error types.
type ClusterClient struct {
	godo *godo.Client
}

// resolveDOToken returns the DO API token from the argument or
// $DIGITALOCEAN_ACCESS_TOKEN, or "" if neither is set.
func resolveDOToken(token string) string {
	if token == "" {
		return os.Getenv(tokenEnv)
	}
	return token
}

// NewClusterClient builds a cluster client, reading the token from the argument
// or $DIGITALOCEAN_ACCESS_TOKEN.
func NewClusterClient(token string) (*ClusterClient, error) {
	token = resolveDOToken(token)
	if token == "" {
		return nil, MissingCredentials(
			fmt.Sprintf("pass --token or set $%s.", tokenEnv),
		)
	}
	return &ClusterClient{godo: godo.NewFromToken(token)}, nil
}

// List returns every cluster, following pagination.
func (c *ClusterClient) List(ctx context.Context) ([]core.Cluster, error) {
	opts := &godo.ListOptions{Page: 1, PerPage: perPage}
	var clusters []core.Cluster
	for {
		page, resp, err := c.godo.Kubernetes.List(ctx, opts)
		if err != nil {
			return nil, apiError(err)
		}
		for _, cl := range page {
			clusters = append(clusters, toCoreCluster(cl))
		}
		if resp == nil || resp.Links == nil || resp.Links.IsLastPage() {
			break
		}
		next, err := resp.Links.CurrentPage()
		if err != nil {
			return nil, err
		}
		opts.Page = next + 1
	}
	return clusters, nil
}

// Create creates a cluster from the neutral spec.
func (c *ClusterClient) Create(ctx context.Context, spec core.CreateSpec) (core.Cluster, error) {
	pools := make([]*godo.KubernetesNodePoolCreateRequest, 0, len(spec.NodePools))
	for _, p := range spec.NodePools {
		pools = append(pools, &godo.KubernetesNodePoolCreateRequest{
			Name: p.Name, Size: p.Size, Count: p.Count,
			AutoScale: p.AutoScale, MinNodes: p.MinNodes, MaxNodes: p.MaxNodes,
		})
	}
	req := &godo.KubernetesClusterCreateRequest{
		Name:         spec.Name,
		RegionSlug:   spec.Region,
		VersionSlug:  spec.Version,
		Tags:         spec.Tags,
		NodePools:    pools,
		AutoUpgrade:  spec.AutoUpgrade,
		SurgeUpgrade: spec.SurgeUpgrade,
	}
	if spec.HA {
		ha := true
		req.HA = &ha
	}
	cl, _, err := c.godo.Kubernetes.Create(ctx, req)
	if err != nil {
		return core.Cluster{}, apiError(err)
	}
	return toCoreCluster(cl), nil
}

// Update updates a cluster's mutable fields.
func (c *ClusterClient) Update(ctx context.Context, id string, spec core.UpdateSpec) (core.Cluster, error) {
	autoUpgrade := spec.AutoUpgrade
	req := &godo.KubernetesClusterUpdateRequest{
		Name:         spec.Name,
		AutoUpgrade:  &autoUpgrade,
		SurgeUpgrade: spec.SurgeUpgrade,
	}
	if spec.Tags != nil {
		req.Tags = *spec.Tags
	}
	if spec.HA {
		ha := true
		req.HA = &ha
	}
	cl, _, err := c.godo.Kubernetes.Update(ctx, id, req)
	if err != nil {
		return core.Cluster{}, apiError(err)
	}
	return toCoreCluster(cl), nil
}

// AssociatedResources lists the Volumes, Volume Snapshots, and LoadBalancers
// DigitalOcean provisioned for the cluster's PVCs and Services -- exactly what
// a plain cluster delete would otherwise leave behind.
func (c *ClusterClient) AssociatedResources(ctx context.Context, id string) (core.AssociatedResources, error) {
	res, _, err := c.godo.Kubernetes.ListAssociatedResourcesForDeletion(ctx, id)
	if err != nil {
		return core.AssociatedResources{}, apiError(err)
	}
	return core.AssociatedResources{
		VolumeIDs:         resourceIDs(res.Volumes),
		VolumeSnapshotIDs: resourceIDs(res.VolumeSnapshots),
		LoadBalancerIDs:   resourceIDs(res.LoadBalancers),
	}, nil
}

// DeleteSelective deletes a cluster together with the given associated
// resources, so the Volumes/LoadBalancers it provisioned don't outlive it as
// orphaned, billed resources.
func (c *ClusterClient) DeleteSelective(ctx context.Context, id string, resources core.AssociatedResources) error {
	_, err := c.godo.Kubernetes.DeleteSelective(ctx, id, &godo.KubernetesClusterDeleteSelectiveRequest{
		Volumes:         resources.VolumeIDs,
		VolumeSnapshots: resources.VolumeSnapshotIDs,
		LoadBalancers:   resources.LoadBalancerIDs,
	})
	if err != nil {
		return apiError(err)
	}
	return nil
}

func resourceIDs(resources []*godo.AssociatedResource) []string {
	ids := make([]string, 0, len(resources))
	for _, r := range resources {
		if r == nil {
			continue
		}
		ids = append(ids, r.ID)
	}
	return ids
}

// ListVolumes returns every block-storage Volume in the account -- not just
// those attached to a cluster. It backs the drift audit's account-wide side.
func (c *ClusterClient) ListVolumes(ctx context.Context) ([]core.Volume, error) {
	opts := &godo.ListVolumeParams{ListOptions: &godo.ListOptions{Page: 1, PerPage: perPage}}
	var volumes []core.Volume
	for {
		page, resp, err := c.godo.Storage.ListVolumes(ctx, opts)
		if err != nil {
			return nil, apiError(err)
		}
		for _, v := range page {
			volumes = append(volumes, toCoreVolume(v))
		}
		if resp == nil || resp.Links == nil || resp.Links.IsLastPage() {
			break
		}
		next, err := resp.Links.CurrentPage()
		if err != nil {
			return nil, err
		}
		opts.ListOptions.Page = next + 1
	}
	return volumes, nil
}

// ListVolumeSnapshots returns every volume snapshot in the account.
func (c *ClusterClient) ListVolumeSnapshots(ctx context.Context) ([]core.VolumeSnapshot, error) {
	opts := &godo.ListOptions{Page: 1, PerPage: perPage}
	var snapshots []core.VolumeSnapshot
	for {
		page, resp, err := c.godo.Snapshots.ListVolume(ctx, opts)
		if err != nil {
			return nil, apiError(err)
		}
		for _, s := range page {
			snapshots = append(snapshots, toCoreSnapshot(s))
		}
		if resp == nil || resp.Links == nil || resp.Links.IsLastPage() {
			break
		}
		next, err := resp.Links.CurrentPage()
		if err != nil {
			return nil, err
		}
		opts.Page = next + 1
	}
	return snapshots, nil
}

// ListLoadBalancers returns every Load Balancer in the account.
func (c *ClusterClient) ListLoadBalancers(ctx context.Context) ([]core.LoadBalancer, error) {
	opts := &godo.ListOptions{Page: 1, PerPage: perPage}
	var loadBalancers []core.LoadBalancer
	for {
		page, resp, err := c.godo.LoadBalancers.List(ctx, opts)
		if err != nil {
			return nil, apiError(err)
		}
		for _, lb := range page {
			loadBalancers = append(loadBalancers, toCoreLoadBalancer(lb))
		}
		if resp == nil || resp.Links == nil || resp.Links.IsLastPage() {
			break
		}
		next, err := resp.Links.CurrentPage()
		if err != nil {
			return nil, err
		}
		opts.Page = next + 1
	}
	return loadBalancers, nil
}

func toCoreVolume(v godo.Volume) core.Volume {
	out := core.Volume{ID: v.ID, Name: v.Name, SizeGigaBytes: v.SizeGigaBytes}
	if v.Region != nil {
		out.Region = v.Region.Slug
	}
	if !v.CreatedAt.IsZero() {
		out.CreatedAt = v.CreatedAt.Format("2006-01-02T15:04:05Z07:00")
	}
	return out
}

func toCoreSnapshot(s godo.Snapshot) core.VolumeSnapshot {
	out := core.VolumeSnapshot{ID: s.ID, Name: s.Name, CreatedAt: s.Created}
	if len(s.Regions) > 0 {
		out.Region = s.Regions[0]
	}
	return out
}

func toCoreLoadBalancer(lb godo.LoadBalancer) core.LoadBalancer {
	out := core.LoadBalancer{ID: lb.ID, Name: lb.Name, CreatedAt: lb.Created}
	if lb.Region != nil {
		out.Region = lb.Region.Slug
	}
	return out
}

// Get returns a single cluster (including status.state).
func (c *ClusterClient) Get(ctx context.Context, id string) (core.Cluster, error) {
	cl, _, err := c.godo.Kubernetes.Get(ctx, id)
	if err != nil {
		return core.Cluster{}, apiError(err)
	}
	return toCoreCluster(cl), nil
}

// GetKubeconfig returns the cluster's kubeconfig YAML.
func (c *ClusterClient) GetKubeconfig(ctx context.Context, id string) (string, error) {
	cfg, _, err := c.godo.Kubernetes.GetKubeConfig(ctx, id, nil)
	if err != nil {
		return "", apiError(err)
	}
	return string(cfg.KubeconfigYAML), nil
}

// GetSize returns the neutral Size for a droplet slug, following pagination. It
// backs the per-cluster cost model (node price + included transfer); the godo
// Size type is mapped to core.Size here so it never leaks out of the adapter.
func (c *ClusterClient) GetSize(ctx context.Context, slug string) (core.Size, error) {
	opts := &godo.ListOptions{Page: 1, PerPage: perPage}
	for {
		sizes, resp, err := c.godo.Sizes.List(ctx, opts)
		if err != nil {
			return core.Size{}, apiError(err)
		}
		for _, s := range sizes {
			if s.Slug == slug {
				return core.Size{Slug: s.Slug, PriceHourly: s.PriceHourly, TransferTB: s.Transfer}, nil
			}
		}
		if resp == nil || resp.Links == nil || resp.Links.IsLastPage() {
			break
		}
		next, err := resp.Links.CurrentPage()
		if err != nil {
			return core.Size{}, err
		}
		opts.Page = next + 1
	}
	return core.Size{}, &core.SizeNotFoundError{Slug: slug}
}

func toCoreCluster(cl *godo.KubernetesCluster) core.Cluster {
	if cl == nil {
		return core.Cluster{}
	}
	out := core.Cluster{
		ID:       cl.ID,
		Name:     cl.Name,
		Region:   cl.RegionSlug,
		Version:  cl.VersionSlug,
		Endpoint: cl.Endpoint,
		Tags:     cl.Tags,
	}
	if cl.Status != nil {
		out.State = string(cl.Status.State)
	}
	if !cl.CreatedAt.IsZero() {
		out.CreatedAt = cl.CreatedAt.Format("2006-01-02T15:04:05Z07:00")
	}
	for _, p := range cl.NodePools {
		if p == nil {
			continue
		}
		out.NodePools = append(out.NodePools, core.NodePool{
			ID: p.ID, Name: p.Name, Size: p.Size, Count: p.Count,
			AutoScale: p.AutoScale, MinNodes: p.MinNodes, MaxNodes: p.MaxNodes,
		})
	}
	return out
}
