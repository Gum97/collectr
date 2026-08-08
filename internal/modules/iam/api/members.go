package api

import (
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/collectr/collectr/internal/contracts"
	"github.com/collectr/collectr/internal/modules/iam/app"
	"github.com/collectr/collectr/internal/modules/iam/domain"
	"github.com/collectr/collectr/internal/modules/iam/store"
	"github.com/collectr/collectr/internal/platform/authn"
	"github.com/collectr/collectr/internal/platform/httpx"
	"github.com/collectr/collectr/internal/platform/password"
	"github.com/jackc/pgx/v5"
)

// RegisterMembers mounts the membership routes.
func (h *Handler) RegisterMembers(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/members", h.listMembers)
	mux.HandleFunc("DELETE /api/v1/members/{id}", h.removeMember)
	mux.HandleFunc("POST /api/v1/members/invitations", h.invite)
	mux.HandleFunc("GET /api/v1/members/invitations", h.listInvitations)
	mux.HandleFunc("DELETE /api/v1/members/invitations/{id}", h.revokeInvitation)
}

// RegisterInvitePublic mounts the routes an invited person uses before they have
// an account.
func (h *Handler) RegisterInvitePublic(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/auth/invitations/{token}", h.previewInvitation)
	mux.HandleFunc("POST /api/auth/invitations/accept", h.acceptInvitation)
}

