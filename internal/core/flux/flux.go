// Package flux holds the framework-free application logic for composing Flux
// GitOps on a cluster: installing Flux and registering/removing GitRepository
// sources and Kustomizations. The flux CLI surface is reached through the
// Engine interface, satisfied by the clients adapter, so this package imports
// neither cobra nor a client SDK and is reused by the future operator.
package flux

import (
	"context"
	"time"
)

// Defaults for the platform GitOps source a cluster is bootstrapped against.
// The composable commands and `do cluster bootstrap` share these so the base
// source/kustomization stay consistent.
const (
	DefaultSourceName   = "platform"
	DefaultSourceURL    = "https://github.com/dantofa/platform"
	DefaultSourceBranch = "master"
	// DefaultSourcePath is the remote/DOKS reconcile root (Velero backup stack);
	// local (kind) clusters reconcile DefaultLocalSourcePath instead.
	DefaultSourcePath = "./flux/cluster"
	// DefaultLocalSourcePath is the local/kind reconcile root: the SeaweedFS
	// backend that stands in for a cloud bucket plus the shared Velero stack.
	DefaultLocalSourcePath = "./flux/local"
)

// Engine is the Flux surface this package depends on, satisfied by the clients
// adapter (a wrapper over the flux CLI). Create operations are create-or-update
// (idempotent); Install is idempotent too.
type Engine interface {
	Install(ctx context.Context, version string) error
	CreateGitSource(ctx context.Context, name, url, branch string) error
	DeleteGitSource(ctx context.Context, name string) error
	CreateKustomization(ctx context.Context, name, source, path string) error
	DeleteKustomization(ctx context.Context, name string) error
	CreateOCISource(ctx context.Context, name, url, tag string) error
	CreateOCIKustomization(ctx context.Context, name, source, path string) error
}

// SourceSpec describes a GitRepository source to register.
type SourceSpec struct {
	Name   string
	URL    string
	Branch string
}

// KustomizationSpec describes a Kustomization reconciling a path from a source.
type KustomizationSpec struct {
	Name   string
	Source string
	Path   string
}

// SourceResult reports a registered source.
type SourceResult struct {
	Source string `json:"source"`
	URL    string `json:"url"`
	Branch string `json:"branch"`
}

// KustomizationResult reports a registered kustomization.
type KustomizationResult struct {
	Kustomization string `json:"kustomization"`
	Source        string `json:"source"`
	Path          string `json:"path"`
}

// BootstrapResult reports the source + kustomization a bootstrap registered.
type BootstrapResult struct {
	Source        string `json:"source"`
	URL           string `json:"url"`
	Branch        string `json:"branch"`
	Kustomization string `json:"kustomization"`
	Path          string `json:"path"`
}

// AddSource registers (create-or-update) a GitRepository source.
func AddSource(ctx context.Context, e Engine, spec SourceSpec) (SourceResult, error) {
	if err := e.CreateGitSource(ctx, spec.Name, spec.URL, spec.Branch); err != nil {
		return SourceResult{}, err
	}
	return SourceResult{Source: spec.Name, URL: spec.URL, Branch: spec.Branch}, nil
}

// RemoveSource deletes a GitRepository source.
func RemoveSource(ctx context.Context, e Engine, name string) error {
	return e.DeleteGitSource(ctx, name)
}

// AddKustomization registers (create-or-update) a Kustomization.
func AddKustomization(ctx context.Context, e Engine, spec KustomizationSpec) (KustomizationResult, error) {
	if err := e.CreateKustomization(ctx, spec.Name, spec.Source, spec.Path); err != nil {
		return KustomizationResult{}, err
	}
	return KustomizationResult{Kustomization: spec.Name, Source: spec.Source, Path: spec.Path}, nil
}

// RemoveKustomization deletes a Kustomization.
func RemoveKustomization(ctx context.Context, e Engine, name string) error {
	return e.DeleteKustomization(ctx, name)
}

// KustomizationStatus is one Flux Kustomization's reconciliation state. Status is
// the kstatus verdict (Current/InProgress/Failed/...); Ready is the gate.
type KustomizationStatus struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	Ready     bool   `json:"ready"`
	Message   string `json:"message,omitempty"`
}

