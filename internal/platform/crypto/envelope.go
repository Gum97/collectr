// Package crypto implements envelope encryption for sensitive personal data.
//
// Every data subject gets their own data key (DEK), which is stored only in
// wrapped form under the deployment's key-encryption key (KEK). Erasing a person
// destroys their DEK, and every copy of their sensitive data in the primary
// database becomes undecryptable at that moment.
//
// This is the only mechanism here that makes deletion true rather than a promise
// about the primary database. It is also why losing the KEK is unrecoverable:
// the same property that defeats a stale backup defeats an honest restore.
//
// A caveat that belongs next to the mechanism, not in a design document:
// destroying a subject's key makes their data unreadable in every backup taken
// AFTER that point. A backup taken before it holds both the ciphertext and the
// wrapped key, and the tenant key that unwraps it is not rotated, so restoring
// that backup restores the data. Telling a regulator or a data subject that an
// erasure is irreversible is only true once the pre-erasure backups have aged
// out of their retention window -- or once TENANT_KEK has been rotated.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
)

// KeySize is the DEK and KEK length in bytes (AES-256).
const KeySize = 32

// ErrUnwrap means a wrapped key could not be recovered: wrong KEK, or the
// ciphertext was altered.
var ErrUnwrap = errors.New("cannot unwrap data key")

// ErrShredded means the data subject's key has been destroyed, so their
// sensitive data is permanently unreadable. This is a successful erasure, not a
// fault.
var ErrShredded = errors.New("data key has been destroyed")

// Envelope wraps and unwraps per-subject data keys.
type Envelope struct {
	aead cipher.AEAD
}

// NewEnvelope returns an Envelope keyed by kek, which must be 32 bytes.
func NewEnvelope(kek []byte) (*Envelope, error) {
	if len(kek) != KeySize {
		return nil, fmt.Errorf("kek must be %d bytes, got %d", KeySize, len(kek))
	}
	block, err := aes.NewCipher(kek)
	if err != nil {
		return nil, fmt.Errorf("creating cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("creating gcm: %w", err)
	}
	return &Envelope{aead: aead}, nil
}

// NewDataKey returns a fresh random DEK.
func NewDataKey() ([]byte, error) {
	dek := make([]byte, KeySize)
	if _, err := io.ReadFull(rand.Reader, dek); err != nil {
		return nil, fmt.Errorf("generating data key: %w", err)
	}
	return dek, nil
}

// Wrap encrypts a DEK for storage.
func (e *Envelope) Wrap(dek []byte) ([]byte, error) {
	if len(dek) != KeySize {
		return nil, fmt.Errorf("dek must be %d bytes, got %d", KeySize, len(dek))
	}
	return e.seal(dek, nil)
}

// Unwrap recovers a DEK.
func (e *Envelope) Unwrap(wrapped []byte) ([]byte, error) {
	if len(wrapped) == 0 {
		return nil, ErrShredded
	}
	dek, err := e.open(wrapped, nil)
	if err != nil {
		return nil, ErrUnwrap
	}
	return dek, nil
}

// SealWith encrypts plaintext under a DEK.
//
// aad binds the ciphertext to its context (tenant and submission), so a blob
// cannot be lifted from one record and pasted into another: decryption fails
// unless the same context is supplied.
func SealWith(dek, plaintext, aad []byte) ([]byte, error) {
	aead, err := aeadFor(dek)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generating nonce: %w", err)
	}
	return aead.Seal(nonce, nonce, plaintext, aad), nil
}

// OpenWith decrypts a blob produced by SealWith.
func OpenWith(dek, blob, aad []byte) ([]byte, error) {
	aead, err := aeadFor(dek)
	if err != nil {
		return nil, err
	}
	if len(blob) < aead.NonceSize() {
		return nil, fmt.Errorf("ciphertext is shorter than the nonce")
	}
	nonce, body := blob[:aead.NonceSize()], blob[aead.NonceSize():]
	out, err := aead.Open(nil, nonce, body, aad)
	if err != nil {
		return nil, fmt.Errorf("decrypting: %w", err)
	}
	return out, nil
}

// SealBytes encrypts arbitrary data under the KEK.
//
// Wrap is for data keys and insists on exactly 32 bytes; this is for the other
// secrets a deployment holds -- TOTP seeds, SMTP passwords -- which have no
// reason to be key-sized.
func (e *Envelope) SealBytes(plaintext []byte) ([]byte, error) {
	return e.seal(plaintext, nil)
}

// OpenBytes decrypts data produced by SealBytes.
func (e *Envelope) OpenBytes(blob []byte) ([]byte, error) {
	if len(blob) == 0 {
		return nil, ErrShredded
	}
	out, err := e.open(blob, nil)
	if err != nil {
		return nil, ErrUnwrap
	}
	return out, nil
}

func (e *Envelope) seal(plaintext, aad []byte) ([]byte, error) {
	nonce := make([]byte, e.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generating nonce: %w", err)
	}
	return e.aead.Seal(nonce, nonce, plaintext, aad), nil
}

func (e *Envelope) open(blob, aad []byte) ([]byte, error) {
	if len(blob) < e.aead.NonceSize() {
		return nil, fmt.Errorf("ciphertext is shorter than the nonce")
	}
	nonce, body := blob[:e.aead.NonceSize()], blob[e.aead.NonceSize():]
	return e.aead.Open(nil, nonce, body, aad)
}

func aeadFor(dek []byte) (cipher.AEAD, error) {
	if len(dek) != KeySize {
		return nil, fmt.Errorf("dek must be %d bytes, got %d", KeySize, len(dek))
	}
	block, err := aes.NewCipher(dek)
	if err != nil {
		return nil, fmt.Errorf("creating cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("creating gcm: %w", err)
	}
	return aead, nil
}
