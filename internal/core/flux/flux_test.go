package flux

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeEngine records the flux operations invoked against it, in order.
type fakeEngine struct {
	events        []string
	failOn        string // an event prefix that should return an error
	failErr       error
	lastConfigMap map[string]string // data of the last ApplyConfigMap call
}

func (f *fakeEngine) record(event string) error {
	f.events = append(f.events, event)
	if f.failOn != "" && event == f.failOn {
		return f.failErr
	}
	return nil
}

func (f *fakeEngine) Install(_ context.Context, version string) error {
	return f.record("install:" + version)
}

func (f *fakeEngine) CreateGitSource(_ context.Context, name, url, branch string) error {
	return f.record("create-source:" + name + ":" + url + ":" + branch)
}

func (f *fakeEngine) DeleteGitSource(_ context.Context, name string) error {
	return f.record("delete-source:" + name)
}

func (f *fakeEngine) CreateKustomization(_ context.Context, name, sourceKind, source, path string) error {
	return f.record("create-ks:" + name + ":" + sourceKind + "/" + source + ":" + path)
}

func (f *fakeEngine) DeleteKustomization(_ context.Context, name string) error {
	return f.record("delete-ks:" + name)
}

func (f *fakeEngine) CreateOCISource(_ context.Context, name, url, tag string, _ bool) error {
	return f.record("create-oci-source:" + name + ":" + url + ":" + tag)
}

func (f *fakeEngine) DeleteOCISource(_ context.Context, name string) error {
	return f.record("delete-oci-source:" + name)
}

func (f *fakeEngine) ApplyReconcileRoot(_ context.Context, root ReconcileRoot) error {
	return f.record("apply-root:" + root.Name + ":" + root.SourceKind + "/" + root.SourceName + ":" + root.Path)
}

func (f *fakeEngine) ApplyConfigMap(_ context.Context, namespace, name string, data map[string]string) error {
	f.lastConfigMap = data
	return f.record("apply-cfgmap:" + namespace + "/" + name)
}

func eq(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestAddSource(t *testing.T) {
	e := &fakeEngine{}
	res, err := AddSource(context.Background(), e, SourceSpec{Type: SourceGit, Name: "app", URL: "https://git/app", Revision: "main"})
	if err != nil {
		t.Fatalf("AddSource: %v", err)
	}
	eq(t, res.Source, "app")
	eq(t, res.Kind, "GitRepository")
	eq(t, res.URL, "https://git/app")
	eq(t, res.Revision, "main")
	eq(t, e.events[0], "create-source:app:https://git/app:main")
}

func TestAddSourceOCI(t *testing.T) {
	e := &fakeEngine{}
	res, err := AddSource(context.Background(), e, SourceSpec{Type: SourceOCI, Name: "app", URL: "oci://reg/app", Revision: "latest"})
	if err != nil {
		t.Fatalf("AddSource: %v", err)
	}
	eq(t, res.Kind, "OCIRepository")
	eq(t, e.events[0], "create-oci-source:app:oci://reg/app:latest")
}

func TestAddKustomization(t *testing.T) {
	e := &fakeEngine{}
	res, err := AddKustomization(context.Background(), e, KustomizationSpec{Type: SourceOCI, Name: "app", Source: "platform", Path: "./flux"})
	if err != nil {
		t.Fatalf("AddKustomization: %v", err)
	}
	eq(t, res.Kustomization, "app")
	eq(t, res.SourceKind, "OCIRepository")
	eq(t, res.Source, "platform")
	eq(t, res.Path, "./flux")
	eq(t, e.events[0], "create-ks:app:OCIRepository/platform:./flux")
}

func TestRemoveSourceAndKustomization(t *testing.T) {
	e := &fakeEngine{}
	if err := RemoveSource(context.Background(), e, SourceGit, "app"); err != nil {
		t.Fatalf("RemoveSource: %v", err)
	}
	if err := RemoveKustomization(context.Background(), e, "app"); err != nil {
		t.Fatalf("RemoveKustomization: %v", err)
	}
	eq(t, e.events[0], "delete-source:app")
	eq(t, e.events[1], "delete-ks:app")
}

func TestBootstrapOCIOrdersInstallSourceVarsThenRoots(t *testing.T) {
	e := &fakeEngine{}
	// The local shape: an OCI source and two roots, cluster after requirements.
	roots := []ReconcileRoot{
		{Name: LocalRequirementsRootName, Path: DefaultLocalSourcePath},
		{Name: ClusterRootName, Path: DefaultSourcePath, DependsOn: []string{LocalRequirementsRootName}, Substitute: true},
	}
	vars := map[string]string{VarBaseDomain: "127.0.0.1.nip.io", VarClusterName: "local"}
	res, err := Bootstrap(context.Background(), e, e, "",
		SourceSpec{Type: SourceOCI, Name: DefaultSourceName, URL: "oci://kind-registry:5000/local", Revision: "latest"},
		vars, roots)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	// cluster-vars is written after the source, before the roots that read it.
	want := []string{
		"install:",
		"create-oci-source:platform:oci://kind-registry:5000/local:latest",
		"apply-cfgmap:flux-system/cluster-vars",
		"apply-root:local-requirements:OCIRepository/platform:" + DefaultLocalSourcePath,
		"apply-root:cluster:OCIRepository/platform:" + DefaultSourcePath,
	}
	if len(e.events) != len(want) {
		t.Fatalf("events = %v, want %v", e.events, want)
	}
	for i := range want {
		eq(t, e.events[i], want[i])
	}
	// cluster-vars merges the source coordinates with the caller's vars.
	eq(t, e.lastConfigMap[VarSourceKind], "OCIRepository")
	eq(t, e.lastConfigMap[VarSourceName], "platform")
	eq(t, e.lastConfigMap[VarBaseDomain], "127.0.0.1.nip.io")
	eq(t, e.lastConfigMap[VarClusterName], "local")
	eq(t, res.Source, "platform")
	eq(t, res.SourceKind, "OCIRepository")
	if len(res.Kustomizations) != 2 || res.Kustomizations[0] != LocalRequirementsRootName || res.Kustomizations[1] != ClusterRootName {
		t.Fatalf("kustomizations = %v", res.Kustomizations)
	}
}

func TestBootstrapGitRegistersGitSource(t *testing.T) {
	e := &fakeEngine{}
	// The DOKS/downstream shape: a git source and a single cluster root.
	res, err := Bootstrap(context.Background(), e, e, "v2.3.0",
		SourceSpec{Type: SourceGit, Name: DefaultSourceName, URL: DefaultSourceURL, Revision: DefaultSourceBranch},
		map[string]string{VarBaseDomain: "dev.example.com"},
		[]ReconcileRoot{{Name: ClusterRootName, Path: DefaultSourcePath, Substitute: true}})
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	want := []string{
		"install:v2.3.0",
		"create-source:platform:" + DefaultSourceURL + ":master",
		"apply-cfgmap:flux-system/cluster-vars",
		"apply-root:cluster:GitRepository/platform:" + DefaultSourcePath,
	}
	if len(e.events) != len(want) {
		t.Fatalf("events = %v, want %v", e.events, want)
	}
	for i := range want {
		eq(t, e.events[i], want[i])
	}
	eq(t, e.lastConfigMap[VarSourceKind], "GitRepository")
	eq(t, e.lastConfigMap[VarBaseDomain], "dev.example.com")
	eq(t, res.SourceKind, "GitRepository")
	eq(t, res.Revision, "master")
}

type fakeKustomizationStatuser struct {
	statuses   []KustomizationStatus
	err        error
	readyAfter int // become all-ready on this call number (0 = never flip)
	calls      int
}

func (f *fakeKustomizationStatuser) KustomizationStatuses(context.Context, string) ([]KustomizationStatus, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	if f.readyAfter > 0 && f.calls >= f.readyAfter {
		out := make([]KustomizationStatus, len(f.statuses))
		for i, s := range f.statuses {
			s.Status, s.Ready = "Current", true
			out[i] = s
		}
		return out, nil
	}
	return f.statuses, nil
}

func TestListKustomizationsEmptyIsNonNil(t *testing.T) {
	got, err := ListKustomizations(context.Background(), &fakeKustomizationStatuser{}, "")
	if err != nil {
		t.Fatal(err)
	}
	// A nil slice marshals to JSON `null`; a list command must render `[]`.
	if got == nil {
		t.Fatal("expected a non-nil empty slice, got nil")
	}
}

func TestVerifyKustomizationsOKWhenAllReady(t *testing.T) {
	f := &fakeKustomizationStatuser{statuses: []KustomizationStatus{
		{Name: "platform", Status: "Current", Ready: true},
		{Name: "velero", Status: "Current", Ready: true},
	}}
	statuses, ok, err := VerifyKustomizations(context.Background(), f, "")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("expected ok")
	}
	if len(statuses) != 2 {
		t.Errorf("expected 2 statuses, got %d", len(statuses))
	}
}

