package crypto

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func testKEK(t *testing.T) []byte {
	t.Helper()
	return []byte(strings.Repeat("k", KeySize))
}

func TestNewEnvelopeRejectsWrongKeySize(t *testing.T) {
	t.Parallel()

	for _, size := range []int{0, 1, 16, 31, 33} {
		if _, err := NewEnvelope(bytes.Repeat([]byte("k"), size)); err == nil {
			t.Errorf("NewEnvelope with a %d-byte key = nil error, want error", size)
		}
	}
}

func TestWrapUnwrapRoundTrip(t *testing.T) {
	t.Parallel()

	env, err := NewEnvelope(testKEK(t))
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}
	dek, err := NewDataKey()
	if err != nil {
		t.Fatalf("NewDataKey: %v", err)
	}

	wrapped, err := env.Wrap(dek)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if bytes.Contains(wrapped, dek) {
		t.Fatal("the wrapped blob contains the plaintext data key")
	}

	got, err := env.Unwrap(wrapped)
	if err != nil {
		t.Fatalf("Unwrap: %v", err)
	}
	if !bytes.Equal(got, dek) {
		t.Error("unwrapped key does not match the original")
	}
}

func TestUnwrapRejects(t *testing.T) {
	t.Parallel()

	env, err := NewEnvelope(testKEK(t))
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}
	dek, _ := NewDataKey()
	wrapped, _ := env.Wrap(dek)

	t.Run("a destroyed key reads as shredded, not as an error", func(t *testing.T) {
		t.Parallel()
		// This is the state after erasure: the row is there, the key is gone.
		if _, err := env.Unwrap(nil); !errors.Is(err, ErrShredded) {
			t.Errorf("Unwrap(nil) = %v, want ErrShredded", err)
		}
	})

	t.Run("tampered ciphertext", func(t *testing.T) {
		t.Parallel()
		bad := bytes.Clone(wrapped)
		bad[len(bad)-1] ^= 0xff
		if _, err := env.Unwrap(bad); !errors.Is(err, ErrUnwrap) {
			t.Errorf("Unwrap(tampered) = %v, want ErrUnwrap", err)
		}
	})

	t.Run("another deployment's KEK", func(t *testing.T) {
		t.Parallel()
		other, err := NewEnvelope(bytes.Repeat([]byte("z"), KeySize))
		if err != nil {
			t.Fatalf("NewEnvelope: %v", err)
		}
		if _, err := other.Unwrap(wrapped); !errors.Is(err, ErrUnwrap) {
			t.Errorf("Unwrap with a foreign KEK = %v, want ErrUnwrap", err)
		}
	})
}

func TestSealOpenRoundTrip(t *testing.T) {
	t.Parallel()

	dek, _ := NewDataKey()
	plaintext := []byte("tình trạng sức khoẻ: bình thường")
	aad := []byte("tenant:1|submission:2")

	blob, err := SealWith(dek, plaintext, aad)
	if err != nil {
		t.Fatalf("SealWith: %v", err)
	}
	if bytes.Contains(blob, plaintext) {
		t.Fatal("ciphertext contains the plaintext")
	}

	got, err := OpenWith(dek, blob, aad)
	if err != nil {
		t.Fatalf("OpenWith: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Errorf("OpenWith = %q, want %q", got, plaintext)
	}
}

// TestOpenRejectsWrongContext pins the property that stops a ciphertext being
// moved between records: without the same context, it will not open.
func TestOpenRejectsWrongContext(t *testing.T) {
	t.Parallel()

	dek, _ := NewDataKey()
	blob, err := SealWith(dek, []byte("secret"), []byte("tenant:1|submission:2"))
	if err != nil {
		t.Fatalf("SealWith: %v", err)
	}
	if _, err := OpenWith(dek, blob, []byte("tenant:1|submission:999")); err == nil {
		t.Error("OpenWith accepted a blob under a different submission context")
	}
}

// TestShreddingIsIrreversible is the erasure guarantee, stated as a test: once
// the key is gone, the data cannot be recovered by any means the system has.
func TestShreddingIsIrreversible(t *testing.T) {
	t.Parallel()

	env, _ := NewEnvelope(testKEK(t))
	dek, _ := NewDataKey()
	wrapped, _ := env.Wrap(dek)
	blob, _ := SealWith(dek, []byte("sensitive answer"), nil)

	// The key works first. Without this the test asserts that a nil key fails to
	// unwrap, which would pass against an implementation that never worked at
	// all -- it proves erasure only if there was something to erase.
	recovered, err := env.Unwrap(wrapped)
	if err != nil {
		t.Fatalf("Unwrap before shredding: %v", err)
	}
	if plain, err := OpenWith(recovered, blob, nil); err != nil || string(plain) != "sensitive answer" {
		t.Fatalf("OpenWith before shredding = %q, %v", plain, err)
	}

	// Erasure: the wrapped key is deleted. The ciphertext survives wherever it
	// was copied to -- which is the point, and also the limit: a backup taken
	// before this moment still holds the wrapped key beside it.
	wrapped = nil
	if _, err := env.Unwrap(wrapped); !errors.Is(err, ErrShredded) {
		t.Fatalf("Unwrap after shredding = %v, want ErrShredded", err)
	}

	// A different key must not open it either.
	otherDEK, _ := NewDataKey()
	if _, err := OpenWith(otherDEK, blob, nil); err == nil {
		t.Error("ciphertext opened with an unrelated key")
	}
}

func TestSealWithRejectsWrongKeySize(t *testing.T) {
	t.Parallel()

	if _, err := SealWith([]byte("short"), []byte("x"), nil); err == nil {
		t.Error("SealWith accepted an undersized key")
	}
}

func TestOpenWithRejectsTruncatedBlob(t *testing.T) {
	t.Parallel()

	dek, _ := NewDataKey()
	if _, err := OpenWith(dek, []byte{1, 2, 3}, nil); err == nil {
		t.Error("OpenWith accepted a blob shorter than the nonce")
	}
}
