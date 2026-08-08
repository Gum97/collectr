package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// S3 stores objects in anything that speaks the S3 API.
//
// minio-go rather than the AWS SDK, because the deployments this targets are
// mostly not AWS: MinIO next to the app, Cloudflare R2, FPT Cloud Object
// Storage, Vietnam's other providers. All of them are S3-compatible endpoints,
// and this client is built around that case -- including path-style addressing,
// which MinIO needs and which the AWS SDK makes awkward to force.
//
// What is stored here is already encrypted. Each attachment is sealed under its
// data subject's own key before it reaches Put, so the bucket holds ciphertext
// and nothing else: a provider, a misconfigured bucket policy, or a copied
// backup yields bytes nobody can read. That is also why SignedURL is not on the
// attachment path -- see the note there.
type S3 struct {
	client *minio.Client
	bucket string
}

// S3Options configures the driver.
type S3Options struct {
	// Endpoint is the host, with or without a scheme: s3.amazonaws.com,
	// minio:9000, <account>.r2.cloudflarestorage.com, s3.cloud.fpt.vn.
	Endpoint  string
	Bucket    string
	Region    string
	AccessKey string
	SecretKey string
	UseSSL    bool
}

// NewS3 connects to an S3-compatible endpoint and checks the bucket is reachable.
//
// The bucket check runs at startup on purpose. A wrong key or a missing bucket
// otherwise surfaces on the first upload -- which is a respondent losing the
// document they just attached, hours after the deployment looked healthy.
func NewS3(ctx context.Context, o S3Options) (*S3, error) {
	if o.Endpoint == "" || o.Bucket == "" {
		return nil, errors.New("s3 storage needs STORAGE_S3_ENDPOINT and STORAGE_S3_BUCKET")
	}
	if o.AccessKey == "" || o.SecretKey == "" {
		return nil, errors.New("s3 storage needs STORAGE_S3_ACCESS_KEY and STORAGE_S3_SECRET_KEY")
	}

	// A scheme in the endpoint is a common way to write it and a hard error for
	// this client, so it is stripped rather than rejected -- and it decides SSL
	// when it is present, because someone who wrote https:// meant it.
	endpoint := o.Endpoint
	useSSL := o.UseSSL
	if rest, ok := strings.CutPrefix(endpoint, "https://"); ok {
		endpoint, useSSL = rest, true
	} else if rest, ok := strings.CutPrefix(endpoint, "http://"); ok {
		endpoint, useSSL = rest, false
	}
	endpoint = strings.TrimSuffix(endpoint, "/")

	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(o.AccessKey, o.SecretKey, ""),
		Secure: useSSL,
		Region: o.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("connecting to object storage: %w", err)
	}

	ok, err := client.BucketExists(ctx, o.Bucket)
	if err != nil {
		return nil, fmt.Errorf("checking bucket %q: %w", o.Bucket, err)
	}
	if !ok {
		// Not created here. Creating a bucket needs privileges the application
		// should not hold, and a bucket made silently at boot gets none of the
		// versioning, lifecycle or access policy the operator meant to set.
		return nil, fmt.Errorf("bucket %q does not exist or the credentials cannot see it", o.Bucket)
	}
	return &S3{client: client, bucket: o.Bucket}, nil
}

// Put writes an object.
func (s *S3) Put(ctx context.Context, key string, r io.Reader) (int64, error) {
	if err := validKey(key); err != nil {
		return 0, err
	}
	// -1 because the caller hands over a reader without a length. minio streams
	// it in parts rather than buffering the whole object to discover the size,
	// which is what keeps a large attachment from being held in memory twice.
	info, err := s.client.PutObject(ctx, s.bucket, key, r, -1, minio.PutObjectOptions{
		ContentType: "application/octet-stream",
	})
	if err != nil {
		return 0, fmt.Errorf("writing object: %w", err)
	}
	return info.Size, nil
}

// Get opens an object.
func (s *S3) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := validKey(key); err != nil {
		return nil, err
	}
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("opening object: %w", err)
	}
	// GetObject is lazy: it returns without contacting the server, so a missing
	// object would otherwise surface as a read error somewhere downstream,
	// after headers have been written.
	if _, err := obj.Stat(); err != nil {
		_ = obj.Close()
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("reading object: %w", err)
	}
	return obj, nil
}

// Delete removes an object. Deleting something absent is not an error.
func (s *S3) Delete(ctx context.Context, key string) error {
	if err := validKey(key); err != nil {
		return err
	}
	err := s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{})
	if err != nil && minio.ToErrorResponse(err).Code != "NoSuchKey" {
		return fmt.Errorf("deleting object: %w", err)
	}
	return nil
}

// SignedURL returns a presigned GET for the object.
//
// Not used by the attachment path, and it must not be: every attachment is
// encrypted under its data subject's key, so a browser following this link
// receives ciphertext. Attachments are served by the application, which holds
// the key, checks the capability and writes the audit entry -- see
// internal/modules/files/api. This exists because the interface declares it and
// a future unencrypted object would want it.
func (s *S3) SignedURL(key string, ttl time.Duration) (string, error) {
	if err := validKey(key); err != nil {
		return "", err
	}
	u, err := s.client.PresignedGetObject(context.Background(), s.bucket, key, ttl, url.Values{})
	if err != nil {
		return "", fmt.Errorf("presigning object: %w", err)
	}
	return u.String(), nil
}

// validKey rejects a key that could escape the prefix it belongs to.
//
// Keys are generated by the application, never by a caller. This is the same
// check the local driver makes before touching the filesystem, kept here so the
// two drivers cannot disagree about what a legal key is.
func validKey(key string) error {
	if key == "" || strings.Contains(key, "..") || strings.HasPrefix(key, "/") {
		return ErrInvalidKey
	}
	return nil
}
