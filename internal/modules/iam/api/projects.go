package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/collectr/collectr/internal/modules/iam/domain"
	"github.com/collectr/collectr/internal/modules/iam/store"
	"github.com/collectr/collectr/internal/platform/authn"
	"github.com/collectr/collectr/internal/platform/httpx"
)

// RegisterProjects mounts the project routes.
func (h *Handler) RegisterProjects(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/projects", h.listProjects)
	mux.HandleFunc("POST /api/v1/projects", h.createProject)
	mux.HandleFunc("PATCH /api/v1/projects/{id}", h.updateProject)
	mux.HandleFunc("DELETE /api/v1/projects/{id}", h.archiveProject)
	mux.HandleFunc("GET /api/v1/projects/{id}/members", h.listProjectMembers)
	mux.HandleFunc("PUT /api/v1/projects/{id}/members/{user_id}", h.grantProjectRole)
	mux.HandleFunc("DELETE /api/v1/projects/{id}/members/{user_id}", h.revokeProjectRole)
}

// listProjects is readable by anyone signed in.
//
// Knowing a project exists is not the same as seeing what is inside it: the
// forms, links and submissions each check their own capability. Hiding the list
// would only stop people asking to be added to something they cannot find.
func (h *Handler) listProjects(w http.ResponseWriter, r *http.Request) {
	actor, ok := authn.ActorFrom(r.Context())
	if !ok {
		httpx.Error(w, r, http.StatusUnauthorized, "unauthenticated", "Authentication required")
		return
	}

	projects, err := h.svc.Projects(r.Context(), actor.TenantID, actor.UserID,
		r.URL.Query().Get("include_archived") == "true")
	if err != nil {
		httpx.Logger(r.Context()).Error("listing projects", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}

	out := make([]map[string]any, 0, len(projects))
	for _, p := range projects {
		out = append(out, map[string]any{
			"id": p.ID, "name": p.Name, "slug": p.Slug,
			"default_retention_days": p.DefaultRetentionDays,
			"archived_at":            p.ArchivedAt,
			"member_count":           p.MemberCount,
			// Empty means the reader holds no role here. The navigation tree
			// renders that as a named but unopenable project rather than hiding
			// it, so people can ask to be added to something they can see exists.
			"my_role": p.MyRole,
			// Whether the reader can open it, answered here rather than derived
			// by the client from my_role. Access arrives two ways -- an
			// organisation role that spans every project, or a grant on this one
			// -- and a client that re-implements that rule will eventually
			// disagree with the API that enforces it.
			"access":     accessLevel(actor, p),
			"created_at": p.CreatedAt,
		})
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{"data": out})
}

type projectBody struct {
	Name          string `json:"name"`
	Slug          string `json:"slug"`
	RetentionDays *int   `json:"default_retention_days"`
}

func (h *Handler) createProject(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.requireMemberManage(w, r)
	if !ok {
		return
	}

	var body projectBody
	if err := httpx.DecodeJSON(w, r, &body, 8<<10); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_body", "Request body is not valid JSON")
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		httpx.ErrorWithFields(w, r, http.StatusUnprocessableEntity, "validation_failed",
			"Invalid request", map[string]any{"name": "must not be empty"})
		return
	}
	if body.RetentionDays != nil && (*body.RetentionDays < 1 || *body.RetentionDays > 3650) {
		httpx.ErrorWithFields(w, r, http.StatusUnprocessableEntity, "validation_failed",
			"Invalid request", map[string]any{"default_retention_days": "must be between 1 and 3650"})
		return
	}

	project, err := h.svc.CreateProject(r.Context(), actor.TenantID, actor.UserID,
		body.Name, body.Slug, body.RetentionDays)
	switch {
	case errors.Is(err, domain.ErrAlreadyMember):
		httpx.Error(w, r, http.StatusConflict, "slug_taken",
			"Đã có dự án dùng định danh này")
		return
	case err != nil:
		httpx.Logger(r.Context()).Error("creating project", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}

	h.writeAudit(r, actor, "project.created",
		map[string]any{"project_id": project.ID}, map[string]any{"name": project.Name})

	httpx.JSON(w, r, http.StatusCreated, map[string]any{
		"id": project.ID, "name": project.Name, "slug": project.Slug,
		"default_retention_days": project.DefaultRetentionDays,
	})
}

func (h *Handler) updateProject(w http.ResponseWriter, r *http.Request) {
	actor, projectID, ok := h.projectRequest(w, r)
	if !ok {
		return
	}

	var body projectBody
	if err := httpx.DecodeJSON(w, r, &body, 8<<10); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_body", "Request body is not valid JSON")
		return
	}

	err := h.svc.UpdateProject(r.Context(), actor.TenantID, projectID, body.Name, body.RetentionDays)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		httpx.Error(w, r, http.StatusNotFound, "not_found", "Project not found")
		return
	case err != nil:
		httpx.Logger(r.Context()).Error("updating project", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}

	// Retention is a compliance setting, so a change to it belongs on the record
	// alongside who made it.
	h.writeAudit(r, actor, "project.updated",
		map[string]any{"project_id": projectID},
		map[string]any{"default_retention_days": body.RetentionDays})
	w.WriteHeader(http.StatusNoContent)
}

