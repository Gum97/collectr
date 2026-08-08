package webpages

import (
	"context"
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

type stubForms struct {
	page FormPage
	err  error
}

func (s stubForms) PublicFormPage(context.Context, string) (FormPage, error) {
	return s.page, s.err
}

type stubDocs struct {
	doc Document
	err error
}

func (s stubDocs) ConsentDocument(context.Context, string) (Document, error) {
	return s.doc, s.err
}

func newHandler(t *testing.T, cfg Config) (*Handler, *http.ServeMux) {
	t.Helper()
	h := New(cfg)
	mux := http.NewServeMux()
	h.Register(mux)
	return h, mux
}

func get(t *testing.T, mux *http.ServeMux, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

// The form page has to be usable when the module never arrives. That is not a
// nicety: it is the state every visitor is in for the first few hundred
// milliseconds, and the state some of them stay in.
func TestFormPageReadableWithoutScript(t *testing.T) {
	_, mux := newHandler(t, Config{
		Forms: stubForms{page: FormPage{
			Title:       "Đăng ký dùng thử Tết",
			Description: "Điền giúp chúng tôi vài thông tin.",
			Controller:  Controller{Name: "Công ty Acme", TaxCode: "0123456789"},
			Consent: ConsentNotice{
				Version: 2,
				Hash:    "9a1f2b3c4d5e6f708192a3b4c5d6e7f8",
				URL:     "/consent/9f2ac1",
				Purposes: []Purpose{
					{Label: "Xử lý yêu cầu dùng thử", Required: true, Retention: "Lưu 24 tháng"},
					{Label: "Gửi thông tin khuyến mại", Retention: "Lưu 12 tháng"},
				},
			},
			SensitiveNotice: "tình trạng sức khoẻ",
		}},
	})

	body := get(t, mux, "/f/abc123").Body.String()

	for _, want := range []string{
		"Đăng ký dùng thử Tết",
		"Điền giúp chúng tôi vài thông tin.",
		"Công ty Acme",
		"Cần bật JavaScript",
		"Xử lý yêu cầu dùng thử",
		"Gửi thông tin khuyến mại",
		`href="/consent/9f2ac1"`,
		"v2",
		"dữ liệu cá nhân nhạy cảm: tình trạng sức khoẻ",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("form page is missing %q with scripting off", want)
		}
	}

	// Nothing may be pre-ticked, ever. A box that arrives already on is not
	// consent that can be evidenced later, and this page is the evidence.
	if strings.Contains(body, "checked") {
		t.Error("a consent checkbox is pre-ticked")
	}
}

// The fallback must sit inside the container form.ts empties, or a visitor whose
// script does load ends up looking at two consent blocks.
func TestConsentFallbackIsInsideTheModuleContainer(t *testing.T) {
	_, mux := newHandler(t, Config{
		Forms: stubForms{page: FormPage{
			Title:   "F",
			Consent: ConsentNotice{Version: 1, URL: "/consent/x", Purposes: []Purpose{{Label: "P"}}},
		}},
	})

	body := get(t, mux, "/f/abc123").Body.String()
	container := strings.Index(body, `id="collectr-form"`)
	// The container is closed immediately before the footer, which is the first
	// thing rendered after it.
	end := strings.Index(body, `class="foot"`)
	if container < 0 || end < 0 {
		t.Fatal("form page is missing the module container or the footer")
	}
	// Searched from the container, not from the start of the document: the class
	// name also appears in the inlined stylesheet.
	if !strings.Contains(body[container:end], `class="cf-consent"`) {
		t.Error("the static consent block is outside #collectr-form; form.ts will not replace it")
	}
}

func TestFormPageDegradesWithoutASource(t *testing.T) {
	_, mux := newHandler(t, Config{})
	w := get(t, mux, "/f/abc123")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), `data-public-id="abc123"`) {
		t.Error("the module container lost its public id")
	}
}