func TestVerifyKustomizationsFailsWhenAnyNotReady(t *testing.T) {
	f := &fakeKustomizationStatuser{statuses: []KustomizationStatus{
		{Name: "platform", Status: "Current", Ready: true},
		{Name: "velero", Status: "Failed", Ready: false},
	}}
	statuses, ok, err := VerifyKustomizations(context.Background(), f, "")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("expected not ok")
	}
	// The full list is still returned so the caller can show every status.
	if len(statuses) != 2 {
		t.Errorf("expected 2 statuses, got %d", len(statuses))
	}
}

func TestVerifyKustomizationsWaitConverges(t *testing.T) {
	f := &fakeKustomizationStatuser{
		statuses:   []KustomizationStatus{{Name: "platform", Status: "InProgress", Ready: false}},
		readyAfter: 2, // not ready on the first poll, ready on the second
	}
	_, ok, err := VerifyKustomizationsWait(context.Background(), f, "", time.Second, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("expected ok after convergence")
	}
	if f.calls < 2 {
		t.Errorf("expected at least 2 polls, got %d", f.calls)
	}
}

func TestVerifyKustomizationsWaitTimesOut(t *testing.T) {
	f := &fakeKustomizationStatuser{
		statuses: []KustomizationStatus{{Name: "velero", Status: "Failed", Ready: false}},
	}
	statuses, ok, err := VerifyKustomizationsWait(context.Background(), f, "", 20*time.Millisecond, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if ok || len(statuses) != 1 {
		t.Errorf("expected a timed-out report with the problem, got ok=%v statuses=%v", ok, statuses)
	}
}

func TestBootstrapStopsOnInstallFailure(t *testing.T) {
	sentinel := errors.New("install boom")
	e := &fakeEngine{failOn: "install:", failErr: sentinel}
	if _, err := Bootstrap(context.Background(), e, e, "",
		SourceSpec{Type: SourceOCI, Name: "x"}, nil,
		[]ReconcileRoot{{Name: ClusterRootName, Path: "./flux"}}); !errors.Is(err, sentinel) {
		t.Fatalf("expected install error, got %v", err)
	}
	// No source/root attempted after install failed.
	if len(e.events) != 1 {
		t.Fatalf("expected only the install attempt, got %v", e.events)
	}
}
