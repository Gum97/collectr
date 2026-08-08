// Package app implements upload, download and the lifecycle of attachments.
package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/oklog/ulid/v2"

	"github.com/collectr/collectr/internal/modules/files/domain"
	"github.com/collectr/collectr/internal/modules/files/store"
	"github.com/collectr/collectr/internal/platform/crypto"
	"github.com/collectr/collectr/internal/platform/storage"
)

// DownloadTTL is how long a signed download link stays valid.
//
// Minutes, not hours: the link is a bearer credential for a document that may be
// someone's identity card.
const DownloadTTL = 10 * time.Minute

// OrphanAge is how long an unattached upload survives.
//
// People abandon half-filled forms constantly; without a sweeper their
// attachments accumulate forever, and personal data with no retention clock
// attached to it is the worst kind to be holding.
const OrphanAge = 24 * time.Hour

// Service handles attachments.
type Service struct {
	store   *store.Store
	objects storage.Storage
	env     *crypto.Envelope
	log     *slog.Logger
}

// NewService returns a Service.
func NewService(s *store.Store, objects storage.Storage, env *crypto.Envelope, log *slog.Logger) *Service {
	return &Service{store: s, objects: objects, env: env, log: log}
}

// UploadInput describes an incoming attachment.
type UploadInput struct {
	TenantID      uuid.UUID
	ProjectID     uuid.UUID
	FormVersionID uuid.UUID
	FieldID       string
	Filename      string
	// MaxMB is the question's own limit, on top of the system ceiling.
	MaxMB int
	// Accept is the question's list of permitted content types.
	Accept []string
}

// Uploaded is the result handed back to the client.
type Uploaded struct {
	ID          uuid.UUID `json:"file_id"`
	Name        string    `json:"filename"`
	ContentType string    `json:"content_type"`
	Size        int64     `json:"size_bytes"`
	Checksum    string    `json:"checksum"`
}

// Upload stores one attachment.
//
// The bytes travel through the application rather than straight to object
// storage. A presigned upload would save bandwidth, but it would also mean the
// server never sees the file -- and therefore cannot check what it actually is,
// nor encrypt it. At the volumes this system targets that trade is not worth
// making; see docs/06-deep-dives.md.
func (s *Service) Upload(ctx context.Context, in UploadInput, r io.Reader) (Uploaded, error) {
	// Read the head first: the type is decided before anything is written, so a
	// rejected file never reaches the disk at all.
	head := make([]byte, domain.SniffLen)
	n, err := io.ReadFull(r, head)
	switch {
	case errors.Is(err, io.EOF):
		return Uploaded{}, domain.ErrEmpty
	case errors.Is(err, io.ErrUnexpectedEOF):
		head = head[:n]
	case err != nil:
		return Uploaded{}, fmt.Errorf("reading upload: %w", err)
	}

	contentType, extension, err := domain.Detect(head)
	if err != nil {
		return Uploaded{}, err
	}
	if !domain.Accepts(in.Accept, contentType) {
		return Uploaded{}, fmt.Errorf("%w: %s", domain.ErrTypeNotAllowed, contentType)
	}

	dek, err := crypto.NewDataKey()
	if err != nil {
		return Uploaded{}, err
	}
	wrapped, err := s.env.Wrap(dek)
	if err != nil {
		return Uploaded{}, err
	}

	fileID := uuid.New()
	key := storageKey(in.TenantID, fileID)

	// Hard-capped before encryption. A client that lies about Content-Length
	// cannot make the process read more than the ceiling allows.
	limit := int64(domain.MaxUploadBytes)
	if in.MaxMB > 0 && int64(in.MaxMB)<<20 < limit {
		limit = int64(in.MaxMB) << 20
	}
	body := io.LimitReader(io.MultiReader(bytes.NewReader(head), r), limit+1)

	plaintext, err := io.ReadAll(body)
	if err != nil {
		return Uploaded{}, fmt.Errorf("reading upload: %w", err)
	}
	if int64(len(plaintext)) > limit {
		return Uploaded{}, fmt.Errorf("%w: this question allows up to %d MB", domain.ErrTooLarge, limit>>20)
	}
	if err := domain.CheckSize(int64(len(plaintext)), in.MaxMB); err != nil {
		return Uploaded{}, err
	}

	// The checksum covers the plaintext, so it still means something after the
	// file has been encrypted and, later, after it has been decrypted again.
	sum := sha256.Sum256(plaintext)

	// The file id is bound into the ciphertext: a blob cannot be swapped between
	// records even by someone with write access to the storage volume.
	sealed, err := crypto.SealWith(dek, plaintext, []byte(fileID.String()))
	if err != nil {
		return Uploaded{}, err
	}

	if _, err := s.objects.Put(ctx, key, bytes.NewReader(sealed)); err != nil {
		return Uploaded{}, fmt.Errorf("storing upload: %w", err)
	}

	f := store.File{
		ID: fileID, TenantID: in.TenantID, ProjectID: in.ProjectID,
		FormVersionID: in.FormVersionID, FieldID: in.FieldID,
		StorageKey: key, OriginalName: domain.SafeFilename(in.Filename, extension),
		ContentType: contentType, SizeBytes: int64(len(plaintext)),
		Checksum: sum[:], DEKWrapped: wrapped, Status: domain.StatusPending,
	}
	if err := s.store.Insert(ctx, f); err != nil {
		// The row is what makes the object findable; without it the bytes are
		// unreachable garbage, so clean up rather than leave them behind.
		if delErr := s.objects.Delete(context.WithoutCancel(ctx), key); delErr != nil {
			s.log.Error("removing orphaned object after failed insert", "error", delErr, "key", key)
		}
		return Uploaded{}, err
	}

	return Uploaded{
		ID: f.ID, Name: f.OriginalName, ContentType: f.ContentType,
		Size: f.SizeBytes, Checksum: fmt.Sprintf("sha256:%x", sum),
	}, nil
}

