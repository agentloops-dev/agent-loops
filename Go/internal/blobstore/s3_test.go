package blobstore

import (
	"bytes"
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"
)

const testBucket = "artifacts"

// fakeS3 is an in-process S3 (gofakes3) with one bucket, and a client for it.
func fakeS3(t *testing.T) *s3.Client {
	t.Helper()
	backend := s3mem.New()
	srv := httptest.NewServer(gofakes3.New(backend, gofakes3.WithoutVersioning()).Server())
	t.Cleanup(srv.Close)
	if err := backend.CreateBucket(testBucket); err != nil {
		t.Fatal(err)
	}
	return NewS3Client(BucketConfig{
		Endpoint:    srv.URL,
		Region:      "us-east-1",
		PathStyle:   true,
		Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""),
		Timeout:     10 * time.Second,
	})
}

func fakeBucket(t *testing.T, prefix string) *Bucket {
	return NewBucket(fakeS3(t), testBucket, prefix)
}

// The bucket has the folder's layout under its prefix.
func TestBucketKeysAreTheFolderLayout(t *testing.T) {
	ctx := context.Background()
	c := fakeS3(t)
	b := NewBucket(c, testBucket, "/org/a/")
	in, _, err := b.Put(ctx, []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	key := "org/a/blobs/sha256/" + in.Digest.Hex()
	if _, err := c.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(testBucket), Key: aws.String(key)}); err != nil {
		t.Fatalf("no object at %s: %v", key, err)
	}
	// Another prefix is another store.
	other := NewBucket(c, testBucket, "org/b")
	if _, err := other.Get(ctx, in.Digest); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other prefix: %v", err)
	}
	if p, _ := other.List(ctx, "", 0); len(p.Blobs) != 0 {
		t.Fatalf("other prefix lists %d", len(p.Blobs))
	}
	if got := NewBucket(c, testBucket, "").key(in.Digest); got != in.Digest.Path() {
		t.Fatal(got)
	}
}

// A put never overwrites: the conditional create refuses, and the put says it was not new.
func TestBucketPutIsAConditionalCreate(t *testing.T) {
	ctx := context.Background()
	c := fakeS3(t)
	b := NewBucket(c, testBucket, "")
	data := []byte("once")
	d := Of(data)
	_, err := c.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(testBucket), Key: aws.String(b.key(d)), Body: bytes.NewReader(data), IfNoneMatch: aws.String("*"),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(testBucket), Key: aws.String(b.key(d)), Body: bytes.NewReader([]byte("evil")), IfNoneMatch: aws.String("*"),
	})
	if status(err) != 412 {
		t.Fatalf("second conditional create: %v", err)
	}
	if got, err := b.Get(ctx, d); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("%q %v", got, err)
	}
}

// Bytes in the bucket that do not hash to their key are reported on a get.
func TestBucketDetectsCorruptBlob(t *testing.T) {
	ctx := context.Background()
	c := fakeS3(t)
	b := NewBucket(c, testBucket, "p")
	in, _, _ := b.Put(ctx, []byte("good"))
	if _, err := c.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(testBucket), Key: aws.String(b.key(in.Digest)), Body: strings.NewReader("evil"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Get(ctx, in.Digest); !errors.Is(err, ErrMismatch) {
		t.Fatalf("%v", err)
	}
}

// Keys under the prefix that are not digests are not blobs, and do not break the paging.
func TestBucketListSkipsOtherKeys(t *testing.T) {
	ctx := context.Background()
	c := fakeS3(t)
	b := NewBucket(c, testBucket, "")
	for _, k := range []string{"blobs/sha256/.tmp", "blobs/sha256/zz", "blobs/other"} {
		if _, err := c.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(testBucket), Key: aws.String(k), Body: strings.NewReader("x")}); err != nil {
			t.Fatal(err)
		}
	}
	for _, s := range []string{"a", "b", "c"} {
		if _, _, err := b.Put(ctx, []byte(s)); err != nil {
			t.Fatal(err)
		}
	}
	p, err := b.List(ctx, "", 2)
	if err != nil || len(p.Blobs) != 2 || p.Next == "" {
		t.Fatalf("%+v %v", p, err)
	}
	p, err = b.List(ctx, p.Next, 2)
	if err != nil || len(p.Blobs) != 1 || p.Next != "" {
		t.Fatalf("%+v %v", p, err)
	}
}

// TestBucketOnARealEndpoint runs the store on a real S3-compatible service (a scratch MinIO,
// or a bucket) when AGENTS_TEST_S3_ENDPOINT, _BUCKET, _ACCESS_KEY and _SECRET_KEY are set;
// CI and a plain run skip it. It writes under a new prefix and deletes what it wrote.
func TestBucketOnARealEndpoint(t *testing.T) {
	endpoint := os.Getenv("AGENTS_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("AGENTS_TEST_S3_ENDPOINT is not set")
	}
	ctx := context.Background()
	region := os.Getenv("AGENTS_TEST_S3_REGION")
	if region == "" {
		region = "us-east-1"
	}
	c := NewS3Client(BucketConfig{
		Endpoint: endpoint, Region: region, PathStyle: true,
		Credentials: credentials.NewStaticCredentialsProvider(os.Getenv("AGENTS_TEST_S3_ACCESS_KEY"), os.Getenv("AGENTS_TEST_S3_SECRET_KEY"), ""),
	})
	b := NewBucket(c, os.Getenv("AGENTS_TEST_S3_BUCKET"), "test-"+time.Now().UTC().Format("20060102T150405.000000000"))
	var stored []Digest
	t.Cleanup(func() {
		for _, d := range stored {
			_ = b.Delete(ctx, d)
		}
	})
	for _, s := range []string{"one", "two", "three"} {
		in, created, err := b.Put(ctx, []byte(s))
		if err != nil || !created {
			t.Fatalf("put %s: %v %v", s, created, err)
		}
		stored = append(stored, in.Digest)
		if _, created, err := b.Put(ctx, []byte(s)); err != nil || created {
			t.Fatalf("dedupe %s: %v %v", s, created, err)
		}
		if got, err := b.Get(ctx, in.Digest); err != nil || string(got) != s {
			t.Fatalf("get %s: %q %v", s, got, err)
		}
	}
	p, err := b.List(ctx, "", 2)
	if err != nil || len(p.Blobs) != 2 || p.Next == "" {
		t.Fatalf("list: %+v %v", p, err)
	}
	if p, err = b.List(ctx, p.Next, 2); err != nil || len(p.Blobs) != 1 || p.Next != "" {
		t.Fatalf("list 2: %+v %v", p, err)
	}
	if err := b.Delete(ctx, stored[0]); err != nil {
		t.Fatal(err)
	}
	if err := b.Delete(ctx, stored[0]); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
}
