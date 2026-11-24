package digitalocean

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

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

// pruneCerts is a stateful certificate fake: deletes remove packs, failIDs fail.
type pruneCerts struct {
	fakeCerts
	failIDs map[string]error
	deleted []string
}

func (f *pruneCerts) DeleteCertificatePack(_ context.Context, _, id string) error {
	if err := f.failIDs[id]; err != nil {
		return err
	}
	f.deleted = append(f.deleted, id)
	return nil
}

// clustersSeq returns its lists in order, one per call (the last repeats), to
// model clusters appearing between the audit and the prune's re-check.
type clustersSeq struct{ lists [][]Cluster }

func (c *clustersSeq) List(context.Context) ([]Cluster, error) {
	l := c.lists[0]
	if len(c.lists) > 1 {
		c.lists = c.lists[1:]
	}
	return l, nil
}

var pruneNow = time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)

func oldPack(id, base string) edgecert.Pack {
	return edgecert.Pack{ID: id, Status: "active", Hosts: edgecert.Hosts(base), CreatedOn: pruneNow.Add(-3 * time.Hour)}
}

func outcomes(got []OrphanedEdgeCert) map[string]string {
	m := map[string]string{}
	for _, c := range got {
		m[c.ID] = c.Outcome
	}
	return m
}

// The prune deletes exactly what the audit reports: a shared wildcard and a live
// cluster's certificate are never candidates, so never touched.
func TestPruneEdgeCertsDeletesOnlyOrphans(t *testing.T) {
	api := &pruneCerts{fakeCerts: fakeCerts{packs: map[string][]edgecert.Pack{"dantofa.dev": {
		oldPack("shared", "preview.dantofa.dev"),
		oldPack("live", "auth-pr1.preview.dantofa.dev"),
		oldPack("dead", "auth-pr2.preview.dantofa.dev"),
	}}}}
	live := []Cluster{{Name: "auth-pr1"}}
	orphans, err := AuditEdgeCerts(context.Background(), api, []string{"dantofa.dev"}, live)
	if err != nil {
		t.Fatal(err)
	}
	got := PruneEdgeCerts(context.Background(), api, &clustersSeq{lists: [][]Cluster{live}}, orphans, time.Hour, pruneNow)
	if len(api.deleted) != 1 || api.deleted[0] != "dead" {
		t.Fatalf("deleted %v, want only [dead]", api.deleted)
	}
	if o := outcomes(got); len(o) != 1 || o["dead"] != PruneDeleted {
		t.Errorf("outcomes %v", o)
	}
	if !(DriftReport{OrphanedEdgeCerts: got}).Empty() {
		t.Error("everything pruned: the report must count as clean")
	}
}

// Certificates ordered without a cluster (the CI drill) look orphaned while they
// live; the grace period, and an unknown age, keep them.
func TestPruneEdgeCertsSkipsYoungAndUnknownAge(t *testing.T) {
	young := edgecert.Pack{ID: "young", Status: "active", Hosts: edgecert.Hosts("edgecert-ci-1.preview.dantofa.dev"), CreatedOn: pruneNow.Add(-10 * time.Minute)}
	unknown := edgecert.Pack{ID: "unknown", Status: "active", Hosts: edgecert.Hosts("x.preview.dantofa.dev")}
	api := &pruneCerts{}
	got := PruneEdgeCerts(context.Background(), api, &clustersSeq{lists: [][]Cluster{nil}},
		[]OrphanedEdgeCert{{Zone: "dantofa.dev", Pack: young}, {Zone: "dantofa.dev", Pack: unknown}}, time.Hour, pruneNow)
	if len(api.deleted) != 0 {
		t.Fatalf("deleted %v, want none", api.deleted)
	}
	for _, c := range got {
		if c.Outcome != PruneSkipped || c.Reason == "" {
			t.Errorf("%s: outcome %q reason %q, want skipped with a reason", c.ID, c.Outcome, c.Reason)
		}
	}
	if (DriftReport{OrphanedEdgeCerts: got}).Empty() {
		t.Error("skipped certificates are left over: the report must stay non-empty")
	}
}

// The audit's cluster list and the delete are not atomic: a cluster that appears
// in between keeps its certificate.
func TestPruneEdgeCertsRechecksLiveClusters(t *testing.T) {
	api := &pruneCerts{}
	orphans := []OrphanedEdgeCert{{Zone: "dantofa.dev", Pack: oldPack("p", "auth-pr9.preview.dantofa.dev")}}
	got := PruneEdgeCerts(context.Background(), api, &clustersSeq{lists: [][]Cluster{{{Name: "auth-pr9"}}}}, orphans, time.Hour, pruneNow)
	if len(api.deleted) != 0 || got[0].Outcome != PruneSkipped {
		t.Fatalf("deleted %v, outcome %q; want the now-live cluster's certificate kept", api.deleted, got[0].Outcome)
	}
}

// One failed delete is reported, does not stop the others, and keeps the exit
// non-zero.
func TestPruneEdgeCertsPartialFailure(t *testing.T) {
	api := &pruneCerts{failIDs: map[string]error{"a": errors.New("403 forbidden")}}
	orphans := []OrphanedEdgeCert{
		{Zone: "dantofa.dev", Pack: oldPack("a", "a.preview.dantofa.dev")},
		{Zone: "dantofa.dev", Pack: oldPack("b", "b.preview.dantofa.dev")},
	}
	got := PruneEdgeCerts(context.Background(), api, &clustersSeq{lists: [][]Cluster{nil}}, orphans, time.Hour, pruneNow)
	if o := outcomes(got); o["a"] != PruneFailed || o["b"] != PruneDeleted {
		t.Fatalf("outcomes %v", o)
	}
	if got[0].Reason == "" {
		t.Error("a failure must carry its error")
	}
	if (DriftReport{OrphanedEdgeCerts: got}).Empty() {
		t.Error("a failed delete must keep the report non-empty")
	}
}

// A dropped connection on the delete is retried rather than reported as failed.
func TestPruneEdgeCertsRetriesTransientDelete(t *testing.T) {
	api := &flakyDelete{}
	orphans := []OrphanedEdgeCert{{Zone: "dantofa.dev", Pack: oldPack("p", "a.preview.dantofa.dev")}}
	got := PruneEdgeCerts(context.Background(), api, &clustersSeq{lists: [][]Cluster{nil}}, orphans, time.Hour, pruneNow)
	if got[0].Outcome != PruneDeleted || api.calls != 2 {
		t.Fatalf("outcome %q after %d calls, want deleted on the retry", got[0].Outcome, api.calls)
	}
}

type flakyDelete struct {
	fakeCerts
	calls int
}

func (f *flakyDelete) DeleteCertificatePack(context.Context, string, string) error {
	f.calls++
	if f.calls == 1 {
		return io.ErrUnexpectedEOF
	}
	return nil
}
