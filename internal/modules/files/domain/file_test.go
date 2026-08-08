package domain

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestDetect(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		head     []byte
		wantType string
		wantErr  error
	}{
		{name: "pdf", head: []byte("%PDF-1.7\n%âãÏÓ"), wantType: "application/pdf"},
		{name: "png", head: []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), wantType: "image/png"},
		{name: "jpeg", head: []byte("\xff\xd8\xff\xe0\x00\x10JFIF"), wantType: "image/jpeg"},
		{name: "webp", head: []byte("RIFF\x24\x00\x00\x00WEBPVP8 "), wantType: "image/webp"},
		{name: "heic", head: []byte("\x00\x00\x00\x18ftypheic\x00\x00\x00\x00"), wantType: "image/heic"},

		{name: "empty", head: nil, wantErr: ErrEmpty},
		{name: "plain text", head: []byte("just some text"), wantErr: ErrTypeMismatch},
		{name: "html", head: []byte("<!doctype html><script>"), wantErr: ErrTypeMismatch},
		{name: "svg carries script and is not accepted", head: []byte(`<svg xmlns="http://www.w3.org/2000/svg">`), wantErr: ErrTypeMismatch},
		{name: "zip or office document", head: []byte("PK\x03\x04\x14\x00"), wantErr: ErrTypeMismatch},
		{name: "elf binary", head: []byte("\x7fELF\x02\x01\x01"), wantErr: ErrTypeMismatch},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			gotType, ext, err := Detect(tc.head)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("Detect = (%q, %v), want %v", gotType, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Detect: %v", err)
			}
			if gotType != tc.wantType {
				t.Errorf("Detect = %q, want %q", gotType, tc.wantType)
			}
			if ext == "" {
				t.Error("Detect returned no extension")
			}
		})
	}
}

// TestDetectIgnoresDeclaredType is the property the whole package exists for: a
// file is what its bytes say, not what the upload form claimed.
func TestDetectIgnoresDeclaredType(t *testing.T) {
	t.Parallel()

	// A shell script uploaded as "cv.pdf" with Content-Type: application/pdf.
	disguised := []byte("#!/bin/sh\nrm -rf /\n")
	if _, _, err := Detect(disguised); !errors.Is(err, ErrTypeMismatch) {
		t.Fatalf("Detect accepted a disguised script: %v", err)
	}
}

func TestAccepts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		allowed  []string
		detected string
		want     bool
	}{
		{name: "explicitly allowed", allowed: []string{"application/pdf"}, detected: "application/pdf", want: true},
		{name: "case insensitive", allowed: []string{"Application/PDF"}, detected: "application/pdf", want: true},
		{name: "surrounding spaces", allowed: []string{" application/pdf "}, detected: "application/pdf", want: true},
		{name: "not in the list", allowed: []string{"application/pdf"}, detected: "image/png"},
		{name: "empty list allows any recognised type", allowed: nil, detected: "image/png", want: true},
		{name: "one of several", allowed: []string{"image/png", "image/jpeg"}, detected: "image/jpeg", want: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := Accepts(tc.allowed, tc.detected); got != tc.want {
				t.Errorf("Accepts = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCheckSize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		size  int64
		maxMB int
		want  error
	}{
		{name: "within the question limit", size: 1 << 20, maxMB: 10},
		{name: "exactly at the limit", size: 10 << 20, maxMB: 10},
		{name: "over the question limit", size: 11 << 20, maxMB: 10, want: ErrTooLarge},
		{name: "over the absolute ceiling", size: MaxUploadBytes + 1, maxMB: 0, want: ErrTooLarge},
		{name: "zero bytes", size: 0, maxMB: 10, want: ErrEmpty},
		{name: "negative", size: -1, maxMB: 10, want: ErrEmpty},
		{name: "no question limit falls back to the ceiling", size: 20 << 20, maxMB: 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := CheckSize(tc.size, tc.maxMB)
			if tc.want == nil && err != nil {
				t.Errorf("CheckSize = %v, want nil", err)
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Errorf("CheckSize = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestSafeFilename(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     string
		extension string
		want      string
	}{
		{name: "ordinary", input: "cv.pdf", extension: ".pdf", want: "cv.pdf"},
		{name: "vietnamese is preserved", input: "hồ sơ.pdf", extension: ".pdf", want: "hồ sơ.pdf"},
		{name: "path traversal", input: "../../etc/passwd", extension: ".pdf", want: "passwd.pdf"},
		{name: "windows path", input: `C:\Users\a\cv.pdf`, extension: ".pdf", want: "cv.pdf"},
		// The quotes go, and the ".sh" is replaced rather than appended: what the
		// bytes are decides the extension, so a disguised script cannot keep it.
		{name: "header injection", input: "a\"; filename=\"b.sh", extension: ".pdf", want: "a; filename=b.pdf"},
		{name: "newline", input: "cv\n.pdf", extension: ".pdf", want: "cv.pdf"},
		{name: "hidden file", input: "...secret", extension: ".pdf", want: "secret.pdf"},
		{name: "empty", input: "", extension: ".pdf", want: "tep-dinh-kem.pdf"},
		{name: "lying extension is corrected", input: "invoice.pdf", extension: ".png", want: "invoice.png"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := SafeFilename(tc.input, tc.extension)
			if got != tc.want {
				t.Errorf("SafeFilename(%q) = %q, want %q", tc.input, got, tc.want)
			}
			for _, bad := range []string{"/", "\\", "\"", "\n", "\r"} {
				if strings.Contains(got, bad) {
					t.Errorf("SafeFilename(%q) = %q still contains %q", tc.input, got, bad)
				}
			}
		})
	}
}

func TestSafeFilenameIsBounded(t *testing.T) {
	t.Parallel()

	got := SafeFilename(strings.Repeat("a", 500)+".pdf", ".pdf")
	if len(got) > 130 {
		t.Errorf("SafeFilename returned %d characters, want it bounded", len(got))
	}
}

func FuzzDetect(f *testing.F) {
	f.Add([]byte("%PDF-1.4"))
	f.Add([]byte("\xff\xd8\xff"))
	f.Add([]byte("RIFF"))
	f.Add([]byte(""))
	f.Add(bytes.Repeat([]byte{0}, 12))

	// Uploads come from the open internet: no input may panic the detector.
	f.Fuzz(func(t *testing.T, head []byte) {
		contentType, ext, err := Detect(head)
		if err == nil && (contentType == "" || ext == "") {
			t.Fatalf("Detect succeeded but returned type %q and extension %q", contentType, ext)
		}
	})
}

func FuzzSafeFilename(f *testing.F) {
	f.Add("cv.pdf")
	f.Add("../../etc/passwd")
	f.Add("")

	f.Fuzz(func(t *testing.T, name string) {
		got := SafeFilename(name, ".pdf")
		if got == "" {
			t.Fatal("SafeFilename returned an empty name")
		}
		for _, bad := range []string{"/", "\\", "..", "\n", "\r", "\""} {
			if strings.Contains(got, bad) {
				t.Fatalf("SafeFilename(%q) = %q contains %q", name, got, bad)
			}
		}
	})
}
