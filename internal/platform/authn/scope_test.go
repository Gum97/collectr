package authn

import (
	"testing"

	"github.com/google/uuid"
)

// TestInProjectHonoursTheGrant pins the finding two independent audits reached:
// every signed-in user passed every InProject check, because the resolver never
// carried the grants and the method treated "no pinned project" as "all
// projects". Ten call sites across the codebase were dead code.
func TestInProjectHonoursTheGrant(t *testing.T) {
	t.Parallel()

	granted, other := uuid.New(), uuid.New()

	t.Run("member reaches only what was granted", func(t *testing.T) {
		a := NewActor(KindUser, uuid.New(), uuid.New(), nil, []string{CapFormRead}).
			WithProjectScope(false, []uuid.UUID{granted})
		if !a.InProject(granted) {
			t.Error("granted project refused")
		}
		if a.InProject(other) {
			t.Error("ungranted project allowed — this is the bug")
		}
	})

	t.Run("organisation role spans every project", func(t *testing.T) {
		a := NewActor(KindUser, uuid.New(), uuid.New(), nil, []string{CapMemberManage}).
			WithProjectScope(true, nil)
		if !a.InProject(other) {
			t.Error("owner refused a project they administer")
		}
	})

	t.Run("member with no grant reaches nothing", func(t *testing.T) {
		a := NewActor(KindUser, uuid.New(), uuid.New(), nil, nil).
			WithProjectScope(false, nil)
		if a.InProject(granted) {
			t.Error("a member with no grants reached a project")
		}
	})

	t.Run("api key stays pinned to its own project", func(t *testing.T) {
		pinned := granted
		a := NewActor(KindAPIKey, uuid.Nil, uuid.New(), &pinned, []string{CapFormRead}).
			// Even if a caller hands it an organisation-wide scope, the pin wins.
			WithProjectScope(true, []uuid.UUID{other})
		if !a.InProject(pinned) {
			t.Error("key refused its own project")
		}
		if a.InProject(other) {
			t.Error("pinned key reached beyond its project")
		}
	})
}
