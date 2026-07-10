package digitalocean

import (
	"context"
	"fmt"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	core "github.com/dantofa/platform/internal/core/digitalocean"
)

const (
	spacesKeyEnv    = "SPACES_ACCESS_KEY_ID"
	spacesSecretEnv = "SPACES_SECRET_ACCESS_KEY"
	spacesRegionEnv = "DIGITALOCEAN_SPACES_REGION"
	defaultRegion   = "nyc3"
)

// SpacesClient is a semantic wrapper over the S3 client pointed at the regional
// DigitalOcean Spaces endpoint. Spaces is S3-compatible and not part of the DO
// REST API, so bucket lifecycle goes through S3.
type SpacesClient struct {
	s3 *s3.Client
}

// NewSpacesClient builds a Spaces client for the given region (falling back to
// $DIGITALOCEAN_SPACES_REGION then nyc3), reading credentials from
// $SPACES_ACCESS_KEY_ID / $SPACES_SECRET_ACCESS_KEY.
func NewSpacesClient(region string) (*SpacesClient, error) {
	if region == "" {
		region = os.Getenv(spacesRegionEnv)
	}
	if region == "" {
		region = defaultRegion
	}
	key := os.Getenv(spacesKeyEnv)
	secret := os.Getenv(spacesSecretEnv)
	if key == "" || secret == "" {
		return nil, MissingCredentials(
			fmt.Sprintf("set $%s and $%s.", spacesKeyEnv, spacesSecretEnv),
		)
	}
	endpoint := fmt.Sprintf("https://%s.digitaloceanspaces.com", region)
	client := s3.New(s3.Options{
		Region:       region,
		BaseEndpoint: aws.String(endpoint),
		Credentials:  credentials.NewStaticCredentialsProvider(key, secret, ""),
	})
	return &SpacesClient{s3: client}, nil
}

// ListBuckets returns every Spaces bucket.
func (c *SpacesClient) ListBuckets(ctx context.Context) ([]core.Bucket, error) {
	out, err := c.s3.ListBuckets(ctx, &s3.ListBucketsInput{})
	if err != nil {
		return nil, apiError(err)
	}
	buckets := make([]core.Bucket, 0, len(out.Buckets))
	for _, b := range out.Buckets {
		bucket := core.Bucket{}
		if b.Name != nil {
			bucket.Name = *b.Name
		}
		if b.CreationDate != nil {
			bucket.CreatedAt = *b.CreationDate
		}
		buckets = append(buckets, bucket)
	}
	return buckets, nil
}

// CreateBucket creates a bucket, surfacing the raw S3 error (incl. already-exists).
func (c *SpacesClient) CreateBucket(ctx context.Context, name string) error {
	_, err := c.s3.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(name)})
	if err != nil {
		return apiError(err)
	}
	return nil
}

// DeleteBucket deletes a bucket (must be empty).
func (c *SpacesClient) DeleteBucket(ctx context.Context, name string) error {
	_, err := c.s3.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(name)})
	if err != nil {
		return apiError(err)
	}
	return nil
}
