package teardown

import (
	"context"
	"errors"
	"io"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/dantofa/platform/internal/core/edgecert"
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

// flakyReader fails the first failures calls of each read with err, then serves
// from the embedded fakeReader — a stand-in for a managed API server dropping a
// connection.
type flakyReader struct {
	*fakeReader
	err      error
	failures int
	calls    int
}

func (f *flakyReader) SecretValue(ctx context.Context, ns, name, key string) (string, error) {
	f.calls++
	if f.calls <= f.failures {
		return "", f.err
	}
	return f.fakeReader.SecretValue(ctx, ns, name, key)
}

func (f *flakyReader) ConfigMapValue(ctx context.Context, ns, name, key string) (string, error) {
	f.calls++
	if f.calls <= f.failures {
		return "", f.err
	}
	return f.fakeReader.ConfigMapValue(ctx, ns, name, key)
}

// A dropped connection on this read used to abort `cluster delete` outright,
// which does not fail safe: the cluster stays up and keeps billing. Ride it out.
func TestWithRetryRidesOutDroppedConnection(t *testing.T) {
	r := &flakyReader{
		fakeReader: &fakeReader{secrets: map[string]string{
			"external-dns/cloudflare-api/api_token": "tok",
		}},
		err:      &url.Error{Op: "Get", URL: "https://x/api/v1/secrets", Err: io.EOF},
		failures: 2,
	}
	got, err := ResolveCloudflareToken(context.Background(), WithRetry(r, 100*time.Millisecond, time.Millisecond))
	if err != nil {
		t.Fatalf("ResolveCloudflareToken: %v", err)
	}
	if got != "tok" {
		t.Errorf("token = %q, want %q", got, "tok")
	}
}

func TestWithRetryDoesNotRetryDefinitiveErrors(t *testing.T) {
	// A permission problem is an answer: surface it now rather than after the
	// read budget expires.
	sentinel := errors.New(`secrets "cloudflare-api" is forbidden`)
	r := &flakyReader{fakeReader: &fakeReader{}, err: sentinel, failures: 1}
	if _, err := ResolveCloudflareToken(context.Background(), WithRetry(r, 100*time.Millisecond, time.Millisecond)); !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want %v", err, sentinel)
	}
	if r.calls != 1 {
		t.Errorf("calls = %d, want 1 (no retry)", r.calls)
	}
}

// fakeCF satisfies CloudflareAPI: no records (drains immediately), records the
// tunnel delete call.
type fakeCF struct {
	tunnelName    string
	tunnelDeleted bool
}

// fakeCerts is the separate certificate-credential surface.
type fakeCerts struct {
	packs        []edgecert.Pack
	deletedPacks []string
	packErr      error
}

func (f *fakeCerts) ListCertificatePacks(context.Context, string) ([]edgecert.Pack, error) {
	return f.packs, f.packErr
}

func (f *fakeCerts) OrderCertificatePack(context.Context, string, []string) (edgecert.Pack, error) {
	return edgecert.Pack{}, nil
}

func (f *fakeCerts) GetCertificatePack(context.Context, string, string) (edgecert.Pack, error) {
	return edgecert.Pack{}, nil
}