// archiveProject retires a project. It never deletes the data inside it.
func (h *Handler) archiveProject(w http.ResponseWriter, r *http.Request) {
	actor, projectID, ok := h.projectRequest(w, r)
	if !ok {
		return
	}

	err := h.svc.ArchiveProject(r.Context(), actor.TenantID, projectID)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		httpx.Error(w, r, http.StatusNotFound, "not_found", "Project not found")
		return
	case err != nil:
		httpx.Logger(r.Context()).Error("archiving project", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}

	h.writeAudit(r, actor, "project.archived", map[string]any{"project_id": projectID}, nil)
	httpx.JSON(w, r, http.StatusOK, map[string]any{
		"archived": true,
		"message": "Dự án đã lưu trữ. Dữ liệu bên trong giữ nguyên và vẫn theo " +
			"chính sách lưu trữ của nó.",
	})
}

func (h *Handler) listProjectMembers(w http.ResponseWriter, r *http.Request) {
	actor, projectID, ok := h.projectRequest(w, r)
	if !ok {
		return
	}

	members, err := h.svc.ProjectMembers(r.Context(), actor.TenantID, projectID)
	if err != nil {
		httpx.Logger(r.Context()).Error("listing project members", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}

	out := make([]map[string]any, 0, len(members))
	for _, m := range members {
		out = append(out, map[string]any{
			"user_id": m.UserID, "email": m.Email, "name": m.Name,
			"role": m.Role, "granted_at": m.GrantedAt,
		})
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{"data": out})
}

type grantBody struct {
	Role string `json:"role"`
}

func (h *Handler) grantProjectRole(w http.ResponseWriter, r *http.Request) {
	actor, projectID, ok := h.projectRequest(w, r)
	if !ok {
		return
	}
	userID, err := uuid.Parse(r.PathValue("user_id"))
	if err != nil {
		httpx.Error(w, r, http.StatusNotFound, "not_found", "Member not found")
		return
	}

	var body grantBody
	if err := httpx.DecodeJSON(w, r, &body, 4<<10); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_body", "Request body is not valid JSON")
		return
	}
	if !domain.ValidProjectRole(body.Role) {
		httpx.ErrorWithFields(w, r, http.StatusUnprocessableEntity, "validation_failed",
			"Invalid request", map[string]any{"role": "must be manager, editor, analyst or viewer"})
		return
	}

	err = h.svc.GrantProjectRole(r.Context(), actor.TenantID, projectID, userID, actor.UserID, body.Role)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		httpx.Error(w, r, http.StatusNotFound, "not_found",
			"Người này chưa thuộc tổ chức. Hãy mời họ trước.")
		return
	case err != nil:
		httpx.Logger(r.Context()).Error("granting project role", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}

	h.writeAudit(r, actor, "project.role_granted",
		map[string]any{"project_id": projectID, "user_id": userID},
		map[string]any{"role": body.Role})
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) revokeProjectRole(w http.ResponseWriter, r *http.Request) {
	actor, projectID, ok := h.projectRequest(w, r)
	if !ok {
		return
	}
	userID, err := uuid.Parse(r.PathValue("user_id"))
	if err != nil {
		httpx.Error(w, r, http.StatusNotFound, "not_found", "Member not found")
		return
	}

	err = h.svc.RevokeProjectRole(r.Context(), actor.TenantID, projectID, userID)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		httpx.Error(w, r, http.StatusNotFound, "not_found", "Member not found")
		return
	case err != nil:
		httpx.Logger(r.Context()).Error("revoking project role", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}

	h.writeAudit(r, actor, "project.role_revoked",
		map[string]any{"project_id": projectID, "user_id": userID}, nil)
	w.WriteHeader(http.StatusNoContent)
}

// projectRequest resolves the actor, the capability and the project id.
func (h *Handler) projectRequest(w http.ResponseWriter, r *http.Request) (authn.Actor, uuid.UUID, bool) {
	actor, ok := h.requireMemberManage(w, r)
	if !ok {
		return authn.Actor{}, uuid.Nil, false
	}
	projectID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, http.StatusNotFound, "not_found", "Project not found")
		return authn.Actor{}, uuid.Nil, false
	}
	// An actor scoped to one project cannot administer another, whatever their
	// capability set says.
	if !actor.InProject(projectID) {
		httpx.Error(w, r, http.StatusForbidden, "forbidden", "Insufficient permissions")
		return authn.Actor{}, uuid.Nil, false
	}
	return actor, projectID, true
}

// accessLevel reports how the reader reaches a project, if at all.
//
// "org" covers owner, admin and DPO, whose roles span the organisation.
// "project" is an explicit grant. A plain member with neither holds no
// capabilities at all, which is what makes the empty set the right test rather
// than a list of role names -- a role added later is classified correctly
// without anyone remembering to edit this.
func accessLevel(actor authn.Actor, p store.Project) string {
	switch {
	case p.MyRole != "":
		return "project"
	case actor.InProject(p.ID) && len(actor.Capabilities()) > 0:
		return "org"
	default:
		return "none"
	}
}
