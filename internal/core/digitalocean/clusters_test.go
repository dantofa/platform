package digitalocean

import (
	"context"
	"errors"
	"io"
	"reflect"
	"slices"
	"testing"
	"time"
)

type fakeClusterAPI struct {
	clusters                 []Cluster
	getStates                []string // successive states returned by Get (for wait)
	getCalls                 int
	created                  CreateSpec
	updatedID                string
	updateSpec               UpdateSpec
	associatedResources      AssociatedResources // returned by AssociatedResources
	associatedResourcesCalls int
	deletedID                string
	deletedResources         AssociatedResources // passed to DeleteSelective
}

func (f *fakeClusterAPI) List(context.Context) ([]Cluster, error) { return f.clusters, nil }
func (f *fakeClusterAPI) Create(_ context.Context, spec CreateSpec) (Cluster, error) {
	f.created = spec
	return Cluster{ID: "new", Name: spec.Name}, nil
}

func (f *fakeClusterAPI) Update(_ context.Context, id string, spec UpdateSpec) (Cluster, error) {
	f.updatedID, f.updateSpec = id, spec
	return Cluster{ID: id, Name: spec.Name}, nil
}

func (f *fakeClusterAPI) AssociatedResources(_ context.Context, _ string) (AssociatedResources, error) {
	f.associatedResourcesCalls++
	return f.associatedResources, nil
}

func (f *fakeClusterAPI) DeleteSelective(_ context.Context, id string, resources AssociatedResources) error {
	f.deletedID, f.deletedResources = id, resources
	return nil
}

func (f *fakeClusterAPI) Get(context.Context, string) (Cluster, error) {
	state := f.getStates[f.getCalls]
	if f.getCalls < len(f.getStates)-1 {
		f.getCalls++
	}
	return Cluster{ID: "c1", Name: "c1", State: state}, nil
}

func (f *fakeClusterAPI) GetKubeconfig(context.Context, string) (string, error) {
	return "kubeconfig", nil
}

func TestBuildersBakeInvariants(t *testing.T) {
	pool := BuildNodePool("system", "s-2vcpu-4gb", 2, 2, 10)
	if !pool.AutoScale {
		t.Error("node pool autoscale must always be on")
	}
	spec := BuildCreateSpec("c", "nyc3", "latest", pool, nil, false)
	if !spec.AutoUpgrade || !spec.SurgeUpgrade {
		t.Error("create must always enable auto-upgrade and surge-upgrade")
	}
	if len(spec.NodePools) != 1 || spec.Tags == nil {
		t.Errorf("create spec node pools/tags wrong: %+v", spec)
	}
	up := BuildUpdateSpec(nil, false)
	if !up.AutoUpgrade || !up.SurgeUpgrade {
		t.Error("update must always re-assert auto-upgrade and surge-upgrade")
	}
}

func TestUpdateClusterResolvesNameAndResendsName(t *testing.T) {
	f := &fakeClusterAPI{clusters: []Cluster{{ID: "id-1", Name: "prod"}}}
	tags := []string{"a"}
	_, err := UpdateCluster(context.Background(), f, "prod", BuildUpdateSpec(&tags, true))
	if err != nil {
		t.Fatal(err)
	}
	if f.updatedID != "id-1" {
		t.Errorf("expected id-1, got %q", f.updatedID)
	}
	if f.updateSpec.Name != "prod" {
		t.Errorf("update must re-send name, got %q", f.updateSpec.Name)
	}
}

func TestUpdateClusterNotFound(t *testing.T) {
	f := &fakeClusterAPI{}
	_, err := UpdateCluster(context.Background(), f, "ghost", UpdateSpec{})
	var nf *ClusterNotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("expected ClusterNotFoundError, got %v", err)
	}
}

// fakeDeleteAPI extends fakeClusterAPI with the Volume listing/deletion
// DeleteCluster's tag sweep needs. volumeListings is what successive
// ListVolumes calls return (clamped to the last entry), minus anything already
// passed to DeleteVolume -- so a sweep that deletes a Volume stops seeing it.
// listErrs, when set, makes the matching call fail instead.
type fakeDeleteAPI struct {
	fakeClusterAPI
	volumeListings [][]Volume
	listErrs       []error
	listCalls      int
	deletedVolumes []string
}

func (f *fakeDeleteAPI) ListVolumes(context.Context) ([]Volume, error) {
	call := f.listCalls
	f.listCalls++
	if call < len(f.listErrs) && f.listErrs[call] != nil {
		return nil, f.listErrs[call]
	}
	if len(f.volumeListings) == 0 {
		return nil, nil
	}
	listing := f.volumeListings[min(call, len(f.volumeListings)-1)]
	var out []Volume
	for _, v := range listing {
		if !slices.Contains(f.deletedVolumes, v.ID) {
			out = append(out, v)
		}
	}
	return out, nil
}

