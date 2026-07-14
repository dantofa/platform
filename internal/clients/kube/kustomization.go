package kube

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	fluxcore "github.com/dantofa/platform/internal/core/flux"
)

var _ fluxcore.Applier = (*Client)(nil)

// rootNamespace is where the reconcile roots, Flux sources, and the cluster-vars
// ConfigMap live.
const rootNamespace = "flux-system"

// ApplyReconcileRoot creates or updates a top-level Flux Kustomization CR for a
// reconcile root. It applies the CR directly (rather than via `flux create
// kustomization`) because the roots carry a postBuild.substituteFrom and
// dependsOn the flux CLI can't express. wait is always set so a root is Ready
// only once the objects it applies are — which is what a downstream root's
// dependsOn and the verify gate rely on. Implements fluxcore.Applier.
func (c *Client) ApplyReconcileRoot(ctx context.Context, root fluxcore.ReconcileRoot) error {
	spec := map[string]any{
		"interval": "10m",
		"path":     root.Path,
		"prune":    true,
		"wait":     true,
		"sourceRef": map[string]any{
			"kind":      root.SourceKind,
			"name":      root.SourceName,
			"namespace": rootNamespace,
		},
	}
	if root.Substitute {
		// Bind the portable stacks this root reconciles to this cluster's values
		// (source coordinates, base_domain, ...) from the cluster-vars ConfigMap
		// bootstrap writes. Only the ${...} tokens present in those stacks are
		// substituted; other keys in the ConfigMap are ignored.
		spec["postBuild"] = map[string]any{
			"substituteFrom": []any{
				map[string]any{"kind": "ConfigMap", "name": fluxcore.ClusterVarsName},
			},
		}
	}
	if len(root.DependsOn) > 0 {
		deps := make([]any, 0, len(root.DependsOn))
		for _, name := range root.DependsOn {
			deps = append(deps, map[string]any{"name": name})
		}
		spec["dependsOn"] = deps
	}

	obj := &unstructured.Unstructured{Object: map[string]any{"spec": spec}}
	obj.SetAPIVersion("kustomize.toolkit.fluxcd.io/v1")
	obj.SetKind("Kustomization")
	obj.SetNamespace(rootNamespace)
	obj.SetName(root.Name)

	ri := c.dyn.Resource(kustomizationGVR).Namespace(rootNamespace)
	existing, err := ri.Get(ctx, root.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = ri.Create(ctx, obj, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	obj.SetResourceVersion(existing.GetResourceVersion())
	_, err = ri.Update(ctx, obj, metav1.UpdateOptions{})
	return err
}
