package api

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/collectr/collectr/internal/contracts"
	"github.com/collectr/collectr/internal/modules/dsr/domain"
	"github.com/collectr/collectr/internal/modules/dsr/store"
	"github.com/collectr/collectr/internal/platform/authn"
	"github.com/collectr/collectr/internal/platform/httpx"
	"github.com/collectr/collectr/internal/platform/notify"
	"github.com/collectr/collectr/internal/platform/postgres"
)

// AdminHandler serves the queue an organisation works through.
//
// The worker completes erasure, withdrawal, access and export on its own, since
// those are mechanical. Objection and restriction need judgement, so they wait
// here -- and without this queue they would wait forever, past their deadline,
// with nobody able to act on them.
type AdminHandler struct {
	db    *postgres.DB
	store *store.Store
	audit contracts.AuditWriter
	// sla is the statutory answering window, needed when an operator raises a
	// request on a subject's behalf. The deadline is stored on the request, so a
	// later change to the configured SLA cannot move a clock already running.
	sla time.Duration
	// notifier tells the subject their record was changed by somebody else.
	//
	// Optional: a deployment with no SMTP still corrects records. Refusing the
	// correction because the notice could not be sent would leave the wrong
	// value on file, which is the worse of the two failures.
	notifier notify.Notifier
	log      *slog.Logger
	// baseURL is the origin the portal link in that notice points at.
	baseURL string
}

// SetNotifier supplies the channel used to tell subjects about corrections made
// on their behalf.
func (h *AdminHandler) SetNotifier(n notify.Notifier, log *slog.Logger, baseURL string) {
	h.notifier, h.log, h.baseURL = n, log, strings.TrimRight(baseURL, "/")
}

// NewAdmin returns an AdminHandler.
func NewAdmin(db *postgres.DB, s *store.Store, audit contracts.AuditWriter, sla time.Duration) *AdminHandler {
	if sla <= 0 {
		sla = 72 * time.Hour
	}
	return &AdminHandler{db: db, store: s, audit: audit, sla: sla}
}

// RegisterAdmin mounts the routes.
func (h *AdminHandler) RegisterAdmin(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/dsr/requests", h.list)
	mux.HandleFunc("POST /api/v1/dsr/requests/{id}/fulfill", h.fulfill)
	mux.HandleFunc("POST /api/v1/dsr/requests/{id}/reject", h.reject)
}

// actor resolves the caller and enforces the capability.
//
// dsr.handle is deliberately barred from API keys: deciding whether to refuse
// somebody's objection is an act that needs an accountable person, not a string
// in a CI configuration.
func (h *AdminHandler) actor(w http.ResponseWriter, r *http.Request) (authn.Actor, bool) {
	a, ok := authn.ActorFrom(r.Context())
	if !ok {
		httpx.Error(w, r, http.StatusUnauthorized, "unauthenticated", "Authentication required")
		return authn.Actor{}, false
	}
	if !a.Can(authn.CapDSRHandle) {
		httpx.Error(w, r, http.StatusForbidden, "forbidden", "Insufficient permissions")
		return authn.Actor{}, false
	}
	return a, true
}

func (h *AdminHandler) list(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}

	openOnly := r.URL.Query().Get("status") != "all"
	overdueOnly := r.URL.Query().Get("overdue") == "true"
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 100
	}

	requests, err := h.store.ListForAdmin(r.Context(), actor.TenantID, openOnly, overdueOnly, limit)
	if err != nil {
		httpx.Logger(r.Context()).Error("listing dsr requests", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}

	now := time.Now()
	out := make([]map[string]any, 0, len(requests))
	var overdue int
	for _, req := range requests {
		isOverdue := req.Overdue(now)
		if isOverdue {
			overdue++
		}
		out = append(out, map[string]any{
			"id": req.ID, "type": req.Type, "status": req.Status,
			"received_at": req.ReceivedAt, "due_at": req.DueAt,
			"overdue": isOverdue,
			// Hours remaining, so a queue can be sorted by urgency without the
			// reader doing arithmetic on timestamps.
			"hours_remaining": int(time.Until(req.DueAt).Hours()),
			"note":            req.Note,
			// The subject id, never their email or phone: whoever works this queue
			// needs to act on a case, not to read the person's contact details.
			"data_subject_id": req.SubjectID,
			"needs_human":     needsHuman(req.Type),
		})
	}

	httpx.JSON(w, r, http.StatusOK, map[string]any{
		"data":          out,
		"overdue_count": overdue,
	})
}

// needsHuman reports whether a request type waits for a person.
func needsHuman(t string) bool {
	switch t {
	case domain.TypeObject, domain.TypeRestrict, domain.TypeRectify:
		return true
	default:
		return false
	}
}

type resolveBody struct {
	Note string `json:"note"`
}