func (f *fakeDeleteAPI) DeleteVolume(_ context.Context, id string) error {
	f.deletedVolumes = append(f.deletedVolumes, id)
	return nil
}

// testReapTimeout/testReapInterval keep the sweep tests fast.
const (
	testReapTimeout  = time.Second
	testReapInterval = time.Millisecond
)

func TestDeleteClusterIdempotent(t *testing.T) {
	f := &fakeDeleteAPI{}
	res, err := DeleteCluster(context.Background(), f, "ghost", testReapTimeout, testReapInterval)
	if err != nil || res.ID != "" {
		t.Fatalf("missing cluster should be a no-op, got id=%q err=%v", res.ID, err)
	}
	if f.associatedResourcesCalls != 0 || f.listCalls != 0 {
		t.Errorf("a missing cluster must not be inspected, got %d AssociatedResources / %d ListVolumes calls",
			f.associatedResourcesCalls, f.listCalls)
	}
	f.clusters = []Cluster{{ID: "id-9", Name: "prod"}}
	res, err = DeleteCluster(context.Background(), f, "prod", testReapTimeout, testReapInterval)
	if err != nil || res.ID != "id-9" || f.deletedID != "id-9" {
		t.Fatalf("expected id-9 deleted, got id=%q deleted=%q err=%v", res.ID, f.deletedID, err)
	}
	if len(res.ReapedVolumeIDs) != 0 {
		t.Errorf("nothing tagged, so nothing to reap; got %v", res.ReapedVolumeIDs)
	}
}

func TestDeleteClusterReapsAssociatedResources(t *testing.T) {
	f := &fakeDeleteAPI{fakeClusterAPI: fakeClusterAPI{
		clusters: []Cluster{{ID: "id-9", Name: "prod"}},
		associatedResources: AssociatedResources{
			VolumeIDs:       []string{"vol-1", "vol-2"},
			LoadBalancerIDs: []string{"lb-1"},
		},
	}}
	res, err := DeleteCluster(context.Background(), f, "prod", testReapTimeout, testReapInterval)
	if err != nil {
		t.Fatal(err)
	}
	want := AssociatedResources{VolumeIDs: []string{"vol-1", "vol-2"}, LoadBalancerIDs: []string{"lb-1"}}
	if !reflect.DeepEqual(f.deletedResources, want) {
		t.Errorf("DeleteSelective got resources %+v, want %+v", f.deletedResources, want)
	}
	if !reflect.DeepEqual(res.Associated, want) {
		t.Errorf("result must report what DeleteSelective was given: got %+v, want %+v", res.Associated, want)
	}
}

// The leak this guards against: the cluster's PVC Volumes survived the
// selective delete, still tagged k8s:<cluster-id> and detached. Only that
// cluster's tagged Volumes are swept -- never another cluster's, never an
// untagged one.
func TestDeleteClusterReapsLeftoverTaggedVolumes(t *testing.T) {
	f := &fakeDeleteAPI{
		fakeClusterAPI: fakeClusterAPI{clusters: []Cluster{{ID: "id-9", Name: "prod"}}},
		volumeListings: [][]Volume{{
			{ID: "vol-leaked", Tags: []string{"k8s:id-9"}},
			{ID: "vol-other-cluster", Tags: []string{"k8s:id-1"}, DropletIDs: []int{7}},
			{ID: "vol-untagged"},
		}},
	}
	res, err := DeleteCluster(context.Background(), f, "prod", testReapTimeout, testReapInterval)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"vol-leaked"}; !reflect.DeepEqual(f.deletedVolumes, want) || !reflect.DeepEqual(res.ReapedVolumeIDs, want) {
		t.Errorf("deleted %v, reported %v; want only %v", f.deletedVolumes, res.ReapedVolumeIDs, want)
	}
}

// A Volume can't be deleted while still attached to a node the cluster delete
// is tearing down: the sweep waits for it to detach rather than failing.
func TestDeleteClusterWaitsForTaggedVolumesToDetach(t *testing.T) {
	attached := Volume{ID: "vol-1", Tags: []string{"k8s:id-9"}, DropletIDs: []int{42}}
	detached := Volume{ID: "vol-1", Tags: []string{"k8s:id-9"}}
	f := &fakeDeleteAPI{
		fakeClusterAPI: fakeClusterAPI{clusters: []Cluster{{ID: "id-9", Name: "prod"}}},
		volumeListings: [][]Volume{{attached}, {attached}, {detached}},
	}
	res, err := DeleteCluster(context.Background(), f, "prod", testReapTimeout, testReapInterval)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(res.ReapedVolumeIDs, []string{"vol-1"}) {
		t.Errorf("expected vol-1 reaped once detached, got %v", res.ReapedVolumeIDs)
	}
	if f.listCalls != 3 {
		t.Errorf("expected 3 sweeps (two while attached, one deleting it), got %d", f.listCalls)
	}
}

