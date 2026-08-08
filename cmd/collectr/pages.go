package main

import (
	"context"
	"encoding/hex"
	"errors"
	"html/template"

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
	return page, nil
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
