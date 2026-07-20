package teardown

import (
	"context"
	"testing"
	"time"

	fluxcore "github.com/dantofa/platform/internal/core/flux"
)

// fakeReader serves cluster-vars and cloudflare-api values from in-memory maps
// keyed "namespace/name/key".
type fakeReader struct {
	secrets    map[string]string
	configMaps map[string]string
}

func (f *fakeReader) SecretValue(_ context.Context, ns, name, key string) (string, error) {
	return f.secrets[ns+"/"+name+"/"+key], nil
}

func (f *fakeReader) ConfigMapValue(_ context.Context, ns, name, key string) (string, error) {
	return f.configMaps[ns+"/"+name+"/"+key], nil
}

// fakeCF satisfies CloudflareAPI: no records (drains immediately), records the
// tunnel delete call.
type fakeCF struct {
	tunnelName    string
	tunnelDeleted bool
}

func (f *fakeCF) RecordedHosts(context.Context, string, []string) ([]string, error) {
	return nil, nil
}
func (f *fakeCF) DeleteHostRecords(context.Context, string, []string) (int, error) { return 0, nil }
func (f *fakeCF) DeleteTunnelByName(_ context.Context, _, name string) (bool, error) {
	f.tunnelName = name
	f.tunnelDeleted = true
	return true, nil
}

func zoneCM() map[string]string {
	return map[string]string{
		"flux-system/" + fluxcore.ClusterVarsName + "/" + fluxcore.VarDNSZone: "dantofa.dev",
	}
}

func TestResolveZonePrefersDNSZone(t *testing.T) {
	r := &fakeReader{configMaps: map[string]string{
		"flux-system/" + fluxcore.ClusterVarsName + "/" + fluxcore.VarDNSZone:    "dantofa.dev",
		"flux-system/" + fluxcore.ClusterVarsName + "/" + fluxcore.VarBaseDomain: "local.dantofa.dev",
	}}
	zone, err := ResolveZone(context.Background(), r)
	if err != nil || zone != "dantofa.dev" {
		t.Fatalf("ResolveZone = %q, %v; want dantofa.dev", zone, err)
	}
}

func TestResolveZoneFallsBackToBaseDomain(t *testing.T) {
	// A cluster bootstrapped before dns_zone existed: only base_domain is set.
	r := &fakeReader{configMaps: map[string]string{
		"flux-system/" + fluxcore.ClusterVarsName + "/" + fluxcore.VarBaseDomain: "preview.dantofa.dev",
	}}
	zone, err := ResolveZone(context.Background(), r)
	if err != nil || zone != "dantofa.dev" {
		t.Fatalf("ResolveZone fallback = %q, %v; want dantofa.dev", zone, err)
	}
}

func TestResolveZoneErrorsWhenNeitherSet(t *testing.T) {
	if _, err := ResolveZone(context.Background(), &fakeReader{}); err == nil {
		t.Fatal("expected an error when neither dns_zone nor base_domain is set")
	}
}

func TestRunSkipsWhenIngressNotBootstrapped(t *testing.T) {
	// A leftover / partially-created cluster: no cloudflare-api secret anywhere.
	// Teardown must skip (not error) so the delete proceeds without --force.
	r := &fakeReader{configMaps: zoneCM()}
	k := &fakeKube{hosts: []string{"a.dantofa.dev"}}
	cf := &fakeCF{}
	res, err := Run(context.Background(), r, k, func(string) (CloudflareAPI, error) { return cf, nil },
		50*time.Millisecond, time.Millisecond)
	if err != nil {
		t.Fatalf("Run should skip, not error: %v", err)
	}
	if !res.Skipped {
		t.Error("expected Skipped when no cloudflare-api secret exists")
	}
	if k.suspendSeen || k.deleteSeen || cf.tunnelDeleted {
		t.Error("nothing should be torn down when the ingress stack is absent")
	}
}

func TestRunReapsTunnelWhenPresent(t *testing.T) {
	r := &fakeReader{
		secrets: map[string]string{
			"external-dns/cloudflare-api/api_token":               "tok",
			"cloudflare-tunnel-system/cloudflare-api/api_token":   "tok",
			"cloudflare-tunnel-system/cloudflare-api/account_id":  "acct-1",
			"cloudflare-tunnel-system/cloudflare-api/tunnel_name": "local-dev",
		},
		configMaps: zoneCM(),
	}
	k := &fakeKube{hosts: []string{"a.dantofa.dev"}, deleted: 1, stopped: 2}
	cf := &fakeCF{}
	res, err := Run(context.Background(), r, k, func(string) (CloudflareAPI, error) { return cf, nil },
		50*time.Millisecond, time.Millisecond)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !k.stopSeen {
		t.Error("expected the tunnel controller to be stopped before deleting the tunnel")
	}
	if !cf.tunnelDeleted || cf.tunnelName != "local-dev" {
		t.Errorf("tunnel not deleted by name: %+v", cf)
	}
	if res.StoppedTunnelWorkloads != 2 || !res.TunnelDeleted {
		t.Errorf("result missing tunnel fields: %+v", res)
	}
}

func TestRunSkipsTunnelOnDOKS(t *testing.T) {
	// No tunnel secret keys -> DOKS-shaped cluster.
	r := &fakeReader{
		secrets:    map[string]string{"external-dns/cloudflare-api/api_token": "tok"},
		configMaps: zoneCM(),
	}
	k := &fakeKube{hosts: []string{"a.dantofa.dev"}, deleted: 1}
	cf := &fakeCF{}
	_, err := Run(context.Background(), r, k, func(string) (CloudflareAPI, error) { return cf, nil },
		50*time.Millisecond, time.Millisecond)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if k.stopSeen {
		t.Error("tunnel controller should not be stopped on a cluster without a tunnel")
	}
	if cf.tunnelDeleted {
		t.Error("no tunnel should be deleted on a cluster without a tunnel")
	}
}
