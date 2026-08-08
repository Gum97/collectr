// Package contracts holds the interfaces and DTOs through which modules see each
// other. It is the only package a module may import from another module: no
// module ever reaches into another's store, and no query joins across schemas.
//
// Keeping the seam here is what makes extracting a service later a mechanical
// change rather than a rewrite -- and, for consent and audit, it is what makes
// their one-way dependency enforceable.
package contracts

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Event types recorded along the funnel.
const (
	EventClick        = "click"
	EventFormView     = "form_view"
	EventFormPageView = "form_page_view"
	EventFormStart    = "form_start"
	EventSubmit       = "submit"
)

// Event is one funnel data point.
//
// EventID is supplied by the producer so that a retried beacon or a redelivered
// stream entry collapses into a single row instead of inflating the funnel.
type Event struct {
	EventID       string         `json:"event_id"`
	TenantID      uuid.UUID      `json:"tenant_id"`
	Type          string         `json:"type"`
	LinkID        *uuid.UUID     `json:"link_id,omitempty"`
	FormID        *uuid.UUID     `json:"form_id,omitempty"`
	FormVersionID *uuid.UUID     `json:"form_version_id,omitempty"`
	PageID        string         `json:"page_id,omitempty"`
	VisitID       *uuid.UUID     `json:"visit_id,omitempty"`
	Meta          map[string]any `json:"meta,omitempty"`
	OccurredAt    time.Time      `json:"occurred_at"`
}

// EventCollector accepts funnel events.
//
// Collect never returns an error and never blocks: analytics is explicitly
// best-effort, and no measurement is worth failing a redirect for. Dropped
// events are counted, not surfaced.
type EventCollector interface {
	Collect(ctx context.Context, e Event)
}

// PublicFormRef identifies a form the public can reach, without the caller
// knowing anything about how forms are stored.
type PublicFormRef struct {
	TenantID  uuid.UUID
	FormID    uuid.UUID
	VersionID uuid.UUID
	ProjectID uuid.UUID
}

// FormLocator resolves a public form id.
//
// The analytics module needs this to attribute an event to a form; giving it a
// narrow lookup rather than the forms module keeps the two separable.
type FormLocator interface {
	LocatePublicForm(ctx context.Context, publicID string) (PublicFormRef, error)
}
