// Package domain holds the rules a consent document must satisfy.
package domain

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ErrUnsafeBody means the document contains markup that does something other
// than present text.
var ErrUnsafeBody = errors.New("consent body contains unsafe markup")

// forbidden is matched against the document a tenant submits.
//
// Rejected, never rewritten. The stored bytes are hashed, and that hash is what
// a consent record points at as evidence of what a person was shown; silently
// editing the text would make the evidence describe something nobody saw.
//
// A content security policy cannot cover this. <meta http-equiv="refresh"> is
// navigation rather than a subresource, and no CSP directive stops it -- the one
// that would have, navigate-to, was dropped from the specification. A permalink
// served as immutable legal evidence that quietly forwards its reader somewhere
// else is the whole attack, and it needs no script at all.
var forbidden = []struct {
	name    string
	pattern *regexp.Regexp
}{
	{"thẻ <script>", regexp.MustCompile(`(?is)<\s*script\b`)},
	{"thẻ <meta> (có thể tự chuyển hướng trang)", regexp.MustCompile(`(?is)<\s*meta\b`)},
	{"thẻ <base> (đổi gốc của mọi liên kết)", regexp.MustCompile(`(?is)<\s*base\b`)},
	{"thẻ <form> (gửi dữ liệu ra ngoài)", regexp.MustCompile(`(?is)<\s*form\b`)},
	{"thẻ <iframe>", regexp.MustCompile(`(?is)<\s*iframe\b`)},
	{"thẻ <object>, <embed> hoặc <applet>", regexp.MustCompile(`(?is)<\s*(object|embed|applet)\b`)},
	{"thẻ <link> (nạp tài nguyên ngoài)", regexp.MustCompile(`(?is)<\s*link\b`)},
	{"thuộc tính sự kiện on…=", regexp.MustCompile(`(?is)<[^>]*\son[a-z]+\s*=`)},
	{"liên kết javascript:", regexp.MustCompile(`(?is)javascript\s*:`)},
	{"nguồn data:", regexp.MustCompile(`(?is)=\s*["']?\s*data\s*:`)},
	{"@import trong CSS", regexp.MustCompile(`(?is)@import`)},
}

// ValidateBodyHTML rejects a consent document that can do more than be read.
//
// The document is authored by a tenant administrator, which is a trusted role
// for writing text and not a trusted role for writing behaviour: the same
// capability is held by the DPO, whose role is described in roles.go as
// "oversight, not operation".
func ValidateBodyHTML(body string) error {
	if strings.TrimSpace(body) == "" {
		return fmt.Errorf("%w: nội dung trống", ErrUnsafeBody)
	}
	for _, f := range forbidden {
		if f.pattern.MatchString(body) {
			return fmt.Errorf("%w: không cho phép %s trong văn bản đồng ý",
				ErrUnsafeBody, f.name)
		}
	}
	return nil
}