func TestDeleteClusterFailsWhenTaggedVolumesOutlastTimeout(t *testing.T) {
	f := &fakeDeleteAPI{
		fakeClusterAPI: fakeClusterAPI{clusters: []Cluster{{ID: "id-9", Name: "prod"}}},
		volumeListings: [][]Volume{{{ID: "vol-1", Tags: []string{"k8s:id-9"}, DropletIDs: []int{42}}}},
	}
	res, err := DeleteCluster(context.Background(), f, "prod", 0, testReapInterval)
	var notReaped *VolumesNotReapedError
	if !errors.As(err, &notReaped) || !reflect.DeepEqual(notReaped.VolumeIDs, []string{"vol-1"}) {
		t.Fatalf("expected VolumesNotReapedError for vol-1, got %v", err)
	}
	if res.ID != "id-9" {
		t.Errorf("the cluster was still deleted, so the result must say so; got id=%q", res.ID)
	}
}

// A dropped connection mid-sweep must not leave the Volumes behind.
func TestDeleteClusterRetriesTransientVolumeListError(t *testing.T) {
	f := &fakeDeleteAPI{
		fakeClusterAPI: fakeClusterAPI{clusters: []Cluster{{ID: "id-9", Name: "prod"}}},
		volumeListings: [][]Volume{{{ID: "vol-1", Tags: []string{"k8s:id-9"}}}},
		listErrs:       []error{io.EOF},
	}
	res, err := DeleteCluster(context.Background(), f, "prod", testReapTimeout, testReapInterval)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(res.ReapedVolumeIDs, []string{"vol-1"}) {
		t.Errorf("expected vol-1 reaped after the retry, got %v", res.ReapedVolumeIDs)
	}
}

// A real API error is an answer: it fails fast instead of costing the timeout.
func TestDeleteClusterFailsFastOnVolumeListError(t *testing.T) {
	sentinel := errors.New("forbidden")
	f := &fakeDeleteAPI{
		fakeClusterAPI: fakeClusterAPI{clusters: []Cluster{{ID: "id-9", Name: "prod"}}},
		listErrs:       []error{sentinel},
	}
	if _, err := DeleteCluster(context.Background(), f, "prod", time.Hour, testReapInterval); !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want %v", err, sentinel)
	}
	if f.listCalls != 1 {
		t.Errorf("a non-transient error must not be retried, got %d ListVolumes calls", f.listCalls)
	}
}

func TestDeleteClusterPropagatesAssociatedResourcesError(t *testing.T) {
	sentinel := errors.New("boom")
	f := &erroringAssociatedResourcesAPI{
		fakeDeleteAPI: fakeDeleteAPI{fakeClusterAPI: fakeClusterAPI{clusters: []Cluster{{ID: "id-9", Name: "prod"}}}},
		err:           sentinel,
	}
	if _, err := DeleteCluster(context.Background(), f, "prod", testReapTimeout, testReapInterval); !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want %v", err, sentinel)
	}
	if f.deletedID != "" {
		t.Error("DeleteSelective must not be called when AssociatedResources errors")
	}
}

// erroringAssociatedResourcesAPI overrides AssociatedResources to always fail,
// so DeleteCluster's error path is exercised distinctly from the happy path
// above.
type erroringAssociatedResourcesAPI struct {
	fakeDeleteAPI
	err error
}

func (f *erroringAssociatedResourcesAPI) AssociatedResources(context.Context, string) (AssociatedResources, error) {
	return AssociatedResources{}, f.err
}

func TestWaitForRunning(t *testing.T) {
	f := &fakeClusterAPI{clusters: []Cluster{{ID: "c1", Name: "c1"}}, getStates: []string{"provisioning", "provisioning", "running"}}
	c, err := WaitForRunning(context.Background(), f, "c1", time.Second, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if c.State != "running" {
		t.Errorf("expected running, got %q", c.State)
	}
}

func TestWaitForRunningTerminalState(t *testing.T) {
	f := &fakeClusterAPI{clusters: []Cluster{{ID: "c1", Name: "c1"}}, getStates: []string{"error"}}
	_, err := WaitForRunning(context.Background(), f, "c1", time.Second, time.Millisecond)
	var nr *ClusterNotReadyError
	if !errors.As(err, &nr) || nr.TimedOut {
		t.Fatalf("expected non-timeout ClusterNotReadyError, got %v", err)
	}
}

func TestWaitForRunningTimeout(t *testing.T) {
	f := &fakeClusterAPI{clusters: []Cluster{{ID: "c1", Name: "c1"}}, getStates: []string{"provisioning"}}
	_, err := WaitForRunning(context.Background(), f, "c1", time.Millisecond, time.Millisecond)
	var nr *ClusterNotReadyError
	if !errors.As(err, &nr) || !nr.TimedOut {
		t.Fatalf("expected timeout ClusterNotReadyError, got %v", err)
	}
}
