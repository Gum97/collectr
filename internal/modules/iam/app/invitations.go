package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/collectr/collectr/internal/modules/iam/domain"
	"github.com/collectr/collectr/internal/modules/iam/store"
	"github.com/collectr/collectr/internal/platform/notify"
	"github.com/collectr/collectr/internal/platform/password"
)

// InvitationTTL is how long an offer stays open.
//
// A week: long enough to survive a holiday, short enough that a forwarded email
// found months later is worthless.
const InvitationTTL = 7 * 24 * time.Hour

// InviteInput describes an offer of membership.
type InviteInput struct {
	TenantID  uuid.UUID
	Email     string
	OrgRole   string
	Grants    []store.ProjectGrant
	InvitedBy uuid.UUID
	OrgName   string
}

// Invite creates an invitation and emails the link.
//
// Unlike the data subject portal, this endpoint does report that the person is
// already a member. The caller is an administrator of that organisation, so its
// membership list is information they already hold.
func (s *Service) Invite(ctx context.Context, in InviteInput) (store.Invitation, error) {
	email := strings.ToLower(strings.TrimSpace(in.Email))
	if email == "" || !strings.Contains(email, "@") {
		return store.Invitation{}, fmt.Errorf("%w: a valid email address is required", domain.ErrInvalidInput)
	}
	if !domain.ValidOrgRole(in.OrgRole) {
		return store.Invitation{}, fmt.Errorf("%w: unknown organisation role %q", domain.ErrInvalidInput, in.OrgRole)
	}
	for _, g := range in.Grants {
		if !domain.ValidProjectRole(g.Role) {
			return store.Invitation{}, fmt.Errorf("%w: unknown project role %q", domain.ErrInvalidInput, g.Role)
		}
	}

	raw, err := newToken()
	if err != nil {
		return store.Invitation{}, err
	}

	inv, err := s.store.CreateInvitation(ctx, store.Invitation{
		TenantID: in.TenantID, Email: email, OrgRole: in.OrgRole,
		Grants: in.Grants, ExpiresAt: time.Now().UTC().Add(InvitationTTL),
		InvitedBy: in.InvitedBy,
	}, raw)
	if err != nil {
		return store.Invitation{}, err
	}

	link := fmt.Sprintf("%s/invite?token=%s", s.baseURL, raw)
	body := fmt.Sprintf(
		"Bạn được mời tham gia %s trên Collectr với vai trò %s.\n\n%s\n\n"+
			"Lời mời có hiệu lực trong 7 ngày.\n"+
			"Nếu bạn không mong đợi email này, hãy bỏ qua nó.",
		in.OrgName, roleLabel(in.OrgRole), link)

	if err := s.notifier.Send(ctx, notify.Message{
		To:      email,
		Subject: fmt.Sprintf("Lời mời tham gia %s", in.OrgName),
		Body:    body,
	}); err != nil {
		// The invitation stands even if the mail fails: the administrator can
		// resend, and rolling it back would lose the record that it was made.
		s.log.Error("sending invitation", "error", err, "invitation_id", inv.ID)
	}
	return inv, nil
}

// InvitationPreview is what the acceptance page shows before anyone commits.
type InvitationPreview struct {
	Email       string `json:"email"`
	OrgRole     string `json:"org_role"`
	NeedsSignup bool   `json:"needs_signup"`
}

// PreviewInvitation resolves a token so the page can be rendered.
func (s *Service) PreviewInvitation(ctx context.Context, token string) (InvitationPreview, error) {
	inv, err := s.store.FindInvitation(ctx, token)
	if err != nil {
		return InvitationPreview{}, err
	}
	_, exists, err := s.store.FindUserByEmail(ctx, inv.Email)
	if err != nil {
		return InvitationPreview{}, err
	}
	return InvitationPreview{
		Email: inv.Email, OrgRole: inv.OrgRole, NeedsSignup: !exists,
	}, nil
}

// AcceptInvitation joins the invited person to the organisation.
//
// The password is only used when the address has no account yet. Someone already
// signed up keeps the password they have, so an invitation can never be used to
// overwrite the credentials of an existing account.
func (s *Service) AcceptInvitation(ctx context.Context, token, name, pw string) (uuid.UUID, uuid.UUID, error) {
	inv, err := s.store.FindInvitation(ctx, token)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}

	_, exists, err := s.store.FindUserByEmail(ctx, inv.Email)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}

	var hash string
	if !exists {
		if err := password.Validate(pw); err != nil {
			return uuid.Nil, uuid.Nil, err
		}
		if hash, err = password.Hash(pw, password.DefaultParams); err != nil {
			return uuid.Nil, uuid.Nil, err
		}
	}

	userID, err := s.store.AcceptInvitation(ctx, inv, name, hash)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}

	// The new membership decides what they may do, so any cached answer from
	// before it existed has to go.
	s.InvalidateCapabilities(ctx, userID, inv.TenantID)
	return userID, inv.TenantID, nil
}

