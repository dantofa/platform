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

// BucketUnlinker is the DigitalOcean-authenticated surface for tearing a backup
// bucket down: revoking the keys dctl scoped to it, and emptying then deleting
// the bucket. Like BucketProvisioner it is driven from outside the cluster (the
// DO token stays with the caller).
type BucketUnlinker interface {
	// ScopedKeys returns the access key ids of the dctl-minted keys scoped to the
	// bucket (empty when none remain).
	ScopedKeys(ctx context.Context, bucket string) ([]string, error)
	// RevokeCredential deletes a key by its access key id.
	RevokeCredential(ctx context.Context, accessKey string) error
	// EmptyBucket deletes every object (all versions) in the bucket. A no-op if
	// the bucket is already absent.
	EmptyBucket(ctx context.Context, bucket string) error
	// DeleteBucket deletes the (now empty) bucket. A no-op if already absent.
	DeleteBucket(ctx context.Context, name string) error
}

// UnlinkResult reports what a teardown removed.
type UnlinkResult struct {
	Bucket      string   `json:"bucket"`
	RevokedKeys []string `json:"revoked_keys"`
}

// Unlink tears down a bucket previously linked for backups: it revokes the
// bucket-scoped keys dctl minted, empties the bucket (all object versions), and
// deletes it. Idempotent — safe to re-run after a partial teardown (already
// revoked keys and an absent bucket are no-ops).
func Unlink(ctx context.Context, u BucketUnlinker, bucket string) (UnlinkResult, error) {
	keys, err := u.ScopedKeys(ctx, bucket)
	if err != nil {
		return UnlinkResult{}, err
	}
	for _, key := range keys {
		if err := u.RevokeCredential(ctx, key); err != nil {
			return UnlinkResult{}, err
		}
	}
	if err := u.EmptyBucket(ctx, bucket); err != nil {
		return UnlinkResult{}, err
	}
	if err := u.DeleteBucket(ctx, bucket); err != nil {
		return UnlinkResult{}, err
	}
	return UnlinkResult{Bucket: bucket, RevokedKeys: keys}, nil
}

// VeleroCredentialsFile renders the AWS-style credentials file Velero's S3
// plugin expects (a single default profile), for the cluster-side Secret.
func VeleroCredentialsFile(c Credential) string {
	return fmt.Sprintf(
		"[default]\naws_access_key_id=%s\naws_secret_access_key=%s\n",
		c.AccessKey, c.SecretKey,
	)
}

// CredentialStore persists a Spaces credential (and the bucket coordinates) into
// a cluster and reports the access key currently stored there, so rotation can
// revoke the superseded one. Implemented against Kubernetes — the DO token never
// reaches it.
type CredentialStore interface {
	// CurrentAccessKey returns the access key id currently stored, or "" if none.
	CurrentAccessKey(ctx context.Context) (string, error)
	// Store writes the credential and bucket coordinates into the cluster.
	Store(ctx context.Context, cred Credential, coords BucketCoordinates) error
}

// LinkAndStore ensures the versioned bucket exists, mints a fresh bucket-scoped
// credential, persists it into the cluster, and only then revokes the
// previously-stored credential. Idempotent, and this is also the rotate path:
// the new credential is stored before the old is revoked, so an interrupted run
// never leaves the cluster without a usable key.
func LinkAndStore(ctx context.Context, p BucketProvisioner, store CredentialStore, bucket string) (LinkResult, error) {
	prior, err := store.CurrentAccessKey(ctx)
	if err != nil {
		return LinkResult{}, err
	}
	res, err := Link(ctx, p, bucket)
	if err != nil {
		return LinkResult{}, err
	}
	if err := store.Store(ctx, res.Credential, res.Coordinates); err != nil {
		// The new key exists in DO but isn't persisted; revoke it (best effort)
		// rather than leak it, and leave the prior key intact so the cluster
		// keeps a working credential.
		_ = p.RevokeCredential(ctx, res.Credential.AccessKey)
		return LinkResult{}, err
	}
	if err := RevokePrior(ctx, p, prior); err != nil {
		return res, err
	}
	return res, nil
}
