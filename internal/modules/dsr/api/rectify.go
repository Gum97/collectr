package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/collectr/collectr/internal/contracts"
	"github.com/collectr/collectr/internal/modules/dsr/domain"
	"github.com/collectr/collectr/internal/modules/dsr/store"
	"github.com/collectr/collectr/internal/platform/authn"
	"github.com/collectr/collectr/internal/platform/httpx"
)

// verificationMethods are the ways an operator may have satisfied themselves
// that the caller is the data subject.
//
// A closed list rather than free text. The field exists to answer "on what
// basis did you believe this was them", and a box that accepts anything gets
// filled with "ok" -- which records that somebody typed something, not that
// anybody was verified.
var verificationMethods = map[string]string{
	"phone_callback": "Gọi lại số đã đăng ký",
	"email_reply":    "Trả lời từ email đã đăng ký",
	"in_person":      "Xuất trình giấy tờ trực tiếp",
	"portal_session": "Chủ thể tự yêu cầu qua cổng",
}

type rectifyRequest struct {
	// SubmissionID is the record to correct.
	SubmissionID string `json:"submission_id"`
	// Answers replaces the plaintext answers wholesale, like the portal's own
	// correction. Sensitive fields are refused: they are sealed under the
	// subject's key and this path cannot re-seal them.
	Answers map[string]any `json:"answers"`
	// Verification is how the operator established the caller is the subject.
	Verification string `json:"verification_method"`
	Note         string `json:"note"`
}

// RegisterRectify mounts the operator-side correction.
func (h *AdminHandler) RegisterRectify(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/dsr/submissions/{id}/rectify", h.rectifyOnBehalf)
	mux.HandleFunc("GET /api/v1/dsr/verification-methods", h.listVerificationMethods)
}

func (h *AdminHandler) listVerificationMethods(w http.ResponseWriter, r *http.Request) {
	out := make([]map[string]string, 0, len(verificationMethods))
	for code, label := range verificationMethods {
		out = append(out, map[string]string{"code": code, "label": label})
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{"data": out})
}

// rectifyOnBehalf corrects a record for a subject who asked an employee to do it.
//
// The common case this exists for: somebody rings up, says they mistyped their
// phone number, and wants it fixed while they are on the call. Telling them to
// wait for an email and work through a portal is the answer that loses the
// correction -- most people do not come back, and the wrong number stays.
//
// It is not a general edit. An operator cannot open a record and change it: the
// only way through this endpoint is to raise a rectification request, name how
// the caller was verified, and have both the request and the change committed
// together. The request is the lawful basis and the paper trail at once.
//
// Article 4 of Law 91/2025 gives the subject the right to have their data
// corrected. It does not require them to perform the edit themselves -- the
// controller acting on the request is the ordinary way that right is exercised.
// What the record has to show is who changed what, when, and on what footing,
// and that is exactly what this writes.
func (h *AdminHandler) rectifyOnBehalf(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	// The same capability that handles every other data subject request. An
	// operator who may not process a DSR may not correct a record either.
	if !actor.Can(authn.CapDSRHandle) {
		httpx.Error(w, r, http.StatusForbidden, "forbidden", "Insufficient permissions")
		return
	}

	subjectID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, http.StatusNotFound, "not_found", "Subject not found")
		return
	}

	var req rectifyRequest
	if err := httpx.DecodeJSON(w, r, &req, 256<<10); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_body", "Request body is not valid JSON")
		return
	}

	submissionID, err := uuid.Parse(strings.TrimSpace(req.SubmissionID))
	if err != nil {
		httpx.ErrorWithFields(w, r, http.StatusUnprocessableEntity, "validation_failed",
			"Invalid request", map[string]any{"submission_id": "phải là uuid"})
		return
	}
	if len(req.Answers) == 0 {
		// The whole point of this endpoint. Closing a rectification with no
		// correction is the paperwork-without-effect outcome the restriction
		// path already refuses to produce.
		httpx.ErrorWithFields(w, r, http.StatusUnprocessableEntity, "validation_failed",
			"Invalid request", map[string]any{"answers": "phải nêu giá trị cần sửa"})
		return
	}
	if _, known := verificationMethods[req.Verification]; !known {
		httpx.ErrorWithFields(w, r, http.StatusUnprocessableEntity, "validation_failed",
			"Invalid request", map[string]any{
				"verification_method": "phải nêu cách xác minh chủ thể: " +
					"phone_callback, email_reply, in_person hoặc portal_session",
			})
		return
	}

	var created domain.Request
	err = h.db.InTenantTx(r.Context(), actor.TenantID, func(tx pgx.Tx) error {
		// Raised and closed in the same breath, because the subject asked for it
		// on a call that is still in progress. The SLA clock is still recorded:
		// a request answered in ninety seconds is a fact worth having.
		created, err = h.store.CreateRequest(r.Context(), tx, actor.TenantID, subjectID,
			domain.TypeRectify, req.Verification, h.sla)
		if err != nil {
			return err
		}
		if err := h.store.RectifyTx(r.Context(), tx, actor.TenantID, subjectID, submissionID,
			req.Answers, store.OperatorRectifier(actor.UserID)); err != nil {
			return err
		}
		reqID, perr := uuid.Parse(created.ID)
		if perr != nil {
			return perr
		}
		if _, err := h.store.Resolve(r.Context(), tx, actor.TenantID, reqID, actor.UserID,
			domain.StatusFulfilled, req.Note); err != nil {
			return err
		}
		return h.audit.Write(r.Context(), tx, contracts.AuditEntry{
			TenantID: actor.TenantID,
			Actor: contracts.AuditActor{
				Type: "user", ID: actor.UserID.String(), IPPrefix: httpx.IPPrefix(r),
			},
			Action: "submission.updated",
			Target: map[string]any{
				"submission_id": submissionID,
				"subject_id":    subjectID,
				"request_id":    created.ID,
			},
			// The changed values are not recorded here; they live in the
			// revision row. What belongs in the trail is that an employee made
			// the change, for whom, and on what verification.
			Payload: map[string]any{
				"source":              "dsr_operator",
				"verification_method": req.Verification,
				"fields":              len(req.Answers),
				"note":                req.Note,
			},
		})
	})
	switch {
	case errors.Is(err, domain.ErrForbidden):
		// 404: confirming the record exists but belongs to someone else is
		// itself a disclosure.
		http.NotFound(w, r)
		return
	case err != nil:
		httpx.Logger(r.Context()).Error("rectifying on behalf", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}

	httpx.JSON(w, r, http.StatusOK, map[string]any{
		"request_id": created.ID,
		"message": "Đã sửa và ghi vào nhật ký. Chủ thể dữ liệu được thông báo về " +
			"thay đổi này; giá trị cũ vẫn giữ trong bản ghi sửa đổi.",
	})
}
