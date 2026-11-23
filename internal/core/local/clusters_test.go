package local

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	fluxcore "github.com/dantofa/platform/internal/core/flux"
)

type fakeLocalAPI struct {
	clusters   []string
	created    string
	deleted    string
	pushedURL  string
	pushedPath string
	pushedFile string // content of flux/cluster/a.yaml at push time
	pushedSrc  string
	pushedRev  string
}

func (f *fakeLocalAPI) List(context.Context) ([]string, error) { return f.clusters, nil }
func (f *fakeLocalAPI) Create(_ context.Context, name, _ string, _, _, _ int) error {
	f.created = name
	f.clusters = append(f.clusters, name)
	return nil
}
func (f *fakeLocalAPI) Delete(_ context.Context, name string) error { f.deleted = name; return nil }
func (f *fakeLocalAPI) GetKubeconfig(context.Context, string) (string, error) {
	return "kubeconfig", nil
}

func (f *fakeLocalAPI) RegistryIP(context.Context, string) (string, error) {
	return "172.18.0.6", nil
}

func (f *fakeLocalAPI) GitProvenance(context.Context) (string, string, error) {
	return "file:///repo", "main@sha1:abc", nil
}

func (f *fakeLocalAPI) PushArtifact(_ context.Context, url, path, source, revision string) error {
	f.pushedPath, f.pushedSrc, f.pushedRev = path, source, revision
	if b, err := os.ReadFile(filepath.Join(path, "flux/cluster/a.yaml")); err == nil {
		f.pushedFile = string(b)
	}
	f.pushedURL = url
	return nil
}

type fakeReconciler struct {
	reconciled string
	err        error
}

func (r *fakeReconciler) ReconcileOCISource(_ context.Context, name string) error {
	r.reconciled = name
	return r.err
}

func TestListClustersEmptyIsNonNil(t *testing.T) {
	got, err := ListClusters(context.Background(), &fakeLocalAPI{})
	if err != nil {
		t.Fatal(err)
	}
	// A nil slice marshals to JSON `null`; a list command must render `[]`.
	if got == nil {
		t.Fatal("expected a non-nil empty slice, got nil")
	}
	if len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}

func TestCreateClusterExists(t *testing.T) {
	f := &fakeLocalAPI{clusters: []string{"local"}}
	_, err := CreateCluster(context.Background(), f, "local", "reg", 5001, 1, 3, filepath.Join(t.TempDir(), "kc"))
	var ex *LocalClusterExistsError
	if !errors.As(err, &ex) {
		t.Fatalf("expected LocalClusterExistsError, got %v", err)
	}
}

func TestCreateClusterEndpoints(t *testing.T) {
	f := &fakeLocalAPI{}
	path := filepath.Join(t.TempDir(), ".kubeconfig")
	res, err := CreateCluster(context.Background(), f, "local", "kind-registry", 5001, 1, 3, path)
	if err != nil {
		t.Fatal(err)
	}
	if res.Registry != "localhost:5001" || res.RegistryInCluster != "kind-registry:5000" {
		t.Fatalf("wrong endpoints: %+v", res)
	}
	if res.Kubeconfig != path {
		t.Errorf("result kubeconfig = %q, want %q", res.Kubeconfig, path)
	}
}

// Create writes the project kubeconfig itself (kind only ever gets a scratch
// file), owner-only since it carries cluster-admin credentials.
func TestCreateClusterWritesKubeconfigOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".kubeconfig")
	if _, err := CreateCluster(context.Background(), &fakeLocalAPI{}, "local", "reg", 5001, 1, 0, path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("kubeconfig mode = %o, want 600", perm)
	}
	if b, _ := os.ReadFile(path); string(b) != "kubeconfig" {
		t.Errorf("kubeconfig content = %q", b)
	}
}

func TestDeleteClusterIdempotent(t *testing.T) {
	f := &fakeLocalAPI{}
	path := filepath.Join(t.TempDir(), ".kubeconfig")
	res, err := DeleteCluster(context.Background(), f, "ghost", path)
	if err != nil || res.Name != "" || res.Kubeconfig.Skipped != SkipKubeconfigAbsent {
		t.Fatalf("missing cluster should be a no-op, got %+v err %v", res, err)
	}
	f.clusters = []string{"local"}
	res, err = DeleteCluster(context.Background(), f, "local", path)
	if err != nil || res.Name != "local" || f.deleted != "local" {
		t.Fatalf("expected local deleted, got %+v err %v", res, err)
	}
}

