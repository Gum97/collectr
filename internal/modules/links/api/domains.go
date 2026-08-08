package api

import (
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/collectr/collectr/internal/modules/links/domain"
	"github.com/collectr/collectr/internal/platform/authn"
	"github.com/collectr/collectr/internal/platform/httpx"
)

// RegisterDomains mounts the domain routes.
//
// Adding a hostname is an organisation-level act, not a link-level one -- it
// changes what the deployment answers to and needs a certificate and a DNS
// record to go with it -- so it takes member.manage rather than link.write. An
// editor who can create links should not be able to point a new domain at them.
func (h *Handler) RegisterDomains(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/domains", h.listDomains)
	mux.HandleFunc("POST /api/v1/domains", h.addDomain)
	mux.HandleFunc("PUT /api/v1/domains/{id}/default", h.setDefaultDomain)
	mux.HandleFunc("DELETE /api/v1/domains/{id}", h.deleteDomain)
}

type domainResponse struct {
	ID        string `json:"id"`
	Host      string `json:"host"`
	IsDefault bool   `json:"is_default"`
	LinkCount int    `json:"link_count"`
	ShortURL  string `json:"short_url_example"`
}

func (h *Handler) listDomains(w http.ResponseWriter, r *http.Request) {
	actor, ok := authn.ActorFrom(r.Context())
	if !ok {
		httpx.Error(w, r, http.StatusUnauthorized, "unauthenticated", "Authentication required")
		return
	}
	if !actor.Can(authn.CapLinkRead) {
		httpx.Error(w, r, http.StatusForbidden, "forbidden", "Insufficient permissions")
		return
	}

	domains, err := h.svc.Domains(r.Context(), actor.TenantID)
	if err != nil {
		httpx.Logger(r.Context()).Error("listing domains", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}

	out := make([]domainResponse, 0, len(domains))
	for _, d := range domains {
		out = append(out, domainResponse{
			ID: d.ID.String(), Host: d.Host, IsDefault: d.IsDefault,
			LinkCount: d.LinkCount, ShortURL: h.scheme + "://" + d.Host + "/r/abc123",
		})
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{"data": out})
}

type addDomainRequest struct {
	Host      string `json:"host"`
	IsDefault bool   `json:"is_default"`
}

func (h *Handler) addDomain(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.requireDomainManage(w, r)
	if !ok {
		return
	}

	var req addDomainRequest
	if err := httpx.DecodeJSON(w, r, &req, 4<<10); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_body", "Request body is not valid JSON")
		return
	}

	d, err := h.svc.AddDomain(r.Context(), actor.TenantID, req.Host, req.IsDefault)
	switch {
	case errors.Is(err, domain.ErrInvalidHost):
		httpx.ErrorWithFields(w, r, http.StatusUnprocessableEntity, "validation_failed",
			"Invalid request", map[string]any{"host": err.Error()})
		return
	case errors.Is(err, domain.ErrHostTaken):
		// Deliberately does not say whether the holder is this tenant or another
		// one: on a shared deployment that would report which hostnames other
		// organisations have registered.
		httpx.Error(w, r, http.StatusConflict, "host_taken",
			"Tên miền này đã được đăng ký.")
		return
	case err != nil:
		httpx.Logger(r.Context()).Error("adding domain", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}

	httpx.JSON(w, r, http.StatusCreated, map[string]any{
		"id": d.ID, "host": d.Host, "is_default": d.IsDefault,
		"next_step": "Trỏ bản ghi DNS của " + d.Host + " về deployment này, " +
			"và thêm host vào SITE_ADDRESS để Caddy cấp chứng chỉ.",
	})
}

func (h *Handler) setDefaultDomain(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.requireDomainManage(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, http.StatusNotFound, "not_found", "Domain not found")
		return
	}

	err = h.svc.SetDefaultDomain(r.Context(), actor.TenantID, id)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		httpx.Error(w, r, http.StatusNotFound, "not_found", "Domain not found")
		return
	case err != nil:
		httpx.Logger(r.Context()).Error("setting default domain", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{
		"is_default": true,
		"message": "Link tạo mới sẽ dùng tên miền này. Link cũ giữ nguyên " +
			"tên miền của chúng.",
	})
}

func (h *Handler) deleteDomain(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.requireDomainManage(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, http.StatusNotFound, "not_found", "Domain not found")
		return
	}

	err = h.svc.RemoveDomain(r.Context(), actor.TenantID, id)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		httpx.Error(w, r, http.StatusNotFound, "not_found", "Domain not found")
		return
	case errors.Is(err, domain.ErrDomainInUse):
		httpx.Error(w, r, http.StatusConflict, "domain_in_use",
			"Tên miền còn link đang dùng. Xóa hoặc chuyển các link đó trước — "+
				"xóa tên miền sẽ làm hỏng mọi mã đã in ra hoặc đã chia sẻ.")
		return
	case err != nil:
		httpx.Logger(r.Context()).Error("deleting domain", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) requireDomainManage(w http.ResponseWriter, r *http.Request) (authn.Actor, bool) {
	actor, ok := authn.ActorFrom(r.Context())
	if !ok {
		httpx.Error(w, r, http.StatusUnauthorized, "unauthenticated", "Authentication required")
		return authn.Actor{}, false
	}
	if !actor.Can(authn.CapMemberManage) {
		httpx.Error(w, r, http.StatusForbidden, "forbidden", "Insufficient permissions")
		return authn.Actor{}, false
	}
	return actor, true
}
