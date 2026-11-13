package digitalocean

import (
	"context"
	"testing"

	"github.com/dantofa/platform/internal/core/edgecert"
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

type fakeCerts struct{ packs map[string][]edgecert.Pack }

func (f fakeCerts) ListCertificatePacks(_ context.Context, zone string) ([]edgecert.Pack, error) {
	return f.packs[zone], nil
}

func (fakeCerts) OrderCertificatePack(context.Context, string, []string) (edgecert.Pack, error) {
	return edgecert.Pack{}, nil
}

func (fakeCerts) GetCertificatePack(context.Context, string, string) (edgecert.Pack, error) {
	return edgecert.Pack{}, nil
}
func (fakeCerts) DeleteCertificatePack(context.Context, string, string) error { return nil }

func TestAuditEdgeCertsReportsOnlyDeadClustersCerts(t *testing.T) {
	api := fakeCerts{packs: map[string][]edgecert.Pack{"dantofa.dev": {
		{ID: "shared", Status: "active", Hosts: edgecert.Hosts("preview.dantofa.dev")},
		{ID: "live", Status: "active", Hosts: edgecert.Hosts("auth-pr1.preview.dantofa.dev")},
		{ID: "dead", Status: "active", Hosts: edgecert.Hosts("auth-pr2.preview.dantofa.dev")},
	}}}
	got, err := AuditEdgeCerts(context.Background(), api, []string{"dantofa.dev"}, []Cluster{{Name: "auth-pr1"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "dead" || got[0].Zone != "dantofa.dev" {
		t.Errorf("got %+v, want only the dead cluster's certificate", got)
	}
	if (DriftReport{OrphanedEdgeCerts: got}).Empty() {
		t.Error("an orphaned certificate must make the report non-empty so the audit exits non-zero")
	}
}
