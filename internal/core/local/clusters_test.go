package local

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

type fakeLocalAPI struct {
	clusters   []string
	created    string
	deleted    string
	pushedURL  string
	reconciled string
	pushedPath string
	pushedFile string // content of flux/cluster/a.yaml at push time
	pushedSrc  string
	pushedRev  string
}

func (f *fakeLocalAPI) List(context.Context) ([]string, error) { return f.clusters, nil }
func (f *fakeLocalAPI) Create(_ context.Context, name, _ string, _, _, _ int) error {
	f.created = name
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

func (f *fakeLocalAPI) ReconcileSource(_ context.Context, name string) error {
	f.reconciled = name
	return nil
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
	_, err := CreateCluster(context.Background(), f, "local", "reg", 5001, 1, 3)
	var ex *LocalClusterExistsError
	if !errors.As(err, &ex) {
		t.Fatalf("expected LocalClusterExistsError, got %v", err)
	}
}

func TestCreateClusterEndpoints(t *testing.T) {
	f := &fakeLocalAPI{}
	res, err := CreateCluster(context.Background(), f, "local", "kind-registry", 5001, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	if res.Registry != "localhost:5001" || res.RegistryInCluster != "kind-registry:5000" {
		t.Fatalf("wrong endpoints: %+v", res)
	}
}

func TestDeleteClusterIdempotent(t *testing.T) {
	f := &fakeLocalAPI{}
	name, err := DeleteCluster(context.Background(), f, "ghost")
	if err != nil || name != "" {
		t.Fatalf("missing cluster should be a no-op, got %q err %v", name, err)
	}
	f.clusters = []string{"local"}
	name, err = DeleteCluster(context.Background(), f, "local")
	if err != nil || name != "local" || f.deleted != "local" {
		t.Fatalf("expected local deleted, got %q err %v", name, err)
	}
}

func TestPushArtifactAssemblesAndReconciles(t *testing.T) {
	f := &fakeLocalAPI{}
	res, err := PushArtifact(context.Background(), f, "local", "latest", "flux/", 5001)
	if err != nil {
		t.Fatal(err)
	}
	if f.pushedURL != "oci://localhost:5001/local:latest" {
		t.Errorf("wrong push url: %q", f.pushedURL)
	}
	if f.reconciled != "local" || res.Revision != "main@sha1:abc" {
		t.Errorf("wrong result: reconciled=%q res=%+v", f.reconciled, res)
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
	res, err := PushPlatformArtifact(context.Background(), f, tree, "2026.11.01+gabc", "local", "latest", 5001)
	if err != nil {
		t.Fatal(err)
	}
	if f.pushedURL != "oci://localhost:5001/local:latest" {
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
