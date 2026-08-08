package webpages

import (
	"html/template"
	"time"
)

// FormPage is the server-rendered frame of /f/{public_id}.
//
// Everything here is what a respondent has to be able to read before deciding,
// which is why it is rendered by the server and not fetched: a consent notice
// that only appears once a script has run is a consent notice that sometimes
// does not appear.
type FormPage struct {
	PublicID    string
	Title       string
	Description string

	Controller Controller
	Consent    ConsentNotice

	// SensitiveNotice names the categories of sensitive personal data the form
	// collects, e.g. "tình trạng sức khoẻ". Empty when it collects none.
	//
	// Vietnam's PDPD requires this to be stated separately and before the fact,
	// not inferred from a field label further down the page.
	SensitiveNotice string
}

// Controller is the organisation collecting the data.
type Controller struct {
	Name    string
	TaxCode string
	Contact string
}

// ConsentNotice is the document the respondent is agreeing to, named by version.
//
// Version and Hash are shown rather than a bare "our privacy policy" link: the
// consent record stores which version was displayed, and a person who agreed
// must be able to reach that same version afterwards. A link to a page the
// controller can edit later proves nothing.
type ConsentNotice struct {
	DocumentID  string
	Version     int
	PublishedAt time.Time
	// Hash is the hex sha256 of the document body, with or without the "sha256:"
	// prefix.
	Hash string
	// URL is the permalink to the exact version, e.g. /consent/{id}.
	URL      string
	Purposes []Purpose
}

// Purpose is one thing the data will be used for, consented to on its own.
//
// One checkbox per purpose, never pre-ticked. A single blanket tick cannot be
// evidenced afterwards as consent to any particular use, and evidence is the
// entire job of the record this page produces.
type Purpose struct {
	Code        string
	Label       string
	Description string
	Required    bool
	// Retention is the plain-language storage period, e.g. "Lưu 24 tháng".
	Retention string
}

// Document is one immutable consent document version, rendered at
// /consent/{id}.
type Document struct {
	ID          string
	Title       string
	Kind        string
	Version     int
	PublishedAt time.Time
	// Hash is the hex sha256 of BodyHTML. Printed in full: this page is what
	// somebody hands to a lawyer, and a truncated digest verifies nothing.
	Hash string

	// BodyHTML is tenant-authored HTML, injected unescaped because it is a
	// formatted legal text. The page is served under a CSP with no script-src at
	// all; see docCSP.
	BodyHTML template.HTML

	// Purposes are the uses named in this version, listed separately so a reader
	// can see them without parsing the prose.
	Purposes []string
}
