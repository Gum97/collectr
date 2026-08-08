package domain

import (
	"slices"
	"testing"

	"github.com/collectr/collectr/internal/platform/authn"
)

func TestCapabilitiesUnion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		orgRole      string
		projectRoles []string
		wantHas      []string
		wantLacks    []string
	}{
		{
			// The default is nothing. Access arrives through project membership,
			// never by being subtracted from a broad grant.
			name:      "member with no projects sees nothing",
			orgRole:   RoleMember,
			wantLacks: []string{authn.CapFormRead, authn.CapSubmissionRead, authn.CapAnalyticsRead},
		},
		{
			name:         "member of one project",
			orgRole:      RoleMember,
			projectRoles: []string{RoleViewer},
			wantHas:      []string{authn.CapFormRead, authn.CapSubmissionRead},
			wantLacks:    []string{authn.CapFormWrite, authn.CapSubmissionExport, authn.CapAuditRead},
		},
		{
			name:         "roles in two projects combine",
			orgRole:      RoleMember,
			projectRoles: []string{RoleViewer, RoleEditor},
			wantHas:      []string{authn.CapFormWrite, authn.CapFormPublish, authn.CapSubmissionRead},
			wantLacks:    []string{authn.CapSubmissionReadSensitive, authn.CapMemberManage},
		},
		{
			name:    "owner holds everything",
			orgRole: RoleOwner,
			wantHas: []string{
				authn.CapMemberManage, authn.CapAuditRead, authn.CapSubmissionReadSensitive,
				authn.CapSubmissionExport, authn.CapDSRHandle,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := Capabilities(tc.orgRole, tc.projectRoles)
			for _, c := range tc.wantHas {
				if !slices.Contains(got, c) {
					t.Errorf("capabilities %v are missing %q", got, c)
				}
			}
			for _, c := range tc.wantLacks {
				if slices.Contains(got, c) {
					t.Errorf("capabilities %v should not include %q", got, c)
				}
			}
		})
	}
}

// TestDPOOversees pins the separation the role exists for: the data protection
// officer can look across the whole organisation and cannot take anything out of
// it or change it.
func TestDPOOversees(t *testing.T) {
	t.Parallel()

	caps := Capabilities(RoleDPO, nil)

	for _, want := range []string{authn.CapAuditRead, authn.CapDSRHandle, authn.CapSubmissionRead} {
		if !slices.Contains(caps, want) {
			t.Errorf("the DPO needs %q to do the job", want)
		}
	}
	for _, unwanted := range []string{
		authn.CapSubmissionExport,        // oversight is not a reason to hold a copy
		authn.CapSubmissionReadSensitive, // nor to read the most sensitive fields
		authn.CapFormWrite,               // nor to change what is being collected
	} {
		if slices.Contains(caps, unwanted) {
			t.Errorf("the DPO must not hold %q: the role is oversight, not operation", unwanted)
		}
	}
}

// TestAnalystExportsWithoutSeeingSensitive pins the most common real-world
// combination: someone who needs the figures, not the medical histories.
func TestAnalystExportsWithoutSeeingSensitive(t *testing.T) {
	t.Parallel()

	caps := Capabilities(RoleMember, []string{RoleAnalyst})

	if !slices.Contains(caps, authn.CapSubmissionExport) {
		t.Error("an analyst needs to be able to export")
	}
	if slices.Contains(caps, authn.CapSubmissionReadSensitive) {
		t.Error("an analyst must not see sensitive fields; their export is masked")
	}
}

func TestCapabilitiesAreDeterministic(t *testing.T) {
	t.Parallel()

	// The result is cached and compared, so map iteration order must not leak in.
	first := Capabilities(RoleAdmin, []string{RoleManager, RoleViewer})
	for range 50 {
		if got := Capabilities(RoleAdmin, []string{RoleManager, RoleViewer}); !slices.Equal(got, first) {
			t.Fatalf("capability resolution is not deterministic:\n%v\n%v", first, got)
		}
	}
}

func TestRequiresMFA(t *testing.T) {
	t.Parallel()

	tests := []struct {
		role string
		want bool
	}{
		{role: RoleOwner, want: true},
		{role: RoleAdmin, want: true},
		{role: RoleDPO, want: true},
		{role: RoleMember, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.role, func(t *testing.T) {
			t.Parallel()
			if got := RequiresMFA(tc.role); got != tc.want {
				t.Errorf("RequiresMFA(%q) = %v, want %v", tc.role, got, tc.want)
			}
		})
	}
}

func TestRoleValidation(t *testing.T) {
	t.Parallel()

	if ValidOrgRole("superuser") {
		t.Error("ValidOrgRole accepted an unknown role")
	}
	if ValidProjectRole("owner") {
		t.Error("ValidProjectRole accepted an organisation role")
	}
	if !ValidOrgRole(RoleDPO) || !ValidProjectRole(RoleAnalyst) {
		t.Error("a known role was rejected")
	}
}
