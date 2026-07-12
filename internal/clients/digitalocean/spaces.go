package digitalocean

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/digitalocean/godo"

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
//
// Credentials come from $SPACES_ACCESS_KEY_ID / $SPACES_SECRET_ACCESS_KEY when
// set; otherwise the client mints an *ephemeral* full-access Spaces key via the
// DO API (from a DO token) and Close revokes it — so standing Spaces credentials
// are optional and the DO token alone suffices.
type SpacesClient struct {
	s3        *s3.Client
	region    string
	doClient  *godo.Client // set only when built from a DO token
	ephemeral string       // ephemeral access key id to revoke on Close (else "")
}

func spacesEndpoint(region string) string {
	return fmt.Sprintf("https://%s.digitaloceanspaces.com", region)
}

// SpacesClient satisfies both the bucket CRUD surface and the provisioning
// surface the bootstrap/link flow needs.
var (
	_ core.SpacesAPI         = (*SpacesClient)(nil)
	_ core.BucketProvisioner = (*SpacesClient)(nil)
	_ core.BucketUnlinker    = (*SpacesClient)(nil)
)

func resolveRegion(region string) string {
	if region == "" {
		region = os.Getenv(spacesRegionEnv)
	}
	if region == "" {
		region = defaultRegion
	}
	return region
}

func newS3(region, key, secret string) *s3.Client {
	return s3.New(s3.Options{
		Region:       region,
		BaseEndpoint: aws.String(fmt.Sprintf("https://%s.digitaloceanspaces.com", region)),
		Credentials:  credentials.NewStaticCredentialsProvider(key, secret, ""),
	})
}

// NewSpacesClient builds a Spaces client for the region (falling back to
// $DIGITALOCEAN_SPACES_REGION then nyc3). It prefers standing Spaces keys from
// the environment; absent those, it mints an ephemeral full-access key via the
// DO API (token from arg or $DIGITALOCEAN_ACCESS_TOKEN) — revoke it with Close.
func NewSpacesClient(ctx context.Context, region, token string) (*SpacesClient, error) {
	region = resolveRegion(region)

	key, secret := os.Getenv(spacesKeyEnv), os.Getenv(spacesSecretEnv)
	if key != "" && secret != "" {
		return &SpacesClient{s3: newS3(region, key, secret), region: region}, nil
	}

	token = resolveDOToken(token)
	if token == "" {
		return nil, MissingCredentials(fmt.Sprintf(
			"set $%s and $%s, or a DigitalOcean token (--token / $%s) to mint an ephemeral key.",
			spacesKeyEnv, spacesSecretEnv, tokenEnv,
		))
	}
	doClient := godo.NewFromToken(token)
	created, _, err := doClient.SpacesKeys.Create(ctx, &godo.SpacesKeyCreateRequest{
		Name:   fmt.Sprintf("dctl-ephemeral-%d", time.Now().UnixNano()),
		Grants: []*godo.Grant{{Bucket: "", Permission: godo.SpacesKeyFullAccess}},
	})
	if err != nil {
		return nil, apiError(err)
	}
	return &SpacesClient{
		s3:        newS3(region, created.AccessKey, created.SecretKey),
		region:    region,
		doClient:  doClient,
		ephemeral: created.AccessKey,
	}, nil
}

// Close revokes the ephemeral Spaces key, if one was minted (a no-op for static
// credentials). Callers should defer it so the key is removed even on error.
func (c *SpacesClient) Close() error {
	if c.ephemeral == "" || c.doClient == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := c.doClient.SpacesKeys.Delete(ctx, c.ephemeral); err != nil {
		return apiError(err)
	}
	return nil
}

// ListBuckets returns every Spaces bucket.
func (c *SpacesClient) ListBuckets(ctx context.Context) ([]core.Bucket, error) {
	var out *s3.ListBucketsOutput
	err := c.retry(func() (err error) {
		out, err = c.s3.ListBuckets(ctx, &s3.ListBucketsInput{})
		return err
	})
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
	err := c.retry(func() error {
		_, err := c.s3.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(name)})
		return err
	})
	if err != nil {
		return apiError(err)
	}
	return nil
}

// DeleteBucket deletes a bucket (must be empty). Idempotent: an absent bucket is
// not an error.
func (c *SpacesClient) DeleteBucket(ctx context.Context, name string) error {
	err := c.retry(func() error {
		_, err := c.s3.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(name)})
		return err
	})
	if err != nil && !isNotFound(err) {
		return apiError(err)
	}
	return nil
}

// EnsureBucket creates the bucket if absent and enables versioning. Idempotent:
// a bucket we already own is not an error. Implements core.BucketProvisioner.
func (c *SpacesClient) EnsureBucket(ctx context.Context, name string) (core.BucketCoordinates, error) {
	err := c.retry(func() error {
		_, err := c.s3.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(name)})
		return err
	})
	if err != nil && !alreadyOwned(err) {
		return core.BucketCoordinates{}, apiError(err)
	}
	if _, err := c.s3.PutBucketVersioning(ctx, &s3.PutBucketVersioningInput{
		Bucket: aws.String(name),
		VersioningConfiguration: &s3types.VersioningConfiguration{
			Status: s3types.BucketVersioningStatusEnabled,
		},
	}); err != nil {
		return core.BucketCoordinates{}, apiError(err)
	}
	return core.BucketCoordinates{
		Bucket:   name,
		Region:   c.region,
		Endpoint: spacesEndpoint(c.region),
	}, nil
}

