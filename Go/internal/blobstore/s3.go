package blobstore

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

// S3API is the part of the AWS SDK's S3 client that the bucket backend calls; *s3.Client is
// one. A test gives it a client whose endpoint is a fake or a scratch MinIO.
type S3API interface {
	PutObject(ctx context.Context, in *s3.PutObjectInput, opts ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	GetObject(ctx context.Context, in *s3.GetObjectInput, opts ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	HeadObject(ctx context.Context, in *s3.HeadObjectInput, opts ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	ListObjectsV2(ctx context.Context, in *s3.ListObjectsV2Input, opts ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
	DeleteObject(ctx context.Context, in *s3.DeleteObjectInput, opts ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
}

// Bucket is a Store in an S3-compatible bucket (#5954, spec §1): the key of a blob is the
// folder's path under the prefix, "<prefix>/blobs/sha256/<hex>", so a bucket and a folder
// have the same layout. A put is a conditional create (If-None-Match: *), so a blob that
// exists is never overwritten, and it carries the bytes' SHA-256, so the bucket refuses bytes
// that a network changed on the way. A get checks the bytes against the digest, as Folder's
// does.
type Bucket struct {
	api    S3API
	bucket string
	prefix string // "" or ends in "/"
}

// NewBucket returns the store in bucket under prefix ("" for the bucket's root).
func NewBucket(api S3API, bucket, prefix string) *Bucket {
	prefix = strings.Trim(prefix, "/")
	if prefix != "" {
		prefix += "/"
	}
	return &Bucket{api: api, bucket: bucket, prefix: prefix}
}

func (b *Bucket) key(d Digest) string { return b.prefix + d.Path() }

func (b *Bucket) dir() string { return b.prefix + "blobs/sha256/" }

// status is the HTTP status of an SDK error, or 0. Compatible stores (MinIO, R2, GCS) do not
// all send S3's error codes, but they all send the status.
func status(err error) int {
	var re *smithyhttp.ResponseError
	if errors.As(err, &re) {
		return re.HTTPStatusCode()
	}
	return 0
}

func notFound(err error) error {
	if status(err) == http.StatusNotFound {
		return ErrNotFound
	}
	return err
}

func (b *Bucket) Put(ctx context.Context, data []byte) (Info, bool, error) {
	return b.PutAs(ctx, Of(data), data)
}

func (b *Bucket) PutAs(ctx context.Context, d Digest, data []byte) (Info, bool, error) {
	if err := checkPut(d, data); err != nil {
		return Info{}, false, err
	}
	// A head first, as Folder's stat: a blob stored already is not sent again.
	if in, err := b.Head(ctx, d); err == nil && in.Size == int64(len(data)) {
		return in, false, nil
	} else if err != nil && !errors.Is(err, ErrNotFound) {
		return Info{}, false, err
	}
	sum, _ := hex.DecodeString(d.Hex())
	// store-bounds: kind=blob; size=1GiB; age=until-deleted; applied=on write (refused), a blob goes only when its caller deletes it
	_, err := b.api.PutObject(ctx, &s3.PutObjectInput{
		Bucket:         aws.String(b.bucket),
		Key:            aws.String(b.key(d)),
		Body:           bytes.NewReader(data),
		ContentLength:  aws.Int64(int64(len(data))),
		ContentType:    aws.String("application/octet-stream"),
		ChecksumSHA256: aws.String(base64.StdEncoding.EncodeToString(sum)),
		IfNoneMatch:    aws.String("*"),
	})
	created := true
	switch s := status(err); {
	case err == nil:
	case s == http.StatusPreconditionFailed || s == http.StatusConflict:
		// 412: another put stored it first. 409: another conditional put of the same key is
		// in flight (S3's ConditionalRequestConflict); its bytes are these bytes.
		created = false
	default:
		return Info{}, false, fmt.Errorf("blobstore: put %s: %w", d, err)
	}
	in, err := b.Head(ctx, d)
	if err != nil {
		return Info{}, false, err
	}
	return in, created, nil
}

func (b *Bucket) get(ctx context.Context, d Digest) (*s3.GetObjectOutput, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	out, err := b.api.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(b.bucket), Key: aws.String(b.key(d))})
	if err != nil {
		return nil, notFound(err)
	}
	return out, nil
}

func (b *Bucket) Get(ctx context.Context, d Digest) ([]byte, error) {
	out, err := b.get(ctx, d)
	if err != nil {
		return nil, err
	}
	defer out.Body.Close()
	data, err := io.ReadAll(io.LimitReader(out.Body, MaxBlobBytes+1))
	if err != nil {
		return nil, err
	}
	if Of(data) != d {
		return nil, ErrMismatch
	}
	return data, nil
}

func (b *Bucket) Open(ctx context.Context, d Digest) (io.ReadCloser, error) {
	out, err := b.get(ctx, d)
	if err != nil {
		return nil, err
	}
	return out.Body, nil
}

func (b *Bucket) Head(ctx context.Context, d Digest) (Info, error) {
	if err := d.Validate(); err != nil {
		return Info{}, err
	}
	out, err := b.api.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(b.bucket), Key: aws.String(b.key(d))})
	if err != nil {
		return Info{}, notFound(err)
	}
	return Info{d, aws.ToInt64(out.ContentLength), aws.ToTime(out.LastModified)}, nil
}

// List asks the bucket for keys in order after the cursor's key and skips any key that is
// not a digest. It asks for one more than the page, so Next is set only when more follow, as
// Folder's is.
func (b *Bucket) List(ctx context.Context, after Digest, limit int) (Page, error) {
	if err := checkAfter(after); err != nil {
		return Page{}, err
	}
	limit = pageLimit(limit)
	dir := b.dir()
	start := ""
	if after != "" {
		start = b.key(after)
	}
	var blobs []Info
	for len(blobs) <= limit {
		out, err := b.api.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:     aws.String(b.bucket),
			Prefix:     aws.String(dir),
			StartAfter: aws.String(start),
			MaxKeys:    aws.Int32(int32(limit + 1 - len(blobs))), //nolint:gosec // at most MaxListLimit+1
		})
		if err != nil {
			return Page{}, notFound(err)
		}
		for _, o := range out.Contents {
			k := aws.ToString(o.Key)
			start = k
			d := Digest(prefix + strings.TrimPrefix(k, dir))
			if d.Valid() && len(blobs) <= limit {
				blobs = append(blobs, Info{d, aws.ToInt64(o.Size), aws.ToTime(o.LastModified)})
			}
		}
		if !aws.ToBool(out.IsTruncated) || len(out.Contents) == 0 {
			break
		}
	}
	var p Page
	if len(blobs) > limit {
		blobs = blobs[:limit]
		p.Next = blobs[limit-1].Digest
	}
	p.Blobs = blobs
	return p, nil
}

