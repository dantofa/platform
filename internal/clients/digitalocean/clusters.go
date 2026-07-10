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

// Delete deletes a cluster by id.
func (c *ClusterClient) Delete(ctx context.Context, id string) error {
	_, err := c.godo.Kubernetes.Delete(ctx, id)
	if err != nil {
		return apiError(err)
	}
	return nil
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