// CreateScopedCredential mints a Spaces key scoped to the one bucket with
// read/write/delete permission (the narrowest DO Spaces offers). Requires a DO
// token. Implements core.BucketProvisioner.
func (c *SpacesClient) CreateScopedCredential(ctx context.Context, bucket string) (core.Credential, error) {
	if c.doClient == nil {
		return core.Credential{}, MissingCredentials(fmt.Sprintf(
			"a DigitalOcean token (--token / $%s) is required to mint a scoped Spaces key.", tokenEnv,
		))
	}
	created, _, err := c.doClient.SpacesKeys.Create(ctx, &godo.SpacesKeyCreateRequest{
		Name:   fmt.Sprintf("dctl-%s-%d", bucket, time.Now().Unix()),
		Grants: []*godo.Grant{{Bucket: bucket, Permission: godo.SpacesKeyReadWrite}},
	})
	if err != nil {
		return core.Credential{}, apiError(err)
	}
	return core.Credential{AccessKey: created.AccessKey, SecretKey: created.SecretKey}, nil
}

// RevokeCredential deletes a Spaces key by access key id. Requires a DO token.
// Implements core.BucketProvisioner.
func (c *SpacesClient) RevokeCredential(ctx context.Context, accessKey string) error {
	if c.doClient == nil {
		return MissingCredentials(fmt.Sprintf(
			"a DigitalOcean token (--token / $%s) is required to revoke a Spaces key.", tokenEnv,
		))
	}
	if _, err := c.doClient.SpacesKeys.Delete(ctx, accessKey); err != nil {
		return apiError(err)
	}
	return nil
}

// ScopedKeys returns the access key ids of the dctl-minted Spaces keys granted
// on the bucket. Requires a DO token. Implements core.BucketUnlinker.
func (c *SpacesClient) ScopedKeys(ctx context.Context, bucket string) ([]string, error) {
	if c.doClient == nil {
		return nil, MissingCredentials(fmt.Sprintf(
			"a DigitalOcean token (--token / $%s) is required to list Spaces keys.", tokenEnv,
		))
	}
	var keys []string
	opt := &godo.ListOptions{PerPage: 200}
	for {
		page, resp, err := c.doClient.SpacesKeys.List(ctx, opt)
		if err != nil {
			return nil, apiError(err)
		}
		for _, k := range page {
			if !strings.HasPrefix(k.Name, "dctl-") {
				continue
			}
			for _, g := range k.Grants {
				if g.Bucket == bucket {
					keys = append(keys, k.AccessKey)
					break
				}
			}
		}
		if resp.Links == nil || resp.Links.IsLastPage() {
			break
		}
		next, err := resp.Links.CurrentPage()
		if err != nil {
			return nil, apiError(err)
		}
		opt.Page = next + 1
	}
	return keys, nil
}

// EmptyBucket deletes every object version and delete marker in the bucket
// (versioning is enabled, so plain deletes would only add markers). A no-op if
// the bucket is already absent. Implements core.BucketUnlinker.
func (c *SpacesClient) EmptyBucket(ctx context.Context, bucket string) error {
	p := s3.NewListObjectVersionsPaginator(c.s3, &s3.ListObjectVersionsInput{Bucket: aws.String(bucket)})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			if isNotFound(err) {
				return nil
			}
			return apiError(err)
		}
		ids := make([]s3types.ObjectIdentifier, 0, len(page.Versions)+len(page.DeleteMarkers))
		for _, v := range page.Versions {
			ids = append(ids, s3types.ObjectIdentifier{Key: v.Key, VersionId: v.VersionId})
		}
		for _, m := range page.DeleteMarkers {
			ids = append(ids, s3types.ObjectIdentifier{Key: m.Key, VersionId: m.VersionId})
		}
		if len(ids) == 0 {
			continue
		}
		if _, err := c.s3.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String(bucket),
			Delete: &s3types.Delete{Objects: ids, Quiet: aws.Bool(true)},
		}); err != nil {
			return apiError(err)
		}
	}
	return nil
}

// retry runs an S3 op, retrying transient auth failures when using an ephemeral
// key (a freshly-minted Spaces key can take a few seconds to become valid).
// Static credentials are called once, so a genuine auth error isn't masked.
func (c *SpacesClient) retry(fn func() error) error {
	if c.ephemeral == "" {
		return fn()
	}
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		if err = fn(); err == nil || !transientAuth(err) {
			return err
		}
		time.Sleep(time.Duration(attempt+1) * time.Second)
	}
	return err
}

// alreadyOwned reports whether an S3 CreateBucket error means the bucket already
// exists under our ownership (so EnsureBucket can treat it as success).
func alreadyOwned(err error) bool {
	var api smithy.APIError
	if errors.As(err, &api) {
		switch api.ErrorCode() {
		case "BucketAlreadyOwnedByYou", "BucketAlreadyExists":
			return true
		}
	}
	return false
}

// isNotFound reports whether an S3 error means the bucket does not exist, so
// empty/delete can treat teardown of an absent bucket as success.
func isNotFound(err error) bool {
	var api smithy.APIError
	if errors.As(err, &api) {
		switch api.ErrorCode() {
		case "NoSuchBucket", "NotFound":
			return true
		}
	}
	return false
}

func transientAuth(err error) bool {
	var api smithy.APIError
	if errors.As(err, &api) {
		switch api.ErrorCode() {
		case "InvalidAccessKeyId", "SignatureDoesNotMatch", "AccessDenied", "Forbidden":
			return true
		}
	}
	return false
}
