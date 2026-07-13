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
	// DefaultOCISourceURL is the published OCI artifact a remote cluster pulls
	// when bootstrapped with --source-type oci (the default). Local clusters
	// override this with the in-cluster kind registry URL.
	DefaultOCISourceURL = "oci://ghcr.io/dantofa/platform"
	// DefaultOCIRevision is the OCI tag a source tracks by default (mutable:
	// re-pulled every reconcile interval). DefaultSourceBranch is the git
	// equivalent.
	DefaultOCIRevision = "latest"
	// DefaultSourcePath is the shared, source-agnostic reconcile root every
	// cluster loads (Velero + Kyverno). Its nested Kustomizations reference the
	// source via ${source_kind}/${source_name}, filled in by the reconcile
	// root's postBuild.substitute.
	DefaultSourcePath = "./flux/cluster"
	// DefaultLocalSourcePath is the local/kind-only requirements root: the
	// SeaweedFS backend that stands in for a cloud bucket plus the backup
	// contract. It is only ever reconciled from OCI, so it hardcodes its source.
	DefaultLocalSourcePath = "./flux/local"

	// ClusterRootName is the reconcile root that loads the shared ./flux/cluster
	// stacks on every cluster type. LocalRequirementsRootName loads the
	// local-only ./flux/local requirements ahead of it on kind clusters.
	ClusterRootName           = "cluster"
	LocalRequirementsRootName = "local-requirements"
)

// SourceType selects which Flux source kind a cluster is bootstrapped against.
// oci is the default; git stays a first-class option for downstream projects
// that would rather track a branch than publish OCI artifacts.
type SourceType string

const (
	SourceOCI SourceType = "oci"
	SourceGit SourceType = "git"

	// DefaultSourceType is what a bootstrap registers unless --source-type says
	// otherwise.
	DefaultSourceType = SourceOCI
)

// FluxKind maps the CLI-facing source type to the Flux source CRD kind used in a
// Kustomization's sourceRef.
func (t SourceType) FluxKind() string {
	if t == SourceGit {
		return "GitRepository"
	}
	return "OCIRepository"
}

// DefaultRevision is the source revision tracked when none is given: the latest
// OCI tag, or the default git branch.
func (t SourceType) DefaultRevision() string {
	if t == SourceGit {
		return DefaultSourceBranch
	}
	return DefaultOCIRevision
}

// Engine is the flux-CLI surface this package depends on, satisfied by the
// clients adapter. It installs Flux and registers sources; the reconcile roots
// go through ReconcileRootApplier instead (the flux CLI can't set
// postBuild.substitute). Create operations are create-or-update (idempotent).
type Engine interface {
	Install(ctx context.Context, version string) error
	CreateGitSource(ctx context.Context, name, url, branch string) error
	DeleteGitSource(ctx context.Context, name string) error
	CreateOCISource(ctx context.Context, name, url, tag string, insecure bool) error
	DeleteOCISource(ctx context.Context, name string) error
	CreateKustomization(ctx context.Context, name, sourceKind, source, path string) error
	DeleteKustomization(ctx context.Context, name string) error
}

// ReconcileRoot is a top-level Flux Kustomization dctl applies as a CR during
// bootstrap. When PropagateSource is set it carries a postBuild.substitute
// providing source_kind/source_name, so the source-agnostic stacks it
// reconciles resolve their sourceRef to this cluster's source. DependsOn orders
// it after other roots. Both are things `flux create kustomization` can't
// express, so bootstrap goes through the kube adapter.
type ReconcileRoot struct {
	Name       string
	Path       string
	SourceKind string // OCIRepository | GitRepository (this cluster's source)
	SourceName string
	DependsOn  []string // reconcile-root names in flux-system to wait for
	// PropagateSource emits postBuild.substitute source_kind/source_name so a
	// portable (source-agnostic) tree binds to SourceKind/SourceName. Leave it
	// off for source-pinned trees to avoid running substitution over them.
	PropagateSource bool
}

// ReconcileRootApplier applies a ReconcileRoot as a Flux Kustomization CR
// (create-or-update), satisfied by the kube adapter.
type ReconcileRootApplier interface {
	ApplyReconcileRoot(ctx context.Context, root ReconcileRoot) error
}

