package local

import (
	"context"
	"errors"
	"testing"
)

type fakeLocalAPI struct {
	clusters   []string
	created    string
	deleted    string
	pushedURL  string
	reconciled string
}

func (f *fakeLocalAPI) List(context.Context) ([]string, error) { return f.clusters, nil }
func (f *fakeLocalAPI) Create(_ context.Context, name, _ string, _, _ int) error {
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

func (f *fakeLocalAPI) PushArtifact(_ context.Context, url, _, _, _ string) error {
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
	_, err := CreateCluster(context.Background(), f, "local", "reg", 5001, 3)
	var ex *LocalClusterExistsError
	if !errors.As(err, &ex) {
		t.Fatalf("expected LocalClusterExistsError, got %v", err)
	}
}

func TestCreateClusterEndpoints(t *testing.T) {
	f := &fakeLocalAPI{}
	res, err := CreateCluster(context.Background(), f, "local", "kind-registry", 5001, 3)
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
