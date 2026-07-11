package flux

import (
	"context"
	"errors"
	"testing"
)

// fakeEngine records the flux operations invoked against it, in order.
type fakeEngine struct {
	events  []string
	failOn  string // an event prefix that should return an error
	failErr error
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

func (f *fakeEngine) CreateKustomization(_ context.Context, name, source, path string) error {
	return f.record("create-ks:" + name + ":" + source + ":" + path)
}

func (f *fakeEngine) DeleteKustomization(_ context.Context, name string) error {
	return f.record("delete-ks:" + name)
}

func eq(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestAddSource(t *testing.T) {
	e := &fakeEngine{}
	res, err := AddSource(context.Background(), e, SourceSpec{Name: "app", URL: "https://git/app", Branch: "main"})
	if err != nil {
		t.Fatalf("AddSource: %v", err)
	}
	eq(t, res.Source, "app")
	eq(t, res.URL, "https://git/app")
	eq(t, res.Branch, "main")
	eq(t, e.events[0], "create-source:app:https://git/app:main")
}

func TestAddKustomization(t *testing.T) {
	e := &fakeEngine{}
	res, err := AddKustomization(context.Background(), e, KustomizationSpec{Name: "app", Source: "platform", Path: "./flux"})
	if err != nil {
		t.Fatalf("AddKustomization: %v", err)
	}
	eq(t, res.Kustomization, "app")
	eq(t, res.Source, "platform")
	eq(t, res.Path, "./flux")
	eq(t, e.events[0], "create-ks:app:platform:./flux")
}

func TestRemoveSourceAndKustomization(t *testing.T) {
	e := &fakeEngine{}
	if err := RemoveSource(context.Background(), e, "app"); err != nil {
		t.Fatalf("RemoveSource: %v", err)
	}
	if err := RemoveKustomization(context.Background(), e, "app"); err != nil {
		t.Fatalf("RemoveKustomization: %v", err)
	}
	eq(t, e.events[0], "delete-source:app")
	eq(t, e.events[1], "delete-ks:app")
}

func TestBootstrapOrdersInstallSourceThenKustomization(t *testing.T) {
	e := &fakeEngine{}
	res, err := Bootstrap(context.Background(), e, "v2.3.0",
		SourceSpec{Name: DefaultSourceName, URL: DefaultSourceURL, Branch: DefaultSourceBranch},
		DefaultSourcePath)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	want := []string{
		"install:v2.3.0",
		"create-source:platform:" + DefaultSourceURL + ":master",
		"create-ks:platform:platform:./flux",
	}
	if len(e.events) != len(want) {
		t.Fatalf("events = %v, want %v", e.events, want)
	}
	for i := range want {
		eq(t, e.events[i], want[i])
	}
	// The kustomization is named after and reconciles from the source.
	eq(t, res.Source, "platform")
	eq(t, res.Kustomization, "platform")
	eq(t, res.Path, "./flux")
}

func TestBootstrapStopsOnInstallFailure(t *testing.T) {
	sentinel := errors.New("install boom")
	e := &fakeEngine{failOn: "install:", failErr: sentinel}
	if _, err := Bootstrap(context.Background(), e, "", SourceSpec{Name: "x"}, "./flux"); !errors.Is(err, sentinel) {
		t.Fatalf("expected install error, got %v", err)
	}
	// No source/kustomization attempted after install failed.
	if len(e.events) != 1 {
		t.Fatalf("expected only the install attempt, got %v", e.events)
	}
}