// KustomizationStatuser reads the reconciliation status of the Flux
// Kustomizations on a cluster (satisfied by the kube adapter, via kstatus).
type KustomizationStatuser interface {
	KustomizationStatuses(ctx context.Context, namespace string) ([]KustomizationStatus, error)
}

// ListKustomizations returns every Kustomization's status (never nil, so an empty
// cluster renders as a JSON `[]`).
func ListKustomizations(ctx context.Context, s KustomizationStatuser, namespace string) ([]KustomizationStatus, error) {
	statuses, err := s.KustomizationStatuses(ctx, namespace)
	if err != nil {
		return nil, err
	}
	if statuses == nil {
		statuses = []KustomizationStatus{}
	}
	return statuses, nil
}

// VerifyKustomizations returns every Kustomization's status plus whether all are
// ready — the gate: ok is false if any Kustomization is not reconciled.
func VerifyKustomizations(ctx context.Context, s KustomizationStatuser, namespace string) (statuses []KustomizationStatus, ok bool, err error) {
	statuses, err = ListKustomizations(ctx, s, namespace)
	if err != nil {
		return nil, false, err
	}
	ok = true
	for _, st := range statuses {
		if !st.Ready {
			ok = false
		}
	}
	return statuses, ok, nil
}

// VerifyKustomizationsWait polls VerifyKustomizations until every Kustomization
// is ready or the timeout elapses, returning the last statuses + ok either way
// (so a timed-out gate still reports what is not reconciled). It turns the
// snapshot gate into a convergence gate for CI after a bootstrap/apply.
func VerifyKustomizationsWait(ctx context.Context, s KustomizationStatuser, namespace string, timeout, interval time.Duration) (statuses []KustomizationStatus, ok bool, err error) {
	deadline := time.Now().Add(timeout)
	for {
		statuses, ok, err = VerifyKustomizations(ctx, s, namespace)
		if err != nil {
			return nil, false, err
		}
		if ok || !time.Now().Before(deadline) {
			return statuses, ok, nil
		}
		select {
		case <-ctx.Done():
			return statuses, ok, ctx.Err()
		case <-time.After(interval):
		}
	}
}

// LocalBootstrapResult reports the OCI source + kustomization a local bootstrap
// registered.
type LocalBootstrapResult struct {
	Source        string `json:"source"`
	URL           string `json:"url"`
	Tag           string `json:"tag"`
	Kustomization string `json:"kustomization"`
	Path          string `json:"path"`
}

// BootstrapLocal installs Flux and points it at an OCIRepository (the artifact
// `dctl local cluster push` publishes to the in-cluster kind registry),
// reconciling path from it. The kustomization is named after the source. This
// is the local (kind) analogue of Bootstrap; the GitOps content it reconciles
// stands up an in-cluster backup target instead of linking a cloud bucket.
func BootstrapLocal(ctx context.Context, e Engine, version, name, url, tag, path string) (LocalBootstrapResult, error) {
	if err := e.Install(ctx, version); err != nil {
		return LocalBootstrapResult{}, err
	}
	if err := e.CreateOCISource(ctx, name, url, tag); err != nil {
		return LocalBootstrapResult{}, err
	}
	if err := e.CreateOCIKustomization(ctx, name, name, path); err != nil {
		return LocalBootstrapResult{}, err
	}
	return LocalBootstrapResult{
		Source: name, URL: url, Tag: tag, Kustomization: name, Path: path,
	}, nil
}

// Bootstrap installs Flux, registers the given source, and registers a
// Kustomization (named after the source) reconciling path from it. This is the
// ordered base-GitOps sequence `do cluster bootstrap` performs after linking the
// backup bucket.
func Bootstrap(ctx context.Context, e Engine, version string, src SourceSpec, path string) (BootstrapResult, error) {
	if err := e.Install(ctx, version); err != nil {
		return BootstrapResult{}, err
	}
	if _, err := AddSource(ctx, e, src); err != nil {
		return BootstrapResult{}, err
	}
	ks := KustomizationSpec{Name: src.Name, Source: src.Name, Path: path}
	if _, err := AddKustomization(ctx, e, ks); err != nil {
		return BootstrapResult{}, err
	}
	return BootstrapResult{
		Source: src.Name, URL: src.URL, Branch: src.Branch,
		Kustomization: ks.Name, Path: path,
	}, nil
}
