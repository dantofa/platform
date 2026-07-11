// Package kube is a thin client-go adapter for the cluster-side writes the
// bootstrap/link flow needs: applying (create-or-update) Secrets and ConfigMaps,
// and reading a Secret annotation. It carries no provider knowledge.
package kube

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// Client wraps a Kubernetes clientset.
type Client struct {
	cs kubernetes.Interface
}

// NewFromKubeconfig builds a client from raw kubeconfig bytes (e.g. one fetched
// from the DigitalOcean API).
func NewFromKubeconfig(data []byte) (*Client, error) {
	cfg, err := clientcmd.RESTConfigFromKubeConfig(data)
	if err != nil {
		return nil, err
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &Client{cs: cs}, nil
}

// NewFromPath builds a client from a kubeconfig path; an empty path uses the
// default loading rules ($KUBECONFIG, then ~/.kube/config).
func NewFromPath(path string) (*Client, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if path != "" {
		rules.ExplicitPath = path
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		return nil, err
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &Client{cs: cs}, nil
}

// EnsureNamespace creates the named Namespace if it does not already exist.
// Idempotent: an existing namespace is a no-op.
func (c *Client) EnsureNamespace(ctx context.Context, name string) error {
	_, err := c.cs.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = c.cs.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: name},
		}, metav1.CreateOptions{})
		return err
	}
	return err
}

// ApplySecret creates or updates an Opaque Secret with the given data and
// annotations (annotations are merged onto an existing Secret).
func (c *Client) ApplySecret(ctx context.Context, namespace, name string, data map[string][]byte, annotations map[string]string) error {
	secrets := c.cs.CoreV1().Secrets(namespace)
	existing, err := secrets.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = secrets.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Annotations: annotations},
			Type:       corev1.SecretTypeOpaque,
			Data:       data,
		}, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	existing.Data = data
	if existing.Annotations == nil {
		existing.Annotations = map[string]string{}
	}
	for k, v := range annotations {
		existing.Annotations[k] = v
	}
	_, err = secrets.Update(ctx, existing, metav1.UpdateOptions{})
	return err
}

// ApplyConfigMap creates or updates a ConfigMap with the given data.
func (c *Client) ApplyConfigMap(ctx context.Context, namespace, name string, data map[string]string) error {
	cms := c.cs.CoreV1().ConfigMaps(namespace)
	existing, err := cms.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = cms.Create(ctx, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Data:       data,
		}, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	existing.Data = data
	_, err = cms.Update(ctx, existing, metav1.UpdateOptions{})
	return err
}

// SecretAnnotation returns an annotation value from a Secret, or "" if the
// Secret (or the annotation) is absent.
func (c *Client) SecretAnnotation(ctx context.Context, namespace, name, key string) (string, error) {
	s, err := c.cs.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return s.Annotations[key], nil
}
