package digitalocean

import (
	"context"
	"errors"
	"reflect"
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
	events    *[]string // optional ordering recorder shared with a fakeStore
}

func (f *fakeProvisioner) record(e string) {
	if f.events != nil {
		*f.events = append(*f.events, e)
	}
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
	f.record("mint")
	if f.credErr != nil {
		return Credential{}, f.credErr
	}
	return f.cred, nil
}

func (f *fakeProvisioner) RevokeCredential(_ context.Context, accessKey string) error {
	f.revoked = accessKey
	f.record("revoke:" + accessKey)
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

type fakeStore struct {
	prior    string
	priorErr error
	storeErr error
	stored   Credential
	events   *[]string
}

func (s *fakeStore) CurrentAccessKey(context.Context) (string, error) {
	if s.events != nil {
		*s.events = append(*s.events, "current")
	}
	return s.prior, s.priorErr
}

func (s *fakeStore) Store(_ context.Context, cred Credential, _ BucketCoordinates) error {
	if s.events != nil {
		*s.events = append(*s.events, "store")
	}
	s.stored = cred
	return s.storeErr
}

func TestLinkAndStoreRotatesInOrder(t *testing.T) {
	var events []string
	p := &fakeProvisioner{cred: Credential{AccessKey: "AK2", SecretKey: "SK2"}, events: &events}
	s := &fakeStore{prior: "AK1", events: &events}
	res, err := LinkAndStore(context.Background(), p, s, "backup")
	if err != nil {
		t.Fatal(err)
	}
	if s.stored != p.cred {
		t.Errorf("stored wrong credential: %+v", s.stored)
	}
	if res.Credential != p.cred {
		t.Errorf("wrong result credential: %+v", res.Credential)
	}
	// The new key must be persisted before the old one is revoked.
	want := []string{"current", "mint", "store", "revoke:AK1"}
	if !reflect.DeepEqual(events, want) {
		t.Errorf("wrong ordering:\n got %v\nwant %v", events, want)
	}
}

func TestLinkAndStoreFirstLinkDoesNotRevoke(t *testing.T) {
	p := &fakeProvisioner{cred: Credential{AccessKey: "AK1"}}
	s := &fakeStore{prior: ""}
	if _, err := LinkAndStore(context.Background(), p, s, "b"); err != nil {
		t.Fatal(err)
	}
	if p.revoked != "" {
		t.Errorf("first link must not revoke, got %q", p.revoked)
	}
}

func TestLinkAndStoreRevokesNewKeyOnStoreFailure(t *testing.T) {
	sentinel := errors.New("store boom")
	p := &fakeProvisioner{cred: Credential{AccessKey: "AK2"}}
	s := &fakeStore{prior: "AK1", storeErr: sentinel}
	if _, err := LinkAndStore(context.Background(), p, s, "b"); !errors.Is(err, sentinel) {
		t.Fatalf("expected store error, got %v", err)
	}
	// The un-persisted new key is cleaned up; the prior key is left intact.
	if p.revoked != "AK2" {
		t.Errorf("expected un-persisted key AK2 to be revoked, got %q", p.revoked)
	}
}
