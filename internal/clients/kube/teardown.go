package kube

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	teardowncore "github.com/dantofa/platform/internal/core/teardown"
)

var _ teardowncore.KubeAPI = (*Client)(nil)

// tunnelNamespace is where the Cloudflare Tunnel controller (and the cloudflared
// Deployment it manages) run; mirrors flux/ingress/tunnel.
const tunnelNamespace = "cloudflare-tunnel-system"

// IngressHosts returns every hostname declared across all Ingresses in every
// namespace (deduplicated). Implements teardowncore.KubeAPI.
func (c *Client) IngressHosts(ctx context.Context) ([]string, error) {
	list, err := c.cs.NetworkingV1().Ingresses(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var hosts []string
	for i := range list.Items {
		for _, rule := range list.Items[i].Spec.Rules {
			if rule.Host != "" && !seen[rule.Host] {
				seen[rule.Host] = true
				hosts = append(hosts, rule.Host)
			}
		}
	}
	return hosts, nil
}

// DeleteIngresses deletes every Ingress in every namespace, returning the count
// deleted. Implements teardowncore.KubeAPI.
func (c *Client) DeleteIngresses(ctx context.Context) (int, error) {
	list, err := c.cs.NetworkingV1().Ingresses(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return 0, err
	}
	n := 0
	for i := range list.Items {
		ing := &list.Items[i]
		err := c.cs.NetworkingV1().Ingresses(ing.Namespace).Delete(ctx, ing.Name, metav1.DeleteOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			return n, fmt.Errorf("deleting ingress %s/%s: %w", ing.Namespace, ing.Name, err)
		}
		n++
	}
	return n, nil
}

// SuspendKustomizations sets spec.suspend=true on every Flux Kustomization so it
// stops reconciling (and cannot re-create the Ingresses teardown deletes). An
// absent CRD (Flux not installed) is a no-op. Implements teardowncore.KubeAPI.
func (c *Client) SuspendKustomizations(ctx context.Context) (int, error) {
	ri := c.dyn.Resource(kustomizationGVR)
	list, err := ri.List(ctx, metav1.ListOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			return 0, nil
		}
		return 0, err
	}
	patch := []byte(`{"spec":{"suspend":true}}`)
	n := 0
	for i := range list.Items {
		item := &list.Items[i]
		_, err := ri.Namespace(item.GetNamespace()).Patch(
			ctx, item.GetName(), types.MergePatchType, patch, metav1.PatchOptions{},
		)
		if err != nil {
			return n, fmt.Errorf("suspending kustomization %s/%s: %w", item.GetNamespace(), item.GetName(), err)
		}
		n++
	}
	return n, nil
}

// StopTunnelController deletes every Deployment in the tunnel controller's
// namespace so cloudflared disconnects (a Cloudflare Tunnel cannot be deleted
// while it has active connections). An absent namespace is a no-op returning 0
// (e.g. on DOKS). Flux is already suspended by the time this runs, so the
// Deployments are not re-created. Implements teardowncore.KubeAPI.
func (c *Client) StopTunnelController(ctx context.Context) (int, error) {
	deploys := c.cs.AppsV1().Deployments(tunnelNamespace)
	list, err := deploys.List(ctx, metav1.ListOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return 0, nil
		}
		return 0, err
	}
	n := 0
	for i := range list.Items {
		if err := deploys.Delete(ctx, list.Items[i].Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return n, fmt.Errorf("deleting tunnel deployment %s: %w", list.Items[i].Name, err)
		}
		n++
	}
	return n, nil
}

// SecretValue returns one key's value from a Secret, or "" (no error) if the
// Secret or the key is absent — so a caller can probe several candidate
// locations for a credential.
func (c *Client) SecretValue(ctx context.Context, namespace, name, key string) (string, error) {
	s, err := c.cs.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(s.Data[key]), nil
}

// ConfigMapValue returns one key's value from a ConfigMap, or "" (no error) if
// the ConfigMap or the key is absent.
func (c *Client) ConfigMapValue(ctx context.Context, namespace, name, key string) (string, error) {
	cm, err := c.cs.CoreV1().ConfigMaps(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return cm.Data[key], nil
}

// terraformGVR is tofu-controller's Terraform kind.
var terraformGVR = schema.GroupVersionResource{
	Group:    "infra.contrib.fluxcd.io",
	Version:  "v1alpha2",
	Resource: "terraforms",
}

// listTerraforms lists every Terraform object; an absent CRD is an empty list.
func (c *Client) listTerraforms(ctx context.Context) ([]unstructured.Unstructured, error) {
	list, err := c.dyn.Resource(terraformGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			return nil, nil
		}
		return nil, err
	}
	return list.Items, nil
}

// TerraformObjects counts the Terraform objects in every namespace. Implements
// teardowncore.TerraformAPI.
func (c *Client) TerraformObjects(ctx context.Context) (int, error) {
	items, err := c.listTerraforms(ctx)
	return len(items), err
}

// DeleteTerraformObjects requests deletion of every Terraform object, returning
// the count. Implements teardowncore.TerraformAPI.
func (c *Client) DeleteTerraformObjects(ctx context.Context) (int, error) {
	items, err := c.listTerraforms(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for i := range items {
		item := &items[i]
		err := c.dyn.Resource(terraformGVR).Namespace(item.GetNamespace()).Delete(ctx, item.GetName(), metav1.DeleteOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			return n, fmt.Errorf("deleting terraform %s/%s: %w", item.GetNamespace(), item.GetName(), err)
		}
		n++
	}
	return n, nil
}
