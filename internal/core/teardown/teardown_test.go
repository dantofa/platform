package teardown

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeKube records teardown's cluster-side calls.
type fakeKube struct {
	hosts       []string
	suspended   int
	deleted     int
	stopped     int
	suspendErr  error
	deleteErr   error
	suspendSeen bool
	deleteSeen  bool
	stopSeen    bool
}

func (f *fakeKube) IngressHosts(context.Context) ([]string, error) { return f.hosts, nil }
func (f *fakeKube) SuspendKustomizations(context.Context) (int, error) {
	f.suspendSeen = true
	return f.suspended, f.suspendErr
}

func (f *fakeKube) DeleteIngresses(context.Context) (int, error) {
	f.deleteSeen = true
	return f.deleted, f.deleteErr
}

func (f *fakeKube) StopTunnelController(context.Context) (int, error) {
	f.stopSeen = true
	return f.stopped, nil
}

// fakeDNS simulates records that disappear after a number of polls (drainAfter),
// or never (0), plus a force-delete path.
type fakeDNS struct {
	drainAfter   int // records gone once RecordedHosts has been called this many times
	polls        int
	forceDeleted int
	forceClears  bool // force delete actually removes the records
	deleteSeen   bool
}

func (f *fakeDNS) RecordedHosts(_ context.Context, _ string, hosts []string) ([]string, error) {
	f.polls++
	if f.deleteSeen && f.forceClears {
		return nil, nil
	}
	if f.drainAfter > 0 && f.polls >= f.drainAfter {
		return nil, nil
	}
	return hosts, nil
}

func (f *fakeDNS) DeleteHostRecords(_ context.Context, _ string, hosts []string) (int, error) {
	f.deleteSeen = true
	f.forceDeleted = len(hosts)
	return len(hosts), nil
}

func opts() Options {
	return Options{Zone: "dantofa.dev", Timeout: 100 * time.Millisecond, Interval: time.Millisecond}
}

func TestTeardownNoIngressIsNoop(t *testing.T) {
	k := &fakeKube{hosts: nil}
	d := &fakeDNS{}
	res, err := Teardown(context.Background(), k, d, opts())
	if err != nil {
		t.Fatalf("Teardown: %v", err)
	}
	if !res.Drained {
		t.Error("expected Drained on an empty cluster")
	}
	if k.suspendSeen || k.deleteSeen {
		t.Error("nothing should be suspended/deleted when there are no ingresses")
	}
}

func TestTeardownControllerDrains(t *testing.T) {
	k := &fakeKube{hosts: []string{"a.dantofa.dev"}, suspended: 5, deleted: 1}
	d := &fakeDNS{drainAfter: 2} // gone by the 2nd poll
	res, err := Teardown(context.Background(), k, d, opts())
	if err != nil {
		t.Fatalf("Teardown: %v", err)
	}
	if !res.Drained || res.SuspendedKustomizations != 5 || res.DeletedIngresses != 1 {
		t.Fatalf("unexpected result: %+v", res)
	}
	if d.deleteSeen {
		t.Error("force delete should not run when the controller drains in time")
	}
	if res.ForceDeletedRecords != 0 {
		t.Errorf("ForceDeletedRecords = %d, want 0", res.ForceDeletedRecords)
	}
}

func TestTeardownFallbackForceDeletes(t *testing.T) {
	k := &fakeKube{hosts: []string{"a.dantofa.dev", "b.dantofa.dev"}, deleted: 2}
	d := &fakeDNS{drainAfter: 0, forceClears: true} // controller never drains
	res, err := Teardown(context.Background(), k, d, opts())
	if err != nil {
		t.Fatalf("Teardown: %v", err)
	}
	if !res.Drained {
		t.Error("expected Drained after successful force delete")
	}
	if res.ForceDeletedRecords != 2 {
		t.Errorf("ForceDeletedRecords = %d, want 2", res.ForceDeletedRecords)
	}
}

func TestTeardownFailsWhenRecordsPersist(t *testing.T) {
	k := &fakeKube{hosts: []string{"a.dantofa.dev"}}
	d := &fakeDNS{drainAfter: 0, forceClears: false} // even force delete doesn't clear
	_, err := Teardown(context.Background(), k, d, opts())
	if err == nil {
		t.Fatal("expected an error when records persist after force delete")
	}
}

func TestTeardownSuspendError(t *testing.T) {
	sentinel := errors.New("suspend boom")
	k := &fakeKube{hosts: []string{"a.dantofa.dev"}, suspendErr: sentinel}
	_, err := Teardown(context.Background(), k, &fakeDNS{}, opts())
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected suspend error, got %v", err)
	}
	if k.deleteSeen {
		t.Error("ingresses must not be deleted after a suspend failure")
	}
}