// Delete heads the blob first, because S3's delete of a missing key succeeds.
func (b *Bucket) Delete(ctx context.Context, d Digest) error {
	if _, err := b.Head(ctx, d); err != nil {
		return err
	}
	_, err := b.api.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(b.bucket), Key: aws.String(b.key(d))})
	return err
}

// BucketConfig is how to reach an S3-compatible bucket.
type BucketConfig struct {
	// Endpoint is the service's URL; "" is AWS's own for Region. A MinIO, R2 or other
	// compatible store needs it.
	Endpoint string
	Region   string
	// PathStyle puts the bucket in the path ("host/bucket/key"), as MinIO wants, not in the
	// host name.
	PathStyle bool
	// Credentials sign each request; nil sends them unsigned (a public bucket only).
	Credentials aws.CredentialsProvider
	// HTTPClient; nil is the SDK's.
	HTTPClient *http.Client
	// Timeout bounds each request; 0 is a minute.
	Timeout time.Duration
}

// NewS3Client returns an SDK client for c. It sends a request's checksum only when the
// operation needs one, or when the caller gives one (a put's SHA-256), so compatible stores
// that do not know the SDK's default CRC32 headers take its requests.
func NewS3Client(c BucketConfig) *s3.Client {
	timeout := c.Timeout
	if timeout == 0 {
		timeout = time.Minute
	}
	hc := c.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: timeout}
	}
	return s3.New(s3.Options{
		Region:                     c.Region,
		Credentials:                c.Credentials,
		HTTPClient:                 hc,
		UsePathStyle:               c.PathStyle,
		BaseEndpoint:               nilIfEmpty(c.Endpoint),
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
