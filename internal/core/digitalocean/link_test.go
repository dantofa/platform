package digitalocean

import (
	"context"
	"errors"
	"testing"
)

type fakeProvisioner struct {
	coords    BucketCoordinates
	cred      Credential
	ensured   string
	scopedFor string
	revoked   string
	ensureErr error
	credErr   error
	revokeErr error
}

func (f *fakeProvisioner) EnsureBucket(_ context.Context, name string) (BucketCoordinates, error) {
	f.ensured = name
	if f.ensureErr != nil {
		return BucketCoordinates{}, f.ensureErr
	}
	return f.coords, nil
}

func (f *fakeProvisioner) CreateScopedCredential(_ context.Context, bucket string) (Credential, error) {
	f.scopedFor = bucket
	if f.credErr != nil {
		return Credential{}, f.credErr
	}
	return f.cred, nil
}

func (f *fakeProvisioner) RevokeCredential(_ context.Context, accessKey string) error {
	f.revoked = accessKey
	return f.revokeErr
}

func TestLinkEnsuresAndMints(t *testing.T) {
	f := &fakeProvisioner{
		coords: BucketCoordinates{Bucket: "backup", Region: "nyc3", Endpoint: "https://nyc3.digitaloceanspaces.com"},
		cred:   Credential{AccessKey: "AK", SecretKey: "SK"},
	}
	res, err := Link(context.Background(), f, "backup")
	if err != nil {
		t.Fatal(err)
	}
	if f.ensured != "backup" || f.scopedFor != "backup" {
		t.Errorf("expected ensure+scope for 'backup', got ensured=%q scoped=%q", f.ensured, f.scopedFor)
	}
	if res.Coordinates != f.coords || res.Credential != f.cred {
		t.Errorf("wrong result: %+v", res)
	}
}

func TestLinkPropagatesErrors(t *testing.T) {
	sentinel := errors.New("boom")
	if _, err := Link(context.Background(), &fakeProvisioner{ensureErr: sentinel}, "b"); !errors.Is(err, sentinel) {
		t.Errorf("ensure error not propagated: %v", err)
	}
	f := &fakeProvisioner{credErr: sentinel}
	if _, err := Link(context.Background(), f, "b"); !errors.Is(err, sentinel) {
		t.Errorf("credential error not propagated: %v", err)
	}
	// A failed mint must not revoke anything.
	if f.revoked != "" {
		t.Errorf("mint failure should not revoke, got %q", f.revoked)
	}
}

func TestRevokePrior(t *testing.T) {
	f := &fakeProvisioner{}
	if err := RevokePrior(context.Background(), f, ""); err != nil || f.revoked != "" {
		t.Errorf("empty prior must be a no-op, got revoked=%q err=%v", f.revoked, err)
	}
	if err := RevokePrior(context.Background(), f, "old-key"); err != nil || f.revoked != "old-key" {
		t.Errorf("expected revoke of old-key, got revoked=%q err=%v", f.revoked, err)
	}
}

func TestVeleroCredentialsFile(t *testing.T) {
	got := VeleroCredentialsFile(Credential{AccessKey: "AK", SecretKey: "SK"})
	want := "[default]\naws_access_key_id=AK\naws_secret_access_key=SK\n"
	if got != want {
		t.Errorf("rendered credentials mismatch:\ngot  %q\nwant %q", got, want)
	}
}

func TestConfigMapData(t *testing.T) {
	c := BucketCoordinates{Bucket: "backup", Region: "nyc3", Endpoint: "https://nyc3.digitaloceanspaces.com"}
	m := c.ConfigMapData()
	if m["bucket"] != "backup" || m["region"] != "nyc3" || m["endpoint"] != c.Endpoint {
		t.Errorf("wrong config map data: %v", m)
	}
}
