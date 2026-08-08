package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/collectr/collectr/internal/modules/iam/domain"
	"github.com/collectr/collectr/internal/platform/authn"
	"github.com/collectr/collectr/internal/platform/httpx"
)

// Deployment is what the running server was configured with.
//
// Read-only, and it stays that way. Storage, keys, rate limits and timeouts
// belong to whoever runs the process, not to whoever administers the
// organisation inside it -- Collectr keeps those two roles apart everywhere
// else and this is the screen where the distinction is easiest to lose.
//
// The concrete reason, for whoever is tempted later: an administrator who could
// change the storage endpoint from the interface could point every future
// attachment at a bucket they own, with one form submission and no access to
// the server at all. Changing it at runtime would also strand everything
// already written -- files.storage_key would address a bucket the process no
// longer has, and the symptom would be scattered 404s rather than an error.
//
// Nothing here is a secret. Driver name, not endpoint; whether mail is
// configured, not the credentials.
type Deployment struct {
	StorageDriver string `json:"storage_driver"`
	// MailConfigured tells an operator why invitations are not arriving. Without
	// it the failure is silent: the deployment looks healthy and nobody can join.
	MailConfigured bool `json:"mail_configured"`

	BaseURL      string `json:"base_url"`
	ShortURLBase string `json:"short_url_base"`

	DefaultRetentionDays int `json:"default_retention_days"`
	DSRSLAHours          int `json:"dsr_sla_hours"`
	MFAGraceHours        int `json:"mfa_grace_hours"`

	PublicWriteIPLimit   int `json:"public_write_ip_limit"`
	PublicWriteFormLimit int `json:"public_write_form_limit"`
}

// SetDeployment supplies the read-only facts shown on the settings screen.
func (h *Handler) SetDeployment(d Deployment) { h.deployment = d }

// RegisterOrg mounts the organisation settings routes.
func (h *Handler) RegisterOrg(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/org", h.getOrg)
	mux.HandleFunc("PATCH /api/v1/org", h.updateOrg)
}

func (h *Handler) getOrg(w http.ResponseWriter, r *http.Request) {
	actor, ok := authn.ActorFrom(r.Context())
	if !ok || !actor.Can(authn.CapMemberManage) {
		httpx.Error(w, r, http.StatusForbidden, "forbidden", "Insufficient permissions")
		return
	}

	t, err := h.svc.Organisation(r.Context(), actor.TenantID)
	if err != nil {
		httpx.Logger(r.Context()).Error("reading organisation", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}

	httpx.JSON(w, r, http.StatusOK, map[string]any{
		"id":   t.ID,
		"name": t.Name,
		// The address this very request resolved to, by the same code that
		// stamps consent records and keys the rate limiter.
		//
		// Here because a wrong TRUSTED_PROXY_HOPS is otherwise invisible: the
		// product keeps working, and the only trace is a proxy's address sitting
		// in the evidence that says who agreed and from where. An operator who
		// opens this page from their own machine can see in one line whether the
		// deployment is recording visitors or recording its own front door.
		"your_ip": httpx.ClientIP(r).String(),
		// The slug is shown but not editable: it is in the consent permalinks a
		// data subject was already given.
		"slug":       t.Slug,
		"settings":   t.Settings,
		"deployment": h.deployment,
	})
}

type updateOrgRequest struct {
	Name string `json:"name"`
	// Settings replaces the stored object wholesale. Small and hand-edited by
	// one screen, so a merge would only hide which fields that screen forgot.
	Settings map[string]any `json:"settings"`
}

func (h *Handler) updateOrg(w http.ResponseWriter, r *http.Request) {
	actor, ok := authn.ActorFrom(r.Context())
	if !ok || !actor.Can(authn.CapMemberManage) {
		httpx.Error(w, r, http.StatusForbidden, "forbidden", "Insufficient permissions")
		return
	}

	var req updateOrgRequest
	if err := httpx.DecodeJSON(w, r, &req, 64<<10); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_body", "Request body is not valid JSON")
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		httpx.ErrorWithFields(w, r, http.StatusUnprocessableEntity, "validation_failed",
			"Invalid request", map[string]any{"name": "tên tổ chức không được để trống"})
		return
	}
	if len(name) > 200 {
		httpx.ErrorWithFields(w, r, http.StatusUnprocessableEntity, "validation_failed",
			"Invalid request", map[string]any{"name": "tên tổ chức quá dài"})
		return
	}

	// This name is printed on the consent notice a respondent reads, as the
	// party collecting their data. Changing it does not rewrite consent already
	// recorded -- those cite a document version by hash -- but it does change
	// who the next respondent is told they are dealing with.
	if err := h.svc.UpdateOrganisation(r.Context(), actor.TenantID, actor.UserID,
		httpx.IPPrefix(r), name, req.Settings); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		httpx.Logger(r.Context()).Error("updating organisation", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