func (h *Handler) requireMemberManage(w http.ResponseWriter, r *http.Request) (authn.Actor, bool) {
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

func (h *Handler) listMembers(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.requireMemberManage(w, r)
	if !ok {
		return
	}

	members, err := h.svc.Members(r.Context(), actor.TenantID)
	if err != nil {
		httpx.Logger(r.Context()).Error("listing members", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}

	out := make([]map[string]any, 0, len(members))
	for _, m := range members {
		out = append(out, map[string]any{
			"user_id": m.UserID, "email": m.Email, "name": m.Name,
			"role": m.OrgRole, "joined_at": m.JoinedAt,
		})
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{"data": out})
}

type inviteRequest struct {
	Email   string `json:"email"`
	OrgRole string `json:"org_role"`
	Grants  []struct {
		ProjectID string `json:"project_id"`
		Role      string `json:"role"`
	} `json:"project_grants"`
}

func (h *Handler) invite(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.requireMemberManage(w, r)
	if !ok {
		return
	}

	var req inviteRequest
	if err := httpx.DecodeJSON(w, r, &req, 16<<10); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_body", "Request body is not valid JSON")
		return
	}

	// Nobody may hand out a role they do not hold themselves. Without this an
	// admin could invite an owner and then accept the invitation from their own
	// mailbox.
	if req.OrgRole == domain.RoleOwner && !actor.Can(authn.CapMemberManage) {
		httpx.Error(w, r, http.StatusForbidden, "forbidden", "Insufficient permissions")
		return
	}

	grants := make([]store.ProjectGrant, 0, len(req.Grants))
	for _, g := range req.Grants {
		projectID, err := uuid.Parse(g.ProjectID)
		if err != nil {
			httpx.ErrorWithFields(w, r, http.StatusUnprocessableEntity, "validation_failed",
				"Invalid request", map[string]any{"project_grants": "project_id must be a uuid"})
			return
		}
		if !actor.InProject(projectID) {
			httpx.Error(w, r, http.StatusForbidden, "forbidden",
				"You cannot grant access to a project you do not administer")
			return
		}
		grants = append(grants, store.ProjectGrant{ProjectID: projectID, Role: g.Role})
	}

	orgName, err := h.svc.OrgName(r.Context(), actor.TenantID)
	if err != nil {
		httpx.Logger(r.Context()).Error("loading organisation", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}

	inv, err := h.svc.Invite(r.Context(), app.InviteInput{
		TenantID: actor.TenantID, Email: req.Email, OrgRole: req.OrgRole,
		Grants: grants, InvitedBy: actor.UserID, OrgName: orgName,
	})
	switch {
	case errors.Is(err, domain.ErrAlreadyMember):
		httpx.Error(w, r, http.StatusConflict, "already_member",
			"Người này đã là thành viên của tổ chức")
		return
	case errors.Is(err, domain.ErrInvalidInput):
		httpx.ErrorWithFields(w, r, http.StatusUnprocessableEntity, "validation_failed",
			"Invalid request", map[string]any{"email": err.Error()})
		return
	case err != nil:
		httpx.Logger(r.Context()).Error("inviting member", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}

	h.writeAudit(r, actor, "member.invited",
		map[string]any{"invitation_id": inv.ID},
		// The address is recorded: who was offered access is precisely what an
		// investigation into unexpected access would need.
		map[string]any{"email": inv.Email, "org_role": inv.OrgRole})

	httpx.JSON(w, r, http.StatusCreated, map[string]any{
		"id": inv.ID, "email": inv.Email, "org_role": inv.OrgRole,
		"expires_at": inv.ExpiresAt,
	})
}

func (h *Handler) listInvitations(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.requireMemberManage(w, r)
	if !ok {
		return
	}

	invitations, err := h.svc.PendingInvitations(r.Context(), actor.TenantID)
	if err != nil {
		httpx.Logger(r.Context()).Error("listing invitations", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}

	out := make([]map[string]any, 0, len(invitations))
	for _, inv := range invitations {
		out = append(out, map[string]any{
			"id": inv.ID, "email": inv.Email, "org_role": inv.OrgRole,
			"expires_at": inv.ExpiresAt, "created_at": inv.CreatedAt,
			// The project roles the invitation carries. The store has always read
			// them and this response dropped them, so revoking an invitation and
			// issuing a new one silently lost the project access it was for --
			// and the person re-issuing it had nothing on screen to copy from.
			"project_grants": grantsOf(inv),
		})
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{"data": out})
}

func (h *Handler) revokeInvitation(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.requireMemberManage(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, http.StatusNotFound, "not_found", "Invitation not found")
		return
	}

	err = h.svc.RevokeInvitation(r.Context(), actor.TenantID, id)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		httpx.Error(w, r, http.StatusNotFound, "not_found", "Invitation not found")
		return
	case err != nil:
		httpx.Logger(r.Context()).Error("revoking invitation", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}

	h.writeAudit(r, actor, "member.invitation_revoked",
		map[string]any{"invitation_id": id}, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) removeMember(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.requireMemberManage(w, r)
	if !ok {
		return
	}
	userID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, http.StatusNotFound, "not_found", "Member not found")
		return
	}

	err = h.svc.RemoveMember(r.Context(), actor.TenantID, userID)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		httpx.Error(w, r, http.StatusNotFound, "not_found", "Member not found")
		return
	case errors.Is(err, domain.ErrLastOwner):
		httpx.Error(w, r, http.StatusConflict, "last_owner",
			"Tổ chức phải còn ít nhất một chủ sở hữu")
		return
	case err != nil:
		httpx.Logger(r.Context()).Error("removing member", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}

	h.writeAudit(r, actor, "member.removed", map[string]any{"user_id": userID}, nil)
	w.WriteHeader(http.StatusNoContent)
}

// previewInvitation lets the acceptance page show who was invited and whether
// they need to choose a password.
func (h *Handler) previewInvitation(w http.ResponseWriter, r *http.Request) {
	preview, err := h.svc.PreviewInvitation(r.Context(), r.PathValue("token"))
	if err != nil {
		// Expired, revoked and never-existed are one answer.
		httpx.Error(w, r, http.StatusNotFound, "invalid_invitation",
			"Lời mời này không còn hiệu lực")
		return
	}
	httpx.JSON(w, r, http.StatusOK, preview)
}

type acceptRequest struct {
	Token    string `json:"token"`
	Name     string `json:"name"`
	Password string `json:"password"`
}

func (h *Handler) acceptInvitation(w http.ResponseWriter, r *http.Request) {
	var req acceptRequest
	if err := httpx.DecodeJSON(w, r, &req, 8<<10); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_body", "Request body is not valid JSON")
		return
	}

	userID, tenantID, err := h.svc.AcceptInvitation(r.Context(), req.Token, req.Name, req.Password)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		httpx.Error(w, r, http.StatusNotFound, "invalid_invitation",
			"Lời mời này không còn hiệu lực")
		return
	case errors.Is(err, password.ErrTooShort), errors.Is(err, password.ErrTooLong),
		errors.Is(err, domain.ErrInvalidCredentials):
		httpx.ErrorWithFields(w, r, http.StatusUnprocessableEntity, "validation_failed",
			"Invalid request", map[string]any{"password": "cần mật khẩu dài ít nhất 12 ký tự"})
		return
	case err != nil:
		httpx.Logger(r.Context()).Error("accepting invitation", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}

	if err := h.db.InTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		return h.audit.Write(r.Context(), tx, contracts.AuditEntry{
			TenantID: tenantID,
			Actor:    contracts.AuditActor{Type: "user", ID: userID.String(), IPPrefix: httpx.IPPrefix(r)},
			Action:   "member.joined",
			Target:   map[string]any{"user_id": userID},
		})
	}); err != nil {
		httpx.Logger(r.Context()).Error("auditing invitation acceptance", "error", err)
	}

	httpx.JSON(w, r, http.StatusOK, map[string]any{
		"user_id": userID, "tenant_id": tenantID,
		"message": "Đã tham gia tổ chức. Hãy đăng nhập.",
	})
}

// writeAudit records an administrative action, best-effort.
//
// A failed audit write must not undo the action the administrator took, but it
// must be loud: a permission change with no trail is exactly what an
// investigation would need and not find.
func (h *Handler) writeAudit(r *http.Request, actor authn.Actor, action string, target, payload map[string]any) {
	err := h.db.InTenantTx(r.Context(), actor.TenantID, func(tx pgx.Tx) error {
		return h.audit.Write(r.Context(), tx, contracts.AuditEntry{
			TenantID: actor.TenantID,
			Actor:    contracts.AuditActor{Type: "user", ID: actor.UserID.String(), IPPrefix: httpx.IPPrefix(r)},
			Action:   action, Target: target, Payload: payload,
		})
	})
	if err != nil {
		httpx.Logger(r.Context()).Error("writing audit entry", "error", err, "action", action)
	}
}

// grantsOf renders an invitation's project roles for the listing.
//
// Always a list, never null: a client that maps over the field would otherwise
// have to special-case an invitation that grants nothing.
func grantsOf(inv store.Invitation) []map[string]any {
	out := make([]map[string]any, 0, len(inv.Grants))
	for _, g := range inv.Grants {
		out = append(out, map[string]any{"project_id": g.ProjectID, "role": g.Role})
	}
	return out
}