func (f *fakeCerts) DeleteCertificatePack(_ context.Context, _, id string) error {
	f.deletedPacks = append(f.deletedPacks, id)
	return nil
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
	res, err := Run(context.Background(), r, k, func(string) (CloudflareAPI, error) { return cf, nil }, nil,
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
	res, err := Run(context.Background(), r, k, func(string) (CloudflareAPI, error) { return cf, nil }, nil,
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
	_, err := Run(context.Background(), r, k, func(string) (CloudflareAPI, error) { return cf, nil }, nil,
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

func edgeCertReader() *fakeReader {
	return &fakeReader{
		secrets: map[string]string{"external-dns/cloudflare-api/api_token": "tok"},
		configMaps: map[string]string{
			"flux-system/" + fluxcore.ClusterVarsName + "/" + fluxcore.VarDNSZone:    "dantofa.dev",
			"flux-system/" + fluxcore.ClusterVarsName + "/" + fluxcore.VarBaseDomain: "auth-pr42.preview.dantofa.dev",
		},
	}
}

func TestRunDeletesOnlyTheClustersEdgeCert(t *testing.T) {
	certs := &fakeCerts{packs: []edgecert.Pack{
		{ID: "shared", Status: "active", Hosts: edgecert.Hosts("preview.dantofa.dev")},
		{ID: "other-pr", Status: "active", Hosts: edgecert.Hosts("auth-pr43.preview.dantofa.dev")},
		{ID: "mine", Status: "active", Hosts: edgecert.Hosts("auth-pr42.preview.dantofa.dev")},
	}}
	res, err := Run(context.Background(), edgeCertReader(), &fakeKube{}, func(string) (CloudflareAPI, error) { return &fakeCF{}, nil }, certs,
		50*time.Millisecond, time.Millisecond)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(certs.deletedPacks) != 1 || certs.deletedPacks[0] != "mine" || res.EdgeCertsDeleted != 1 {
		t.Errorf("deleted %v (result %+v), want only [mine]", certs.deletedPacks, res)
	}
}

func TestRunEdgeCertFailureDoesNotBlockTheDelete(t *testing.T) {
	certs := &fakeCerts{packErr: errors.New("403 forbidden")}
	res, err := Run(context.Background(), edgeCertReader(), &fakeKube{}, func(string) (CloudflareAPI, error) { return &fakeCF{}, nil }, certs,
		50*time.Millisecond, time.Millisecond)
	if err != nil {
		t.Fatalf("an edge-cert failure must not fail teardown (it would leave the cluster running): %v", err)
	}
	if !strings.Contains(res.EdgeCertError, "403") {
		t.Errorf("the failure must still be reported, got %+v", res)
	}
}

func TestRunSkipsEdgeCertAtTheApex(t *testing.T) {
	r := edgeCertReader()
	r.configMaps["flux-system/"+fluxcore.ClusterVarsName+"/"+fluxcore.VarBaseDomain] = "dantofa.dev"
	certs := &fakeCerts{packErr: errors.New("must not be listed")}
	res, err := Run(context.Background(), r, &fakeKube{}, func(string) (CloudflareAPI, error) { return &fakeCF{}, nil }, certs,
		50*time.Millisecond, time.Millisecond)
	if err != nil || res.EdgeCertError != "" {
		t.Errorf("a covered base domain has no certificate to reap: %+v, %v", res, err)
	}
}

func TestRunReapsEdgeCertEvenWhenTheDrainFails(t *testing.T) {
	certs := &fakeCerts{packs: []edgecert.Pack{{ID: "mine", Status: "active", Hosts: edgecert.Hosts("auth-pr42.preview.dantofa.dev")}}}
	k := &fakeKube{suspendErr: errors.New("api down"), hosts: []string{"a.dantofa.dev"}}
	_, err := Run(context.Background(), edgeCertReader(), k, func(string) (CloudflareAPI, error) { return &fakeCF{}, nil }, certs,
		50*time.Millisecond, time.Millisecond)
	if err == nil {
		t.Fatal("the drain failure must still surface")
	}
	if len(certs.deletedPacks) != 1 {
		t.Error("the certificate must be reaped even when the drain fails, or --force leaks it")
	}
}

func TestRunWithoutCertCredentialLeavesCertsAlone(t *testing.T) {
	res, err := Run(context.Background(), edgeCertReader(), &fakeKube{}, func(string) (CloudflareAPI, error) { return &fakeCF{}, nil }, nil,
		50*time.Millisecond, time.Millisecond)
	if err != nil || res.EdgeCertsDeleted != 0 || res.EdgeCertError != "" {
		t.Errorf("no certificate credential means no cleanup and no noise: %+v, %v", res, err)
	}
}
