package digitalocean

import (
	"context"
	"fmt"
)

// Credential is a Spaces access key / secret pair.
type Credential struct {
	AccessKey string
	SecretKey string
}

// BucketCoordinates locates a Spaces bucket for downstream configuration (the
// values a Velero BackupStorageLocation needs). These become the cluster-side
// ConfigMap that Flux substitutes into the backup manifests.
type BucketCoordinates struct {
	Bucket   string
	Region   string
	Endpoint string
}

// ConfigMapData renders the coordinates as the string map of a Kubernetes
// ConfigMap (consumed via Flux postBuild.substituteFrom).
func (b BucketCoordinates) ConfigMapData() map[string]string {
	return map[string]string{
		"bucket":   b.Bucket,
		"region":   b.Region,
		"endpoint": b.Endpoint,
	}
}

// LinkResult is the outcome of a link/rotate: the fresh credential plus the
// bucket coordinates, ready to be written into the cluster (Secret + ConfigMap).
type LinkResult struct {
	Coordinates BucketCoordinates
	Credential  Credential
}

// BucketProvisioner is the DigitalOcean-authenticated surface for provisioning a
// backup bucket and its cluster-scoped credential. Minting/revoking keys needs a
// DO token; the token lives with the caller (never in-cluster), so this is only
// ever driven from outside the cluster.
type BucketProvisioner interface {
	// EnsureBucket creates the bucket if absent and enables versioning.
	// Idempotent. Returns the coordinates downstream config needs.
	EnsureBucket(ctx context.Context, name string) (BucketCoordinates, error)
	// CreateScopedCredential mints a fresh key scoped to the one bucket with
	// read/write/delete permission (the narrowest DO Spaces allows).
	CreateScopedCredential(ctx context.Context, bucket string) (Credential, error)
	// RevokeCredential deletes a previously-issued key by its access key id.
	RevokeCredential(ctx context.Context, accessKey string) error
}

// Link ensures the versioned bucket exists and mints a fresh bucket-scoped
// credential for it. It is idempotent with respect to the bucket, and it only
// *mints* — never revokes — so a downstream failure to persist the new key can
// never leave the cluster without a valid one. Rotation is the same call
// followed by RevokePrior once the new credential is persisted.
func Link(ctx context.Context, p BucketProvisioner, bucket string) (LinkResult, error) {
	coords, err := p.EnsureBucket(ctx, bucket)
	if err != nil {
		return LinkResult{}, err
	}
	cred, err := p.CreateScopedCredential(ctx, bucket)
	if err != nil {
		return LinkResult{}, err
	}
	return LinkResult{Coordinates: coords, Credential: cred}, nil
}

// RevokePrior revokes a superseded credential. Call it only after the new
// credential has been persisted, so rotation never breaks an in-flight write. A
// no-op when priorAccessKey is empty (first link).
func RevokePrior(ctx context.Context, p BucketProvisioner, priorAccessKey string) error {
	if priorAccessKey == "" {
		return nil
	}
	return p.RevokeCredential(ctx, priorAccessKey)
}

// VeleroCredentialsFile renders the AWS-style credentials file Velero's S3
// plugin expects (a single default profile), for the cluster-side Secret.
func VeleroCredentialsFile(c Credential) string {
	return fmt.Sprintf(
		"[default]\naws_access_key_id=%s\naws_secret_access_key=%s\n",
		c.AccessKey, c.SecretKey,
	)
}
