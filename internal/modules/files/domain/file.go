// Package domain decides what a file is and whether it may be accepted.
//
// Nothing here trusts the client. The declared content type and the filename
// extension are both attacker-controlled; only the bytes are evidence.
package domain

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
)

// Errors returned when a file is refused.
var (
	ErrTooLarge       = errors.New("file is too large")
	ErrEmpty          = errors.New("file is empty")
	ErrTypeNotAllowed = errors.New("file type is not accepted for this question")
	ErrTypeMismatch   = errors.New("file contents do not match its declared type")
	ErrNotFound       = errors.New("file not found")
	ErrAlreadyBound   = errors.New("file is already attached to a submission")
)

// File statuses.
const (
	// StatusPending is an uploaded file not yet attached to a submission.
	StatusPending = "pending"
	// StatusBound is attached and subject to the submission's retention.
	StatusBound = "bound"
	// StatusErased means the object is gone and the key destroyed.
	StatusErased = "erased"
)

// MaxUploadBytes is the ceiling regardless of what a question allows.
const MaxUploadBytes = 25 << 20

// sniffLen is how much of the file the detector needs.
const sniffLen = 512

// signature is one recognised file type.
type signature struct {
	contentType string
	extension   string
	// match reports whether the leading bytes belong to this type.
	match func(head []byte) bool
}

// signatures are the types this system will accept.
//
// A deliberately short list. Every additional format is another parser somewhere
// downstream -- in a virus scanner, in a preview generator, in whatever opens the
// file at the other end -- and formats that can carry active content (SVG, HTML,
// Office macros) are absent for that reason.
var signatures = []signature{
	{
		contentType: "application/pdf", extension: ".pdf",
		match: func(h []byte) bool { return bytes.HasPrefix(h, []byte("%PDF-")) },
	},
	{
		contentType: "image/png", extension: ".png",
		match: func(h []byte) bool { return bytes.HasPrefix(h, []byte("\x89PNG\r\n\x1a\n")) },
	},
	{
		contentType: "image/jpeg", extension: ".jpg",
		match: func(h []byte) bool { return bytes.HasPrefix(h, []byte("\xff\xd8\xff")) },
	},
	{
		contentType: "image/webp", extension: ".webp",
		match: func(h []byte) bool {
			return len(h) >= 12 && bytes.HasPrefix(h, []byte("RIFF")) && bytes.Equal(h[8:12], []byte("WEBP"))
		},
	},
	{
		contentType: "image/heic", extension: ".heic",
		match: func(h []byte) bool {
			return len(h) >= 12 && bytes.Equal(h[4:8], []byte("ftyp")) &&
				(bytes.Equal(h[8:12], []byte("heic")) || bytes.Equal(h[8:12], []byte("heix")) ||
					bytes.Equal(h[8:12], []byte("mif1")))
		},
	},
}

// SniffLen is how many leading bytes Detect needs.
const SniffLen = sniffLen

// Detect identifies a file from its leading bytes.
//
// The declared type is never consulted. A .pdf that begins with "<script" is a
// script, whatever the upload form said about it.
func Detect(head []byte) (contentType, extension string, err error) {
	if len(head) == 0 {
		return "", "", ErrEmpty
	}
	for _, s := range signatures {
		if s.match(head) {
			return s.contentType, s.extension, nil
		}
	}
	return "", "", ErrTypeMismatch
}

// Accepts reports whether a question allows a detected type.
//
// An empty accept list means the question takes any type this system recognises,
// which is still a closed set -- never "anything at all".
func Accepts(allowed []string, contentType string) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, a := range allowed {
		if strings.EqualFold(strings.TrimSpace(a), contentType) {
			return true
		}
	}
	return false
}

// CheckSize validates a file's length against the question's limit.
func CheckSize(size int64, maxMB int) error {
	switch {
	case size <= 0:
		return ErrEmpty
	case size > MaxUploadBytes:
		return fmt.Errorf("%w: limit is %d MB", ErrTooLarge, MaxUploadBytes>>20)
	}
	if maxMB > 0 && size > int64(maxMB)<<20 {
		return fmt.Errorf("%w: this question allows up to %d MB", ErrTooLarge, maxMB)
	}
	return nil
}

// SafeFilename reduces a client-supplied name to something safe to store and to
// send back in a Content-Disposition header.
//
// Path separators, control characters and leading dots all go: the name travels
// into a header and, for whoever downloads it, onto a filesystem.
func SafeFilename(name, extension string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))

	var b strings.Builder
	for _, r := range name {
		switch {
		case unicode.IsControl(r), r == '"', r == '\'', r == '/', r == '\\', r == 0:
			// dropped
		default:
			b.WriteRune(r)
		}
	}
	// Runs of dots collapse to one. "0..0" is harmless on a filesystem, but a
	// name that never contains ".." is one fewer thing to reason about at every
	// later point it is passed along.
	clean := collapseDots(strings.TrimLeft(strings.TrimSpace(b.String()), "."))
	if len(clean) > 120 {
		clean = clean[:120]
	}
	if clean == "" {
		clean = "tep-dinh-kem"
	}

	// The extension comes from what the bytes actually are, so a file cannot
	// arrive claiming to be one thing and be saved as another.
	if extension != "" && !strings.EqualFold(filepath.Ext(clean), extension) {
		clean = collapseDots(strings.TrimSuffix(clean, filepath.Ext(clean))) + extension
	}
	return clean
}

// collapseDots reduces any run of dots to a single dot.
func collapseDots(s string) string {
	for strings.Contains(s, "..") {
		s = strings.ReplaceAll(s, "..", ".")
	}
	return strings.TrimSuffix(s, ".")
}