// SourceSpec describes a Flux source to register: its Type (oci/git) selects the
// source CRD kind and how Revision is read (an OCI tag or a git branch).
// Insecure allows plain-HTTP OCI, for the in-cluster kind registry only.
type SourceSpec struct {
	Type     SourceType
	Name     string
	URL      string
	Revision string
	Insecure bool
}

// KustomizationSpec describes a Kustomization reconciling a path from a source.
// Type selects the source CRD kind (oci/git) the sourceRef points at.
type KustomizationSpec struct {
	Type   SourceType
	Name   string
	Source string
	Path   string
}

// SourceResult reports a registered source.
type SourceResult struct {
	Source   string `json:"source"`
	Kind     string `json:"kind"`
	URL      string `json:"url"`
	Revision string `json:"revision"`
}

// KustomizationResult reports a registered kustomization.
type KustomizationResult struct {
	Kustomization string `json:"kustomization"`
	SourceKind    string `json:"source_kind"`
	Source        string `json:"source"`
	Path          string `json:"path"`
}

// BootstrapResult reports the source and reconcile roots a bootstrap registered.
type BootstrapResult struct {
	Source         string   `json:"source"`
	SourceKind     string   `json:"source_kind"`
	URL            string   `json:"url"`
	Revision       string   `json:"revision"`
	Kustomizations []string `json:"kustomizations"`
}

// AddSource registers (create-or-update) an OCIRepository or GitRepository
// source per spec.Type.
func AddSource(ctx context.Context, e Engine, spec SourceSpec) (SourceResult, error) {
	switch spec.Type {
	case SourceGit:
		if err := e.CreateGitSource(ctx, spec.Name, spec.URL, spec.Revision); err != nil {
			return SourceResult{}, err
		}
	default:
		if err := e.CreateOCISource(ctx, spec.Name, spec.URL, spec.Revision, spec.Insecure); err != nil {
			return SourceResult{}, err
		}
	}
	return SourceResult{
		Source: spec.Name, Kind: spec.Type.FluxKind(), URL: spec.URL, Revision: spec.Revision,
	}, nil
}

// RemoveSource deletes a source of the given type.
func RemoveSource(ctx context.Context, e Engine, typ SourceType, name string) error {
	if typ == SourceGit {
		return e.DeleteGitSource(ctx, name)
	}
	return e.DeleteOCISource(ctx, name)
}

// AddKustomization registers (create-or-update) a Kustomization referencing a
// source of spec.Type.
func AddKustomization(ctx context.Context, e Engine, spec KustomizationSpec) (KustomizationResult, error) {
	kind := spec.Type.FluxKind()
	if err := e.CreateKustomization(ctx, spec.Name, kind, spec.Source, spec.Path); err != nil {
		return KustomizationResult{}, err
	}
	return KustomizationResult{
		Kustomization: spec.Name, SourceKind: kind, Source: spec.Source, Path: spec.Path,
	}, nil
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

// Bootstrap installs Flux, registers the source (oci or git per src.Type), and
// applies the given reconcile roots as Kustomization CRs in order. Each root's
// SourceKind/SourceName are filled from the registered source, so callers pass
// roots describing only the paths and ordering. This one sequence serves every
// cluster: DOKS passes a single `cluster` root, kind passes `local-requirements`
// then `cluster`.
func Bootstrap(ctx context.Context, e Engine, a ReconcileRootApplier, version string, src SourceSpec, roots []ReconcileRoot) (BootstrapResult, error) {
	if err := e.Install(ctx, version); err != nil {
		return BootstrapResult{}, err
	}
	if _, err := AddSource(ctx, e, src); err != nil {
		return BootstrapResult{}, err
	}
	kind := src.Type.FluxKind()
	names := make([]string, 0, len(roots))
	for _, r := range roots {
		r.SourceKind, r.SourceName = kind, src.Name
		if err := a.ApplyReconcileRoot(ctx, r); err != nil {
			return BootstrapResult{}, err
		}
		names = append(names, r.Name)
	}
	return BootstrapResult{
		Source: src.Name, SourceKind: kind, URL: src.URL,
		Revision: src.Revision, Kustomizations: names,
	}, nil
}
