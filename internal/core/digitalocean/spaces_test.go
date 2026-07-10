package digitalocean

import (
	"context"
	"testing"
)

type fakeSpacesAPI struct {
	buckets []Bucket
	created string
	deleted string
}

func (f *fakeSpacesAPI) ListBuckets(context.Context) ([]Bucket, error) { return f.buckets, nil }
func (f *fakeSpacesAPI) CreateBucket(_ context.Context, name string) error {
	f.created = name
	return nil
}

func (f *fakeSpacesAPI) DeleteBucket(_ context.Context, name string) error {
	f.deleted = name
	return nil
}

func TestSpacesOpsDelegate(t *testing.T) {
	f := &fakeSpacesAPI{buckets: []Bucket{{Name: "b1"}}}
	got, err := ListBuckets(context.Background(), f)
	if err != nil || len(got) != 1 || got[0].Name != "b1" {
		t.Fatalf("list: got %+v err %v", got, err)
	}
	if err := CreateBucket(context.Background(), f, "new"); err != nil || f.created != "new" {
		t.Fatalf("create: created=%q err=%v", f.created, err)
	}
	if err := DeleteBucket(context.Background(), f, "old"); err != nil || f.deleted != "old" {
		t.Fatalf("delete: deleted=%q err=%v", f.deleted, err)
	}
}
