package teardown

import (
	"context"
	"strings"
	"testing"
	"time"
)

func suspendVia(k *fakeKube) func(context.Context) error {
	return func(ctx context.Context) error {
		_, err := k.SuspendKustomizations(ctx)
		return err
	}
}

// A cluster without Terraform objects (or without the CRD) is left untouched:
// in particular Flux is not suspended on its account.
func TestDrainTerraformNothingToDrain(t *testing.T) {
	k := &fakeKube{}
	n, err := DrainTerraform(context.Background(), k, suspendVia(k), time.Second, time.Millisecond)
	if err != nil || n != 0 || k.suspendSeen || k.tfDeleteSeen {
		t.Fatalf("n=%d err=%v suspended=%v deleted=%v; want a no-op", n, err, k.suspendSeen, k.tfDeleteSeen)
	}
}

// Flux is suspended before the delete (or it re-applies the objects), and the
// drain waits until they are actually gone, i.e. their destroy has finished.
func TestDrainTerraformSuspendsDeletesAndWaits(t *testing.T) {
	k := &fakeKube{tf: 2, tfDrainPolls: 3}
	n, err := DrainTerraform(context.Background(), k, suspendVia(k), time.Second, time.Millisecond)
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v, want 2 drained", n, err)
	}
	if strings.Join(k.tfOrder, ",") != "suspend,delete-tf" {
		t.Errorf("call order %v, want suspend then delete", k.tfOrder)
	}
	if k.tfDrainPolls != 0 {
		t.Error("returned before the objects were gone")
	}
}

func TestDrainTerraformStuckObjectsFail(t *testing.T) {
	k := &fakeKube{tf: 1, tfStuck: true}
	_, err := DrainTerraform(context.Background(), k, suspendVia(k), 20*time.Millisecond, time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "still present") {
		t.Fatalf("err=%v, want a still-present error", err)
	}
}

func withDrainTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	old := terraformDrainTimeout
	terraformDrainTimeout = d
	t.Cleanup(func() { terraformDrainTimeout = old })
}

// The drain is not gated on the ingress stack: a cluster with no cloudflare-api
// secret still gets its Terraform objects destroyed.
func TestRunDrainsTerraformWithoutIngressStack(t *testing.T) {
	r := &fakeReader{configMaps: zoneCM()}
	k := &fakeKube{tf: 1, suspended: 7}
	res, err := Run(context.Background(), r, k, func(string) (CloudflareAPI, error) { return &fakeCF{}, nil }, nil,
		50*time.Millisecond, time.Millisecond)
	if err != nil || !res.Skipped || res.TerraformDeleted != 1 {
		t.Fatalf("res=%+v err=%v; want the ingress part skipped and 1 Terraform object drained", res, err)
	}
	if res.SuspendedKustomizations != 7 {
		t.Errorf("suspended_kustomizations = %d, want the drain's 7 reported", res.SuspendedKustomizations)
	}
}

// A stuck drain fails the teardown, but the ingress drain still runs (under
// --force the delete goes ahead, and DNS must not leak on top).
func TestRunTerraformFailureStillDrainsIngress(t *testing.T) {
	withDrainTimeout(t, 20*time.Millisecond)
	r := &fakeReader{
		secrets:    map[string]string{"external-dns/cloudflare-api/api_token": "tok"},
		configMaps: zoneCM(),
	}
	k := &fakeKube{hosts: []string{"a.dantofa.dev"}, deleted: 1, tf: 1, tfStuck: true}
	cf := &fakeCF{}
	res, err := Run(context.Background(), r, k, func(string) (CloudflareAPI, error) { return cf, nil }, nil,
		50*time.Millisecond, time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "terraform") {
		t.Fatalf("err=%v, want the terraform failure reported", err)
	}
	if !k.deleteSeen || res.DeletedIngresses != 1 {
		t.Errorf("ingress drain skipped after a terraform failure: %+v", res)
	}
}
