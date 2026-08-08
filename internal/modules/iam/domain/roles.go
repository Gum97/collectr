// Package domain holds the role and capability model.
//
// Handlers check capabilities, never role names. Roles are only a way to package
// capabilities, and keeping the two apart is what lets "may read submissions"
// stay separate from "may read the sensitive fields inside them" and from "may
// export them in bulk" -- three permissions with very different consequences.
package domain

import (
	"errors"
	"slices"

	"github.com/collectr/collectr/internal/platform/authn"
)

// Organisation roles.
const (
	RoleOwner  = "owner"
	RoleAdmin  = "admin"
	RoleMember = "member"
	// RoleDPO is the data protection officer: sees across every project, and
	// changes nothing. Compliance needs a view of the whole organisation, but the
	// person holding it should not also be able to edit or extract the data they
	// are supposed to be watching over.
	RoleDPO = "dpo"
)

// Project roles.
const (
	RoleManager = "manager"
	RoleEditor  = "editor"
	RoleAnalyst = "analyst"
	RoleViewer  = "viewer"
)

// Errors returned by the IAM module.
var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrMFARequired        = errors.New("multi-factor authentication required")
	ErrMFAInvalid         = errors.New("invalid verification code")
	ErrSessionInvalid     = errors.New("session is invalid or expired")
	ErrRateLimited        = errors.New("too many attempts")
	ErrNotFound           = errors.New("not found")
	ErrLastOwner          = errors.New("an organisation must keep at least one owner")
	ErrAlreadyMember      = errors.New("this person is already a member")
	// ErrInvalidInput marks a caller mistake worth reporting back verbatim, as
	// opposed to an internal failure that must not leak its wording.
	ErrInvalidInput = errors.New("invalid input")
)

// orgCapabilities maps an organisation role to what it grants everywhere.
var orgCapabilities = map[string][]string{
	RoleOwner: {
		authn.CapLinkRead, authn.CapLinkWrite, authn.CapLinkDelete,
		authn.CapFormRead, authn.CapFormWrite, authn.CapFormPublish,
		authn.CapSubmissionRead, authn.CapSubmissionReadSensitive,
		authn.CapSubmissionExport, authn.CapAnalyticsRead,
		authn.CapConsentManage, authn.CapDSRHandle, authn.CapAuditRead,
		authn.CapAPIKeyManage, authn.CapWebhookManage, authn.CapMemberManage,
	},
	RoleAdmin: {
		authn.CapLinkRead, authn.CapLinkWrite, authn.CapLinkDelete,
		authn.CapFormRead, authn.CapFormWrite, authn.CapFormPublish,
		authn.CapSubmissionRead, authn.CapSubmissionReadSensitive,
		authn.CapSubmissionExport, authn.CapAnalyticsRead,
		authn.CapConsentManage, authn.CapDSRHandle, authn.CapAuditRead,
		authn.CapAPIKeyManage, authn.CapWebhookManage, authn.CapMemberManage,
	},
	// A plain member holds nothing at the organisation level. Access arrives only
	// through project membership: the default is to see nothing, not to see
	// everything and have permissions subtracted.
	RoleMember: {},
	RoleDPO: {
		authn.CapAuditRead, authn.CapDSRHandle, authn.CapConsentManage,
		authn.CapSubmissionRead, authn.CapFormRead, authn.CapAnalyticsRead,
		// Deliberately absent: SubmissionExport, SubmissionReadSensitive,
		// FormWrite. Oversight, not operation.
	},
}

// projectCapabilities maps a project role to what it grants inside that project.
var projectCapabilities = map[string][]string{
	RoleManager: {
		authn.CapLinkRead, authn.CapLinkWrite, authn.CapLinkDelete,
		authn.CapFormRead, authn.CapFormWrite, authn.CapFormPublish,
		authn.CapSubmissionRead, authn.CapSubmissionReadSensitive,
		authn.CapSubmissionExport, authn.CapAnalyticsRead,
		authn.CapAPIKeyManage, authn.CapWebhookManage,
	},
	RoleEditor: {
		authn.CapLinkRead, authn.CapLinkWrite,
		authn.CapFormRead, authn.CapFormWrite, authn.CapFormPublish,
		authn.CapSubmissionRead, authn.CapAnalyticsRead,
	},
	// An analyst may take the data out but never sees the sensitive fields: the
	// export they produce is masked. This is the most common real combination --
	// someone who needs the numbers, not the medical histories.
	RoleAnalyst: {
		authn.CapLinkRead, authn.CapFormRead,
		authn.CapSubmissionRead, authn.CapSubmissionExport, authn.CapAnalyticsRead,
	},
	RoleViewer: {
		authn.CapLinkRead, authn.CapFormRead,
		authn.CapSubmissionRead, authn.CapAnalyticsRead,
	},
}

// MFARequiredRoles are the organisation roles that must use a second factor.
// They are the roles that can reach personal data across the whole organisation.
var MFARequiredRoles = []string{RoleOwner, RoleAdmin, RoleDPO}

// RequiresMFA reports whether an organisation role must have MFA enabled.
func RequiresMFA(orgRole string) bool {
	return slices.Contains(MFARequiredRoles, orgRole)
}

// ValidOrgRole reports whether r is an organisation role.
func ValidOrgRole(r string) bool {
	_, ok := orgCapabilities[r]
	return ok
}

// ValidProjectRole reports whether r is a project role.
func ValidProjectRole(r string) bool {
	_, ok := projectCapabilities[r]
	return ok
}

// Capabilities resolves the effective permission set for one membership.
//
// The result is the union of the organisation role and every project role held.
// There are no deny rules: mixing allow and deny creates precedence puzzles that
// nobody can reason about at the moment they matter.
func Capabilities(orgRole string, projectRoles []string) []string {
	seen := make(map[string]struct{}, 16)
	for _, c := range orgCapabilities[orgRole] {
		seen[c] = struct{}{}
	}
	for _, pr := range projectRoles {
		for _, c := range projectCapabilities[pr] {
			seen[c] = struct{}{}
		}
	}

	out := make([]string, 0, len(seen))
	for c := range seen {
		out = append(out, c)
	}
	// Sorted so the cached value is byte-stable and diffable.
	slices.Sort(out)
	return out
}

// OrgCapabilities returns what an organisation role grants on its own.
func OrgCapabilities(role string) []string {
	return slices.Clone(orgCapabilities[role])
}

// ProjectCapabilities returns what a project role grants.
func ProjectCapabilities(role string) []string {
	return slices.Clone(projectCapabilities[role])
}