// Open decrypts and returns an attachment's contents.
func (s *Service) Open(ctx context.Context, fileID uuid.UUID) (store.File, []byte, error) {
	f, err := s.store.ResolvePublic(ctx, fileID)
	if err != nil {
		return store.File{}, nil, err
	}

	dek, err := s.env.Unwrap(f.DEKWrapped)
	if errors.Is(err, crypto.ErrShredded) {
		// The key was destroyed by an erasure. The bytes may still exist; they
		// are permanently unreadable, which is the intended outcome.
		return store.File{}, nil, domain.ErrNotFound
	}
	if err != nil {
		return store.File{}, nil, fmt.Errorf("unwrapping file key: %w", err)
	}

	rc, err := s.objects.Get(ctx, f.StorageKey)
	if errors.Is(err, storage.ErrNotFound) {
		return store.File{}, nil, domain.ErrNotFound
	}
	if err != nil {
		return store.File{}, nil, fmt.Errorf("reading object: %w", err)
	}
	defer func() { _ = rc.Close() }()

	sealed, err := io.ReadAll(rc)
	if err != nil {
		return store.File{}, nil, fmt.Errorf("reading object: %w", err)
	}
	plaintext, err := crypto.OpenWith(dek, sealed, []byte(f.ID.String()))
	if err != nil {
		return store.File{}, nil, fmt.Errorf("decrypting object: %w", err)
	}
	return f, plaintext, nil
}

// Bind attaches uploads to a submission, inside the caller's transaction.
//
// It runs in the same transaction as the submission itself, so a response and
// its attachments become visible together or not at all.
func (s *Service) Bind(ctx context.Context, tx pgx.Tx, tenantID, submissionID uuid.UUID, fileIDs []uuid.UUID) error {
	return s.store.Bind(ctx, tx, tenantID, submissionID, fileIDs)
}

// Validate checks that a file exists, is unattached, and came from the field it
// is being submitted for.
//
// The last part matters: without it a caller could upload against a permissive
// question and then attach the result to a stricter one.
func (s *Service) Validate(ctx context.Context, tenantID uuid.UUID, fileID uuid.UUID, formVersionID uuid.UUID, fieldID string) error {
	f, err := s.store.ResolvePublic(ctx, fileID)
	if err != nil {
		return err
	}
	if f.TenantID != tenantID {
		return domain.ErrNotFound
	}
	if f.Status != domain.StatusPending || f.SubmissionID != nil {
		return domain.ErrAlreadyBound
	}
	if f.FormVersionID != formVersionID || f.FieldID != fieldID {
		return domain.ErrNotFound
	}
	return nil
}

// SweepOrphans deletes uploads that were never attached to a submission.
func (s *Service) SweepOrphans(ctx context.Context) (int, error) {
	orphans, err := s.store.ListOrphans(ctx, OrphanAge, 500)
	if err != nil {
		return 0, err
	}

	var removed int
	for _, o := range orphans {
		if err := s.objects.Delete(ctx, o.StorageKey); err != nil {
			s.log.Error("deleting orphaned object", "error", err, "file_id", o.ID)
			continue
		}
		if err := s.store.Delete(ctx, o.TenantID, o.ID); err != nil {
			s.log.Error("deleting orphaned file row", "error", err, "file_id", o.ID)
			continue
		}
		removed++
	}
	if removed > 0 {
		s.log.Info("swept abandoned uploads", "count", removed)
	}
	return removed, nil
}

// PurgeErased removes the objects of files whose keys have been destroyed.
//
// Destroying the key already makes the contents unreadable; this reclaims the
// space as well.
func (s *Service) PurgeErased(ctx context.Context) (int, error) {
	erased, err := s.store.ListErasedWithObjects(ctx, 500)
	if err != nil {
		return 0, err
	}

	var removed int
	for _, e := range erased {
		if err := s.objects.Delete(ctx, e.StorageKey); err != nil {
			s.log.Error("deleting erased object", "error", err, "file_id", e.ID)
			continue
		}
		if err := s.store.ClearStorageKey(ctx, e.TenantID, e.ID); err != nil {
			s.log.Error("clearing storage key", "error", err, "file_id", e.ID)
			continue
		}
		removed++
	}
	return removed, nil
}

// storageKey lays objects out by tenant and month, so a directory listing never
// grows without bound and a tenant's files stay together.
func storageKey(tenantID, fileID uuid.UUID) string {
	now := time.Now().UTC()
	return fmt.Sprintf("%s/%04d/%02d/%s", tenantID, now.Year(), int(now.Month()), ulid.Make())
}
