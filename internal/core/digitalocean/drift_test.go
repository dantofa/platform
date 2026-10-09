package digitalocean

import (
	"context"
	"testing"
)

// fakeDriftAPI extends fakeClusterAPI (clusters_test.go) with the account-wide
// listings AuditDrift cross-references against.
type fakeDriftAPI struct {
	fakeClusterAPI
	volumes   []Volume
	snapshots []VolumeSnapshot
	lbs       []LoadBalancer
}

func (f *fakeDriftAPI) ListVolumes(context.Context) ([]Volume, error) { return f.volumes, nil }

func (f *fakeDriftAPI) ListVolumeSnapshots(context.Context) ([]VolumeSnapshot, error) {
	return f.snapshots, nil
}

func (f *fakeDriftAPI) ListLoadBalancers(context.Context) ([]LoadBalancer, error) { return f.lbs, nil }

func TestAuditDriftNoneFound(t *testing.T) {
	f := &fakeDriftAPI{
		fakeClusterAPI: fakeClusterAPI{
			clusters: []Cluster{{ID: "c1", Name: "live"}},
			associatedResources: AssociatedResources{
				VolumeIDs:       []string{"vol-1"},
				LoadBalancerIDs: []string{"lb-1"},
			},
		},
		volumes: []Volume{{ID: "vol-1"}},
		lbs:     []LoadBalancer{{ID: "lb-1"}},
	}
	report, ok, err := AuditDrift(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || !report.Empty() {
		t.Fatalf("expected no drift, got %+v", report)
	}
}

func TestAuditDriftFindsOrphans(t *testing.T) {
	f := &fakeDriftAPI{
		fakeClusterAPI: fakeClusterAPI{
			clusters: []Cluster{{ID: "c1", Name: "live"}},
			associatedResources: AssociatedResources{
				VolumeIDs: []string{"vol-1"},
			},
		},
		volumes:   []Volume{{ID: "vol-1"}, {ID: "vol-orphan"}},
		snapshots: []VolumeSnapshot{{ID: "snap-orphan"}},
		lbs:       []LoadBalancer{{ID: "lb-orphan"}},
	}
	report, ok, err := AuditDrift(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected drift to be found")
	}
	if len(report.OrphanedVolumes) != 1 || report.OrphanedVolumes[0].ID != "vol-orphan" {
		t.Errorf("unexpected orphaned volumes: %+v", report.OrphanedVolumes)
	}
	if len(report.OrphanedVolumeSnapshots) != 1 || report.OrphanedVolumeSnapshots[0].ID != "snap-orphan" {
		t.Errorf("unexpected orphaned snapshots: %+v", report.OrphanedVolumeSnapshots)
	}
	if len(report.OrphanedLoadBalancers) != 1 || report.OrphanedLoadBalancers[0].ID != "lb-orphan" {
		t.Errorf("unexpected orphaned load balancers: %+v", report.OrphanedLoadBalancers)
	}
}

func TestAuditDriftNoLiveClustersEverythingOrphaned(t *testing.T) {
	f := &fakeDriftAPI{volumes: []Volume{{ID: "vol-1"}}}
	report, ok, err := AuditDrift(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if ok || len(report.OrphanedVolumes) != 1 {
		t.Fatalf("expected the one volume orphaned with no live clusters, got %+v", report)
	}
}