func TestDeadLinkStates(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		status  int
		wants   []string
		forbids []string
	}{
		{
			name:   "never existed",
			err:    ErrNotFound,
			status: http.StatusNotFound,
			wants:  []string{"Không tìm thấy", "chưa từng tồn tại"},
		},
		{
			name:   "closed",
			err:    ErrGone,
			status: http.StatusGone,
			wants:  []string{"đã đóng", "từng hoạt động"},
		},
		{
			// The one that matters. A page that hints somebody's data was here
			// discloses that person to whoever holds the link.
			name:    "taken down on request",
			err:     ErrWithdrawn,
			status:  http.StatusGone,
			wants:   []string{"đã được gỡ"},
			forbids: []string{"dữ liệu", "xóa", "chủ thể", "yêu cầu của"},
		},
		{
			name:   "legal hold",
			err:    ErrLegalHold,
			status: http.StatusUnavailableForLegalReasons,
			wants:  []string{"pháp lý", "có chủ ý"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, mux := newHandler(t, Config{Forms: stubForms{err: tc.err}})
			w := get(t, mux, "/f/abc123")
			if w.Code != tc.status {
				t.Errorf("status = %d, want %d", w.Code, tc.status)
			}
			if got := w.Header().Get("Cache-Control"); got != noStore {
				t.Errorf("Cache-Control = %q, want %q: a dead link must be dead on the next scan", got, noStore)
			}
			body := w.Body.String()
			for _, want := range tc.wants {
				if !strings.Contains(body, want) {
					t.Errorf("page is missing %q", want)
				}
			}
			for _, forbidden := range tc.forbids {
				if strings.Contains(body, forbidden) {
					t.Errorf("page mentions %q; it must not suggest anyone's data was ever here", forbidden)
				}
			}
		})
	}
}

func TestConsentPermalinkIsImmutableAndScriptless(t *testing.T) {
	published := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	_, mux := newHandler(t, Config{Documents: stubDocs{doc: Document{
		ID:          "9f2ac1",
		Title:       "Điều khoản xử lý dữ liệu cá nhân",
		Version:     2,
		PublishedAt: published,
		Hash:        "4f2a9d10aabbccddeeff00112233445566778899aabbccddeeff001122c4e7",
		BodyHTML:    template.HTML(`<p>Nội dung đầy đủ.</p>`),
		Purposes:    []string{"Liên hệ về đơn hàng", "Gửi thông tin khuyến mại"},
	}}})

	w := get(t, mux, "/consent/9f2ac1")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := w.Header().Get("Cache-Control"); got != immutable {
		t.Errorf("Cache-Control = %q, want %q", got, immutable)
	}
	if csp := w.Header().Get("Content-Security-Policy"); strings.Contains(csp, "script-src") {
		t.Errorf("CSP = %q: the permalink must permit no script at all", csp)
	}

	body := w.Body.String()
	if strings.Contains(body, "<script") {
		t.Error("the permalink emitted a script tag")
	}
	for _, want := range []string{
		"Bản v2",
		"01/07/2026",
		// Never truncated: this is what gets compared against a consent record.
		"4f2a9d10aabbccddeeff00112233445566778899aabbccddeeff001122c4e7",
		"Nội dung đầy đủ.",
		"Liên hệ về đơn hàng",
		"không sửa được sau khi công bố",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("permalink is missing %q", want)
		}
	}
	if !strings.Contains(body, "@media print") {
		t.Error("permalink has no print rules; it is the copy people hand to a lawyer")
	}
}