// A stale kubeconfig from an earlier run is cleaned up even when the cluster
// itself is already gone.
func TestDeleteClusterRemovesKubeconfig(t *testing.T) {
	path := writeFile(t, kindKubeconfig("local"))
	res, err := DeleteCluster(context.Background(), &fakeLocalAPI{}, "local", path)
	if err != nil || !res.Kubeconfig.Removed {
		t.Fatalf("want kubeconfig removed, got %+v err %v", res, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("kubeconfig still present: %v", err)
	}
}

func TestPushArtifactAssemblesAndReconciles(t *testing.T) {
	f, rec := &fakeLocalAPI{}, &fakeReconciler{}
	res, err := PushArtifact(context.Background(), f, rec, DefaultArtifactName, "latest", "flux/", 5001)
	if err != nil {
		t.Fatal(err)
	}
	if f.pushedURL != "oci://localhost:5001/platform:latest" {
		t.Errorf("wrong push url: %q", f.pushedURL)
	}
	if rec.reconciled != DefaultArtifactName || res.Reconciled != DefaultArtifactName || res.Revision != "main@sha1:abc" {
		t.Errorf("wrong result: reconciled=%q res=%+v", rec.reconciled, res)
	}
}

// No cluster means no reconciler: the push publishes and skips the reconcile,
// rather than running it against whatever the ambient kubeconfig selects.
func TestPushArtifactWithoutClusterSkipsReconcile(t *testing.T) {
	res, err := PushArtifact(context.Background(), &fakeLocalAPI{}, nil, "auth", "latest", "flux/", 5001)
	if err != nil || res.Reconciled != "" || res.Artifact != "localhost:5001/auth:latest" {
		t.Fatalf("got %+v err %v", res, err)
	}
}

// The reconcile is best-effort: its failure leaves Reconciled empty, not the push failed.
func TestPushArtifactReconcileFailureIsNotFatal(t *testing.T) {
	rec := &fakeReconciler{err: errors.New("no such OCIRepository")}
	res, err := PushArtifact(context.Background(), &fakeLocalAPI{}, rec, "auth", "latest", "flux/", 5001)
	if err != nil || res.Reconciled != "" {
		t.Fatalf("got %+v err %v", res, err)
	}
}

func TestGetKubeconfigNotFound(t *testing.T) {
	f := &fakeLocalAPI{}
	_, err := GetKubeconfig(context.Background(), f, "ghost")
	var nf *LocalClusterNotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("expected LocalClusterNotFoundError, got %v", err)
	}
}

func TestPushPlatformArtifactPublishesEmbeddedTree(t *testing.T) {
	f := &fakeLocalAPI{}
	tree := fstest.MapFS{"flux/cluster/a.yaml": {Data: []byte("kind: X\n")}}
	res, err := PushPlatformArtifact(context.Background(), f, nil, tree, "2026.11.01+gabc", DefaultArtifactName, "latest", 5001)
	if err != nil {
		t.Fatal(err)
	}
	if f.pushedURL != "oci://localhost:5001/platform:latest" {
		t.Errorf("wrong push url: %q", f.pushedURL)
	}
	if f.pushedFile != "kind: X\n" {
		t.Errorf("tree not materialized with flux/ prefix, got %q", f.pushedFile)
	}
	if f.pushedSrc != PlatformSource || f.pushedRev != "2026.11.01+gabc" || res.Revision != f.pushedRev {
		t.Errorf("wrong provenance: %q %q", f.pushedSrc, f.pushedRev)
	}
	if _, err := os.Stat(f.pushedPath); !os.IsNotExist(err) {
		t.Errorf("temp tree not cleaned up: %v", err)
	}
}

type fakeEngine struct{ created []fluxcore.SourceSpec }

func (e *fakeEngine) Install(context.Context, string) error { return nil }
func (e *fakeEngine) CreateGitSource(context.Context, fluxcore.SourceSpec) error {
	return nil
}

func (e *fakeEngine) DeleteGitSource(context.Context, string) error { return nil }
func (e *fakeEngine) CreateOCISource(_ context.Context, spec fluxcore.SourceSpec) error {
	e.created = append(e.created, spec)
	return nil
}
func (e *fakeEngine) DeleteOCISource(context.Context, string) error     { return nil }
func (e *fakeEngine) DeleteKustomization(context.Context, string) error { return nil }

func TestRegisterSourcePointsAtTheInClusterRegistry(t *testing.T) {
	f, e := &fakeLocalAPI{}, &fakeEngine{}
	got, skipped, err := RegisterSource(context.Background(), f, e, DefaultRegistryName, "auth", "latest")
	if err != nil || got != "auth" || skipped != "" {
		t.Fatalf("got %q skipped %q err %v", got, skipped, err)
	}
	if len(e.created) != 1 {
		t.Fatalf("want one source, got %d", len(e.created))
	}
	spec := e.created[0]
	if spec.Type != fluxcore.SourceOCI || spec.Name != "auth" || !spec.Insecure ||
		spec.Revision != "latest" || spec.URL != "oci://172.18.0.6:5000/auth" {
		t.Errorf("wrong source: %+v", spec)
	}
}

func TestRegisterSourceSkipsThePlatformArtifact(t *testing.T) {
	f, e := &fakeLocalAPI{}, &fakeEngine{}
	got, skipped, err := RegisterSource(context.Background(), f, e, DefaultRegistryName, DefaultArtifactName, "latest")
	if err != nil || got != "" || skipped != SkipPlatformArtifact || len(e.created) != 0 {
		t.Fatalf("platform artifact must not get a second source: %q %q %v %+v", got, skipped, err, e.created)
	}
}

func TestSkipNoClusterNamesTheCluster(t *testing.T) {
	if got := SkipNoCluster("typo"); got != `cluster "typo" not found` {
		t.Errorf("got %q", got)
	}
}
