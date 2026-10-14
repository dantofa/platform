package digitalocean

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

type fakeClusterAPI struct {
	clusters            []Cluster
	getStates           []string // successive states returned by Get (for wait)
	getCalls            int
	created             CreateSpec
	updatedID           string
	updateSpec          UpdateSpec
	associatedResources AssociatedResources // returned by AssociatedResources when associatedResourcesSequence is nil
	// associatedResourcesSequence, when set, returns a different result per
	// AssociatedResources call (by call number, 1-indexed; clamped to the last
	// entry past the end) -- simulates a resource that only appears once DO's
	// enumeration catches up on a later poll.
	associatedResourcesSequence []AssociatedResources
	associatedResourcesCalls    int
	deletedID                   string
	deletedResources            AssociatedResources // passed to DeleteSelective
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
	if f.associatedResourcesSequence == nil {
		return f.associatedResources, nil
	}
	idx := f.associatedResourcesCalls - 1
	if idx >= len(f.associatedResourcesSequence) {
		idx = len(f.associatedResourcesSequence) - 1
	}
	return f.associatedResourcesSequence[idx], nil
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

// testPollAttempts/testPollInterval keep the poll tests fast: real production
// use (DefaultAssociatedResourcesPoll{Attempts,Interval}) is 3 attempts over a
// deliberate 5s gap, which would make every test calling DeleteCluster take
// ~10s if used directly.
const (
	testPollAttempts = 3
	testPollInterval = time.Millisecond
)

func TestDeleteClusterIdempotent(t *testing.T) {
	f := &fakeClusterAPI{}
	id, err := DeleteCluster(context.Background(), f, "ghost", testPollAttempts, testPollInterval)
	if err != nil || id != "" {
		t.Fatalf("missing cluster should be a no-op, got id=%q err=%v", id, err)
	}
	if f.associatedResourcesCalls != 0 {
		t.Errorf("a missing cluster must not poll AssociatedResources, got %d calls", f.associatedResourcesCalls)
	}
	f.clusters = []Cluster{{ID: "id-9", Name: "prod"}}
	id, err = DeleteCluster(context.Background(), f, "prod", testPollAttempts, testPollInterval)
	if err != nil || id != "id-9" || f.deletedID != "id-9" {
		t.Fatalf("expected id-9 deleted, got id=%q deleted=%q err=%v", id, f.deletedID, err)
	}
}

func TestDeleteClusterReapsAssociatedResources(t *testing.T) {
	f := &fakeClusterAPI{
		clusters: []Cluster{{ID: "id-9", Name: "prod"}},
		associatedResources: AssociatedResources{
			VolumeIDs:       []string{"vol-1", "vol-2"},
			LoadBalancerIDs: []string{"lb-1"},
		},
	}
	if _, err := DeleteCluster(context.Background(), f, "prod", testPollAttempts, testPollInterval); err != nil {
		t.Fatal(err)
	}
	want := AssociatedResources{VolumeIDs: []string{"vol-1", "vol-2"}, LoadBalancerIDs: []string{"lb-1"}}
	if !reflect.DeepEqual(f.deletedResources, want) {
		t.Errorf("DeleteSelective got resources %+v, want %+v", f.deletedResources, want)
	}
}

// The actual bug this guards against: a Volume provisioned moments before
// delete (e.g. a CNPG PITR drill's PVC) that DO's AssociatedResources
// enumeration had not yet indexed on the first call, but had by a later one.
// A single-call DeleteCluster would have deleted the cluster without this
// Volume, leaking it exactly as happened on three real CI runs.
func TestDeleteClusterUnionsResourcesAcrossPolls(t *testing.T) {
	f := &fakeClusterAPI{
		clusters: []Cluster{{ID: "id-9", Name: "prod"}},
		associatedResourcesSequence: []AssociatedResources{
			{VolumeIDs: []string{"vol-1"}},               // 1st poll: misses the late volume
			{VolumeIDs: []string{"vol-1", "vol-2-late"}}, // 2nd poll: DO caught up
			{VolumeIDs: []string{"vol-1", "vol-2-late"}}, // 3rd poll: unchanged
		},
	}
	if _, err := DeleteCluster(context.Background(), f, "prod", testPollAttempts, testPollInterval); err != nil {
		t.Fatal(err)
	}
	if f.associatedResourcesCalls != testPollAttempts {
		t.Errorf("expected %d polls, got %d", testPollAttempts, f.associatedResourcesCalls)
	}
	want := AssociatedResources{VolumeIDs: []string{"vol-1", "vol-2-late"}}
	if !reflect.DeepEqual(f.deletedResources, want) {
		t.Errorf("DeleteSelective got resources %+v, want the union %+v", f.deletedResources, want)
	}
}

// A resource must be deleted at most once even if every poll reports it --
// appendUnseen's dedup, exercised through the public DeleteCluster path.
func TestDeleteClusterDedupsRepeatedResourcesAcrossPolls(t *testing.T) {
	f := &fakeClusterAPI{
		clusters:            []Cluster{{ID: "id-9", Name: "prod"}},
		associatedResources: AssociatedResources{VolumeIDs: []string{"vol-1"}, LoadBalancerIDs: []string{"lb-1"}},
	}
	if _, err := DeleteCluster(context.Background(), f, "prod", testPollAttempts, testPollInterval); err != nil {
		t.Fatal(err)
	}
	want := AssociatedResources{VolumeIDs: []string{"vol-1"}, LoadBalancerIDs: []string{"lb-1"}}
	if !reflect.DeepEqual(f.deletedResources, want) {
		t.Errorf("expected each resource exactly once despite %d identical polls, got %+v", f.associatedResourcesCalls, f.deletedResources)
	}
}

func TestDeleteClusterPropagatesAssociatedResourcesError(t *testing.T) {
	sentinel := errors.New("boom")
	f := &erroringAssociatedResourcesAPI{
		fakeClusterAPI: fakeClusterAPI{clusters: []Cluster{{ID: "id-9", Name: "prod"}}},
		err:            sentinel,
	}
	if _, err := DeleteCluster(context.Background(), f, "prod", testPollAttempts, testPollInterval); !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want %v", err, sentinel)
	}
	if f.deletedID != "" {
		t.Error("DeleteSelective must not be called when the poll errors")
	}
}

// erroringAssociatedResourcesAPI overrides AssociatedResources to always fail,
// so DeleteCluster's error path (mid-poll) is exercised distinctly from the
// happy path above.
type erroringAssociatedResourcesAPI struct {
	fakeClusterAPI
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
