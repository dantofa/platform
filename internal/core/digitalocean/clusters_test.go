package digitalocean

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeClusterAPI struct {
	clusters   []Cluster
	getStates  []string // successive states returned by Get (for wait)
	getCalls   int
	created    CreateSpec
	updatedID  string
	updateSpec UpdateSpec
	deletedID  string
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
func (f *fakeClusterAPI) Delete(_ context.Context, id string) error { f.deletedID = id; return nil }
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

func TestDeleteClusterIdempotent(t *testing.T) {
	f := &fakeClusterAPI{}
	id, err := DeleteCluster(context.Background(), f, "ghost")
	if err != nil || id != "" {
		t.Fatalf("missing cluster should be a no-op, got id=%q err=%v", id, err)
	}
	f.clusters = []Cluster{{ID: "id-9", Name: "prod"}}
	id, err = DeleteCluster(context.Background(), f, "prod")
	if err != nil || id != "id-9" || f.deletedID != "id-9" {
		t.Fatalf("expected id-9 deleted, got id=%q deleted=%q err=%v", id, f.deletedID, err)
	}
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