// Erasure is the one action on the portal that cannot be undone, and the page
// has to say so before any script has run.
func TestPortalNamesTheConsequences(t *testing.T) {
	_, mux := newHandler(t, Config{
		Brand:          "Acme",
		Support:        "privacy@acme.vn",
		ResponseWindow: 30 * 24 * time.Hour,
	})

	for _, path := range []string{"/dsr", "/dsr/"} {
		w := get(t, mux, path)
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, want 200 (the magic link points at the bare path)", path, w.Code)
		}
		body := w.Body.String()
		for _, want := range []string{
			"Dữ liệu của bạn tại Acme",
			"không đảo ngược",
			"khóa mã hóa riêng của bạn bị hủy",
			"bản sao lưu",
			"không làm cho việc xử lý trước đó thành trái luật",
			"30 ngày",
			"privacy@acme.vn",
			`id="collectr-portal"`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("GET %s is missing %q", path, want)
			}
		}
		if got := w.Header().Get("Cache-Control"); got != noStore {
			t.Errorf("Cache-Control = %q, want %q on a shared phone", got, noStore)
		}
	}
}

// Every page ships the same headers, whether or not a reverse proxy is in front.
func TestSecurityHeaders(t *testing.T) {
	_, mux := newHandler(t, Config{Documents: stubDocs{doc: Document{Version: 1}}})

	for _, path := range []string{"/f/abc", "/dsr", "/consent/x"} {
		w := get(t, mux, path)
		for header, want := range map[string]string{
			"X-Content-Type-Options": "nosniff",
			"X-Frame-Options":        "DENY",
			"Referrer-Policy":        "no-referrer",
			"Content-Type":           "text/html; charset=utf-8",
		} {
			if got := w.Header().Get(header); got != want {
				t.Errorf("GET %s: %s = %q, want %q", path, header, got, want)
			}
		}
		if w.Header().Get("Content-Security-Policy") == "" {
			t.Errorf("GET %s: no Content-Security-Policy", path)
		}
	}
}

// The public module's name carries a build hash, so the page has to find it
// rather than spell it out.
func TestScriptResolution(t *testing.T) {
	assets := fstest.MapFS{
		"assets/form-bhY4_u0n.js":  {},
		"assets/admin-B5hwPHvM.js": {},
	}
	if got := script(assets, "form"); got != "/assets/form-bhY4_u0n.js" {
		t.Errorf("script() = %q, want the hashed form entry", got)
	}
	if got := script(assets, "portal"); got != "" {
		t.Errorf("script() = %q, want empty for an entry that was never built", got)
	}
	if got := script(nil, "form"); got != "" {
		t.Errorf("script(nil) = %q, want empty", got)
	}
}

func TestSqueezeKeepsTheStylesheetIntact(t *testing.T) {
	css, err := files.ReadFile("assets/public.css")
	if err != nil {
		t.Fatal(err)
	}
	out := squeeze(string(css))

	if strings.Contains(out, "/*") || strings.Contains(out, "*/") {
		t.Error("a comment survived")
	}
	if strings.Contains(out, "\n") {
		t.Error("a newline survived")
	}
	for _, want := range []string{
		// A quoted string inside a selector must come through untouched.
		`a[href^="http"]::after`,
		`content: " (" attr(href) ")"`,
		`.cf-star[aria-pressed="true"]`,
		"@media print",
		"--sans: system-ui",
		".cf-submit {",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("squeeze() lost %q", want)
		}
	}
	// Every class form.ts builds has to be styled by this file, or the module
	// renders unstyled controls on a page nobody can debug from.
	for _, class := range []string{
		".cf", ".cf-field", ".cf-label", ".cf-req", ".cf-help", ".cf-input",
		".cf-options", ".cf-option", ".cf-rating", ".cf-star", ".cf-consent",
		".cf-doc", ".cf-status", ".cf-submit",
	} {
		if !strings.Contains(out, class+" ") && !strings.Contains(out, class+":") &&
			!strings.Contains(out, class+"[") && !strings.Contains(out, class+",") {
			t.Errorf("no rule for %s, which form.ts renders", class)
		}
	}
}

func TestShortHash(t *testing.T) {
	if got := short("sha256:9a1f2b3c4d5e6f70"); got != "9a1f2b3c…" {
		t.Errorf("short() = %q", got)
	}
	if got := short("abc"); got != "abc" {
		t.Errorf("short() = %q, want the value unchanged when it is already short", got)
	}
}
