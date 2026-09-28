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

	// Defaults for the database-backup wiring: a second bucket, its own
	// bucket-scoped key, and a Secret in the shape the CNPG barman-cloud plugin
	// reads. Deliberately not the Velero pair -- that key is ReadWrite on the
	// cluster's disaster-recovery bucket, so handing it to tenant databases would
	// let a compromised Postgres pod delete the cluster's backups.
	DefaultDBNamespace     = "cnpg-system"
	DefaultDBSecretName    = "db-backup-credential"
	DefaultDBConfigMapName = "db-backup-target"

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
	shape         core.SecretShape
}

var _ core.CredentialStore = (*CredentialStore)(nil)

// NewCredentialStore targets the given namespace and object names; empty names
// fall back to the defaults. The shape decides how the credential is rendered
// into the Secret -- a nil shape means Velero's, the original consumer.
func NewCredentialStore(client *kube.Client, namespace, secretName, configMapName string, shape core.SecretShape) *CredentialStore {
	if secretName == "" {
		secretName = DefaultSecretName
	}
	if configMapName == "" {
		configMapName = DefaultConfigMapName
	}
	if shape == nil {
		shape = core.VeleroSecretData
	}
	return &CredentialStore{
		kube:          client,
		namespace:     namespace,
		secretName:    secretName,
		configMapName: configMapName,
		shape:         shape,
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

// Store writes the credential (in the configured shape) and the coordinates. The
// target namespace is ensured first so the write does not race the Flux-managed
// namespace that also declares it.
func (s *CredentialStore) Store(ctx context.Context, cred core.Credential, coords core.BucketCoordinates) error {
	if err := s.kube.EnsureNamespace(ctx, s.namespace); err != nil {
		return err
	}
	if err := s.kube.ApplySecret(
		ctx, s.namespace, s.secretName,
		s.shape(cred),
		map[string]string{accessKeyAnnotation: cred.AccessKey},
	); err != nil {
		return err
	}
	return s.kube.ApplyConfigMap(ctx, s.namespace, s.configMapName, coords.ConfigMapData())
}
