package digitalocean

import "context"

// Volume is a DigitalOcean block-storage volume as surfaced to the user.
type Volume struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	SizeGigaBytes int64  `json:"size_gigabytes"`
	Region        string `json:"region,omitempty"`
	CreatedAt     string `json:"created_at,omitempty"`
}

// VolumeSnapshot is a DigitalOcean volume snapshot as surfaced to the user.
type VolumeSnapshot struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Region    string `json:"region,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
}

// LoadBalancer is a DigitalOcean load balancer as surfaced to the user.
type LoadBalancer struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Region    string `json:"region,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
}

// DriftAPI is the DO surface a drift audit depends on: the account-wide
// inventory (every Volume/VolumeSnapshot/LoadBalancer, not just a given
// cluster's), plus the ClusterAPI methods it cross-references against.
type DriftAPI interface {
	ClusterAPI
	ListVolumes(ctx context.Context) ([]Volume, error)
	ListVolumeSnapshots(ctx context.Context) ([]VolumeSnapshot, error)
	ListLoadBalancers(ctx context.Context) ([]LoadBalancer, error)
}

// DriftReport is the account-wide Volumes/VolumeSnapshots/LoadBalancers that
// belong to no currently-live cluster.
type DriftReport struct {
	OrphanedVolumes         []Volume         `json:"orphaned_volumes"`
	OrphanedVolumeSnapshots []VolumeSnapshot `json:"orphaned_volume_snapshots"`
	OrphanedLoadBalancers   []LoadBalancer   `json:"orphaned_load_balancers"`
}

// Empty reports whether the audit found no drift.
func (r DriftReport) Empty() bool {
	return len(r.OrphanedVolumes) == 0 &&
		len(r.OrphanedVolumeSnapshots) == 0 &&
		len(r.OrphanedLoadBalancers) == 0
}

// AuditDrift audits the account for Volumes, VolumeSnapshots, and
// LoadBalancers that belong to no currently-live cluster. It unions every live
// cluster's AssociatedResources (the same set DeleteCluster reaps on delete,
// see clusters.go) into an "expected" set, then reports every account-wide
// resource outside it -- catching a leak from any path DeleteCluster's own
// invariant doesn't cover (a process killed between create and a later
// delete, or a resource created outside dctl entirely). ok is false whenever
// the report is non-empty, so a scheduled job can gate on it the way `flux
// kustomization verify` gates on reconciliation.
func AuditDrift(ctx context.Context, client DriftAPI) (DriftReport, bool, error) {
	clusters, err := client.List(ctx)
	if err != nil {
		return DriftReport{}, false, err
	}
	expected := map[string]bool{}
	for _, c := range clusters {
		resources, err := client.AssociatedResources(ctx, c.ID)
		if err != nil {
			return DriftReport{}, false, err
		}
		markExpected(expected, resources.VolumeIDs)
		markExpected(expected, resources.VolumeSnapshotIDs)
		markExpected(expected, resources.LoadBalancerIDs)
	}

	volumes, err := client.ListVolumes(ctx)
	if err != nil {
		return DriftReport{}, false, err
	}
	snapshots, err := client.ListVolumeSnapshots(ctx)
	if err != nil {
		return DriftReport{}, false, err
	}
	loadBalancers, err := client.ListLoadBalancers(ctx)
	if err != nil {
		return DriftReport{}, false, err
	}

	report := DriftReport{
		OrphanedVolumes:         []Volume{},
		OrphanedVolumeSnapshots: []VolumeSnapshot{},
		OrphanedLoadBalancers:   []LoadBalancer{},
	}
	for _, v := range volumes {
		if !expected[v.ID] {
			report.OrphanedVolumes = append(report.OrphanedVolumes, v)
		}
	}
	for _, s := range snapshots {
		if !expected[s.ID] {
			report.OrphanedVolumeSnapshots = append(report.OrphanedVolumeSnapshots, s)
		}
	}
	for _, lb := range loadBalancers {
		if !expected[lb.ID] {
			report.OrphanedLoadBalancers = append(report.OrphanedLoadBalancers, lb)
		}
	}
	return report, report.Empty(), nil
}

func markExpected(set map[string]bool, ids []string) {
	for _, id := range ids {
		set[id] = true
	}
}
