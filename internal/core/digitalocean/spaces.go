// Package digitalocean holds the framework-free application logic for
// DigitalOcean resources (Spaces buckets and DOKS clusters). It imports neither
// a CLI framework nor the client SDKs: the provider surfaces are reached through
// the interfaces declared here, which the clients adapter satisfies.
package digitalocean

import (
	"context"
	"time"
)

// Bucket is a Spaces bucket as surfaced to the user.
type Bucket struct {
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// SpacesAPI is the Spaces (S3) surface this package depends on.
type SpacesAPI interface {
	ListBuckets(ctx context.Context) ([]Bucket, error)
	CreateBucket(ctx context.Context, name string) error
	DeleteBucket(ctx context.Context, name string) error
}

// ListBuckets returns all Spaces buckets.
func ListBuckets(ctx context.Context, client SpacesAPI) ([]Bucket, error) {
	return client.ListBuckets(ctx)
}

// CreateBucket creates a bucket, surfacing the raw provider error if it already
// exists.
func CreateBucket(ctx context.Context, client SpacesAPI, name string) error {
	return client.CreateBucket(ctx, name)
}

// DeleteBucket deletes a bucket.
func DeleteBucket(ctx context.Context, client SpacesAPI, name string) error {
	return client.DeleteBucket(ctx, name)
}