// OrgName returns an organisation's display name, for use in emails.
func (s *Service) OrgName(ctx context.Context, tenantID uuid.UUID) (string, error) {
	return s.store.TenantName(ctx, tenantID)
}

// Members returns everyone in an organisation.
func (s *Service) Members(ctx context.Context, tenantID uuid.UUID) ([]store.Member, error) {
	return s.store.ListMembers(ctx, tenantID)
}

// PendingInvitations returns the open offers.
func (s *Service) PendingInvitations(ctx context.Context, tenantID uuid.UUID) ([]store.Invitation, error) {
	return s.store.ListInvitations(ctx, tenantID)
}

// RevokeInvitation withdraws an offer.
func (s *Service) RevokeInvitation(ctx context.Context, tenantID, id uuid.UUID) error {
	return s.store.RevokeInvitation(ctx, tenantID, id)
}

// RemoveMember takes someone out of an organisation and cuts their access.
func (s *Service) RemoveMember(ctx context.Context, tenantID, userID uuid.UUID) error {
	if err := s.store.RemoveMember(ctx, tenantID, userID); err != nil {
		return err
	}
	s.InvalidateCapabilities(ctx, userID, tenantID)
	return nil
}

func roleLabel(role string) string {
	switch role {
	case domain.RoleOwner:
		return "chủ sở hữu"
	case domain.RoleAdmin:
		return "quản trị viên"
	case domain.RoleDPO:
		return "phụ trách bảo vệ dữ liệu"
	default:
		return "thành viên"
	}
}

// Projects returns an organisation's projects, each carrying the caller's role
// in it.
func (s *Service) Projects(ctx context.Context, tenantID, userID uuid.UUID, includeArchived bool) ([]store.Project, error) {
	return s.store.ListProjects(ctx, tenantID, userID, includeArchived)
}

// CreateProject adds a project, deriving a slug when none is supplied.
func (s *Service) CreateProject(ctx context.Context, tenantID, createdBy uuid.UUID, name, slug string, retentionDays *int) (store.Project, error) {
	if slug == "" {
		slug = slugify(name)
	}
	return s.store.CreateProject(ctx, store.Project{
		TenantID: tenantID, Name: strings.TrimSpace(name), Slug: slugify(slug),
		DefaultRetentionDays: retentionDays, CreatedBy: createdBy,
	})
}

// UpdateProject changes a project's name or retention default.
func (s *Service) UpdateProject(ctx context.Context, tenantID, id uuid.UUID, name string, retentionDays *int) error {
	return s.store.UpdateProject(ctx, tenantID, id, strings.TrimSpace(name), retentionDays)
}

// ArchiveProject retires a project without touching the data inside it.
func (s *Service) ArchiveProject(ctx context.Context, tenantID, id uuid.UUID) error {
	return s.store.ArchiveProject(ctx, tenantID, id)
}

// ProjectMembers returns who has access to a project.
func (s *Service) ProjectMembers(ctx context.Context, tenantID, projectID uuid.UUID) ([]store.ProjectMember, error) {
	return s.store.ListProjectMembers(ctx, tenantID, projectID)
}

// GrantProjectRole gives someone a role inside a project.
func (s *Service) GrantProjectRole(ctx context.Context, tenantID, projectID, userID, grantedBy uuid.UUID, role string) error {
	if err := s.store.GrantProjectRole(ctx, tenantID, projectID, userID, grantedBy, role); err != nil {
		return err
	}
	// The grant changes what they may do, so any cached answer from before it
	// has to go rather than linger for the cache TTL.
	s.InvalidateCapabilities(ctx, userID, tenantID)
	return nil
}

// RevokeProjectRole removes someone's access to a project.
func (s *Service) RevokeProjectRole(ctx context.Context, tenantID, projectID, userID uuid.UUID) error {
	if err := s.store.RevokeProjectRole(ctx, tenantID, projectID, userID); err != nil {
		return err
	}
	s.InvalidateCapabilities(ctx, userID, tenantID)
	return nil
}

// Profile returns who the signed-in user is.
func (s *Service) Profile(ctx context.Context, tenantID, userID uuid.UUID) (store.Profile, error) {
	return s.store.UserProfile(ctx, tenantID, userID)
}