// fulfill closes a request as granted.
//
// For a restriction that also means acting on it: marking the subject's
// submissions restricted, which every read path already filters on. Recording
// the decision without applying it would be a queue that produces paperwork and
// no effect.
func (h *AdminHandler) fulfill(w http.ResponseWriter, r *http.Request) {
	h.resolve(w, r, domain.StatusFulfilled, "dsr.fulfilled")
}

// reject closes a request as refused.
//
// A refusal is a legitimate outcome -- some objections do not have to be
// granted -- but it needs a reason on the record, because the data subject may
// challenge it and the controller has to be able to explain itself.
func (h *AdminHandler) reject(w http.ResponseWriter, r *http.Request) {
	h.resolve(w, r, domain.StatusRejected, "dsr.rejected")
}

// errRectifyNeedsCorrection is returned when somebody tries to close a
// rectification request without supplying the corrected values.
var errRectifyNeedsCorrection = errors.New("a rectification is closed by correcting the record")

func (h *AdminHandler) resolve(w http.ResponseWriter, r *http.Request, status, action string) {
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, http.StatusNotFound, "not_found", "Request not found")
		return
	}

	var body resolveBody
	if err := httpx.DecodeJSON(w, r, &body, 8<<10); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_body", "Request body is not valid JSON")
		return
	}
	if status == domain.StatusRejected && body.Note == "" {
		httpx.ErrorWithFields(w, r, http.StatusUnprocessableEntity, "validation_failed",
			"Invalid request", map[string]any{"note": "từ chối phải nêu lý do"})
		return
	}

	var (
		resolved   store.AdminRequest
		restricted int64
		wasLate    bool
	)
	err = h.db.InTenantTx(r.Context(), actor.TenantID, func(tx pgx.Tx) error {
		var err error
		resolved, err = h.store.Resolve(r.Context(), tx, actor.TenantID, id, actor.UserID, status, body.Note)
		if err != nil {
			return err
		}

		// Computed from the deadline itself, before the closing status is applied.
		// Asking Overdue() afterwards always answers false -- a closed request is
		// never "overdue" -- so the flag would record nothing at all.
		wasLate = time.Now().After(resolved.DueAt)

		// A rectification is closed by correcting the record, not by saying it
		// was corrected. Fulfilling one from this endpoint would mark the
		// statutory clock satisfied while the wrong value is still on file --
		// the same paperwork-without-effect outcome the restriction branch below
		// exists to avoid, and the more dangerous of the two, because the
		// subject has been told their data was fixed.
		if status == domain.StatusFulfilled && resolved.Type == domain.TypeRectify {
			return errRectifyNeedsCorrection
		}

		if status == domain.StatusFulfilled && resolved.Type == domain.TypeRestrict {
			if restricted, err = h.store.Restrict(r.Context(), tx, resolved.SubjectID); err != nil {
				return err
			}
		}

		return h.audit.Write(r.Context(), tx, contracts.AuditEntry{
			TenantID: actor.TenantID,
			Actor:    contracts.AuditActor{Type: "user", ID: actor.UserID.String(), IPPrefix: httpx.IPPrefix(r)},
			Action:   action,
			Target:   map[string]any{"request_id": resolved.ID, "subject_id": resolved.SubjectID},
			Payload: map[string]any{
				"type": resolved.Type, "note": body.Note,
				"restricted_submissions": restricted,
				// Whether it was answered in time is part of the record: a late
				// answer is still a fact the controller may have to account for.
				"answered_late": wasLate,
			},
		})
	})

	switch {
	case errors.Is(err, domain.ErrNotFound):
		// Also the answer when somebody else resolved it first.
		httpx.Error(w, r, http.StatusConflict, "already_resolved",
			"Yêu cầu này không còn mở, hoặc không tồn tại")
		return
	case errors.Is(err, errRectifyNeedsCorrection):
		httpx.ErrorWithFields(w, r, http.StatusUnprocessableEntity, "rectify_needs_correction",
			"Yêu cầu chỉnh sửa phải đóng bằng cách sửa dữ liệu",
			map[string]any{
				"answers": "Dùng POST /api/v1/dsr/submissions/{subject_id}/rectify kèm giá trị " +
					"đúng. Đóng yêu cầu mà không sửa gì là báo với chủ thể rằng dữ liệu đã được " +
					"sửa trong khi giá trị sai vẫn còn nguyên.",
			})
		return
	case err != nil:
		httpx.Logger(r.Context()).Error("resolving dsr request", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}

	body2 := map[string]any{
		"id": resolved.ID, "type": resolved.Type, "status": resolved.Status,
	}
	if restricted > 0 {
		body2["restricted_submissions"] = restricted
	}
	httpx.JSON(w, r, http.StatusOK, body2)
}
