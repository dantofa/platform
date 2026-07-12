package digitalocean

import (
	"context"

	"github.com/dantofa/platform/internal/clients/kube"
	core "github.com/dantofa/platform/internal/core/digitalocean"
)

// Defaults for the cluster-side backup wiring.
const (
	DefaultSecretName    = "backup-credential"
	DefaultConfigMapName = "backup-target"

	// credentialsKey is the Secret data key holding the Velero credentials file.
	credentialsKey = "cloud"
	// accessKeyAnnotation records the stored key id so rotation can revoke the
	// prior one without reading the secret material.
	accessKeyAnnotation = "platform.dantofa.io/spaces-access-key"
)

// CredentialStore persists a Spaces credential and bucket coordinates into a
// cluster as a Velero-shaped Secret plus a coordinates ConfigMap. It implements
// core.CredentialStore over the generic kube client.
type CredentialStore struct {
	kube          *kube.Client
	namespace     string
	secretName    string
	configMapName string
}

var _ core.CredentialStore = (*CredentialStore)(nil)

// NewCredentialStore targets the given namespace and object names; empty names
// fall back to the defaults.
func NewCredentialStore(client *kube.Client, namespace, secretName, configMapName string) *CredentialStore {
	if secretName == "" {
		secretName = DefaultSecretName
	}
	if configMapName == "" {
		configMapName = DefaultConfigMapName
	}
	return &CredentialStore{
		kube:          client,
		namespace:     namespace,
		secretName:    secretName,
		configMapName: configMapName,
	}
}

// SecretName returns the target Secret name (after default resolution).
func (s *CredentialStore) SecretName() string { return s.secretName }

// ConfigMapName returns the target ConfigMap name (after default resolution).
func (s *CredentialStore) ConfigMapName() string { return s.configMapName }

// CurrentAccessKey returns the access key id recorded on the existing Secret, or
// "" if none is stored yet.
func (s *CredentialStore) CurrentAccessKey(ctx context.Context) (string, error) {
	return s.kube.SecretAnnotation(ctx, s.namespace, s.secretName, accessKeyAnnotation)
}

// Store writes the credential (as a Velero credentials file) and the coordinates.
// The target namespace (where Velero runs) is ensured first so the write does
// not race the Flux-managed namespace.
func (s *CredentialStore) Store(ctx context.Context, cred core.Credential, coords core.BucketCoordinates) error {
	if err := s.kube.EnsureNamespace(ctx, s.namespace); err != nil {
		return err
	}
	if err := s.kube.ApplySecret(
		ctx, s.namespace, s.secretName,
		map[string][]byte{credentialsKey: []byte(core.VeleroCredentialsFile(cred))},
		map[string]string{accessKeyAnnotation: cred.AccessKey},
	); err != nil {
		return err
	}
	return s.kube.ApplyConfigMap(ctx, s.namespace, s.configMapName, coords.ConfigMapData())
}
