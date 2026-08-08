package main

import (
	"context"
	"encoding/hex"
	"errors"
	"html/template"
	"strings"

	"github.com/google/uuid"

	consentstore "github.com/collectr/collectr/internal/modules/consent/store"
	formsapp "github.com/collectr/collectr/internal/modules/forms/app"
	formsdomain "github.com/collectr/collectr/internal/modules/forms/domain"
	"github.com/collectr/collectr/internal/webpages"
)

// The adapters live here rather than inside webpages so that package keeps
// importing no module at all. It receives view structs and interfaces from the
// composition root, which is what lets the public pages read from forms and
// consent without those two becoming its dependencies.

type formPages struct{ svc *formsapp.Service }

func (a formPages) PublicFormPage(ctx context.Context, publicID string) (webpages.FormPage, error) {
	pf, err := a.svc.Public(ctx, publicID)
	switch {
	case errors.Is(err, formsdomain.ErrFormNotFound):
		return webpages.FormPage{}, webpages.ErrNotFound
	case err != nil:
		return webpages.FormPage{}, err
	}

	page := webpages.FormPage{PublicID: publicID, Title: pf.Form.Title}
	for _, p := range pf.Schema.Consent.Purposes {
		page.Consent.Purposes = append(page.Consent.Purposes, webpages.Purpose{
			// Label falls back to the code because domain.Purpose carries no
			// display name yet. A code is poor copy but it is honest; inventing
			// a friendly label here would put words in the controller's mouth on
			// the one screen where the wording is the legal act.
			Code: p.Code, Label: p.Code, Required: p.Required,
		})
	}

	// Law 91/2025 requires telling a subject, before they answer, that sensitive
	// personal data is being collected. Validate refuses to publish a schema with
	// a sensitive question and no such notice -- and this line is what makes that
	// promise true. Without it the check was enforced at publish and then dropped
	// at render: the operator was made to declare a notice nobody was ever shown.
	//
	// Built from the questions rather than from a free-text field, so it cannot
	// drift away from what the form actually asks.
	if pf.Schema.Consent.SensitiveNoticeRequired {
		page.SensitiveNotice = sensitiveKinds(pf.Schema)
	}
	return page, nil
}

// sensitiveKinds lists what a form's sensitive questions collect, for the notice
// on the public page.
//
// The declared pii kind is preferred over the question's label: "health" says
// what category of data it is, which is what the notice has to convey, while a
// label reads as the question rather than the category. Labels are the fallback,
// because a notice naming something imprecise beats a notice naming nothing.
func sensitiveKinds(s formsdomain.Schema) string {
	// Walked page by page rather than over the Fields map: map iteration order is
	// random in Go, and a notice whose wording reshuffles on every request reads
	// as a different notice each time it is compared.
	seen := map[string]bool{}
	var kinds []string
	for _, page := range s.Pages {
		for _, id := range page.Fields {
			f, ok := s.Fields[id]
			if !ok || !f.Sensitive {
				continue
			}
			kind := f.PII
			if kind == "" {
				kind = f.Label
			}
			if kind == "" || seen[kind] {
				continue
			}
			seen[kind] = true
			kinds = append(kinds, kind)
		}
	}
	return strings.Join(kinds, ", ")
}

type consentDocs struct{ store *consentstore.Store }

func (a consentDocs) ConsentDocument(ctx context.Context, id string) (webpages.Document, error) {
	docID, err := uuid.Parse(id)
	if err != nil {
		return webpages.Document{}, webpages.ErrNotFound
	}
	doc, err := a.store.PublicDocument(ctx, docID)
	if err != nil {
		return webpages.Document{}, webpages.ErrNotFound
	}
	return webpages.Document{
		ID:       doc.ID.String(),
		Kind:     doc.Kind,
		Version:  doc.VersionNo,
		Hash:     hex.EncodeToString(doc.Hash),
		BodyHTML: template.HTML(doc.BodyHTML), //nolint:gosec // see webpages.Document
	}, nil
}
