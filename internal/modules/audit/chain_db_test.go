package audit

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/collectr/collectr/internal/contracts"
	"github.com/collectr/collectr/internal/platform/postgres"
)

// These tests need a real database: the chain's guarantees live in the
// interaction between the writer, the advisory lock and the stored rows, and a
// fake would only test the fake.
//
// Run with:
//
//	COLLECTR_TEST_DATABASE_URL=postgres://collectr:...@localhost:5432/collectr go test ./internal/modules/audit/...
func testDB(t *testing.T) *postgres.DB {
	t.Helper()

	url := os.Getenv("COLLECTR_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("COLLECTR_TEST_DATABASE_URL not set")
	}
	db, err := postgres.Open(context.Background(), url)
	if err != nil {
		t.Fatalf("connecting to the test database: %v", err)
	}
	t.Cleanup(db.Close)
	return db
}

// newTenant creates an isolated tenant so parallel runs cannot see each other's
// chains.
func newTenant(t *testing.T, db *postgres.DB) uuid.UUID {
	t.Helper()

	id := uuid.New()
	ctx := context.Background()
	if _, err := db.Exec(ctx,
		`INSERT INTO iam.tenants (id, name, slug) VALUES ($1, $2, $3)`,
		id, "audit test", "audit-test-"+id.String()[:8]); err != nil {
		t.Fatalf("creating test tenant: %v", err)
	}
	t.Cleanup(func() {
		// audit.entries has no cascade from tenants, by design: the trail is not
		// supposed to be easy to remove.
		if _, err := db.Exec(context.Background(), `DELETE FROM audit.entries WHERE tenant_id = $1`, id); err != nil {
			t.Logf("cleaning audit entries: %v", err)
		}
		if _, err := db.Exec(context.Background(), `DELETE FROM iam.tenants WHERE id = $1`, id); err != nil {
			t.Logf("cleaning test tenant: %v", err)
		}
	})
	return id
}

func writeEntries(t *testing.T, db *postgres.DB, w *Writer, tenantID uuid.UUID, n int) {
	t.Helper()

	ctx := context.Background()
	for i := range n {
		err := db.InTx(ctx, func(tx pgx.Tx) error {
			return w.Write(ctx, tx, contracts.AuditEntry{
				TenantID: tenantID,
				Actor:    contracts.AuditActor{Type: "system", ID: "test"},
				Action:   ActionSubmissionCreated,
				Target:   map[string]any{"n": i},
			})
		})
		if err != nil {
			t.Fatalf("writing entry %d: %v", i, err)
		}
	}
}

func TestChainVerifies(t *testing.T) {
	db := testDB(t)
	w := NewWriter(db)
	tenantID := newTenant(t, db)

	writeEntries(t, db, w, tenantID, 5)

	res, err := w.Verify(context.Background(), tenantID)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !res.Valid {
		t.Fatalf("a freshly written chain does not verify: %s (at %d)", res.Reason, res.BrokenAt)
	}
	if res.Entries != 5 {
		t.Errorf("Entries = %d, want 5", res.Entries)
	}
}

// TestVerifyDetectsAlteredEntry is the point of the whole mechanism: an edit made
// directly in the database, by someone with full privileges, still shows up.
func TestVerifyDetectsAlteredEntry(t *testing.T) {
	db := testDB(t)
	w := NewWriter(db)
	tenantID := newTenant(t, db)
	ctx := context.Background()

	writeEntries(t, db, w, tenantID, 5)

	if _, err := db.Exec(ctx,
		`UPDATE audit.entries SET action = $2 WHERE tenant_id = $1 AND seq = 3`,
		tenantID, ActionSubmissionReadAll); err != nil {
		t.Fatalf("tampering with entry: %v", err)
	}

	res, err := w.Verify(ctx, tenantID)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if res.Valid {
		t.Fatal("an altered entry passed verification")
	}
	if res.BrokenAt != 3 {
		t.Errorf("BrokenAt = %d, want 3", res.BrokenAt)
	}
}

// TestVerifyDetectsDeletedEntry covers the other half: removing a line leaves a
// gap that the hashes alone would not reveal.
func TestVerifyDetectsDeletedEntry(t *testing.T) {
	db := testDB(t)
	w := NewWriter(db)
	tenantID := newTenant(t, db)
	ctx := context.Background()

	writeEntries(t, db, w, tenantID, 5)

	if _, err := db.Exec(ctx,
		`DELETE FROM audit.entries WHERE tenant_id = $1 AND seq = 3`, tenantID); err != nil {
		t.Fatalf("deleting entry: %v", err)
	}

	res, err := w.Verify(ctx, tenantID)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if res.Valid {
		t.Fatal("a deleted entry passed verification")
	}
	if res.BrokenAt != 4 {
		t.Errorf("BrokenAt = %d, want 4 (the entry after the gap)", res.BrokenAt)
	}
}

// TestConcurrentWritesDoNotForkTheChain checks the advisory lock. Without it two
// writers read the same head, produce the same seq, and the chain forks -- which
// verification cannot tell apart from tampering.
func TestConcurrentWritesDoNotForkTheChain(t *testing.T) {
	db := testDB(t)
	w := NewWriter(db)
	tenantID := newTenant(t, db)
	ctx := context.Background()

	const writers = 8
	errc := make(chan error, writers)
	for i := range writers {
		go func() {
			errc <- db.InTx(ctx, func(tx pgx.Tx) error {
				return w.Write(ctx, tx, contracts.AuditEntry{
					TenantID: tenantID,
					Actor:    contracts.AuditActor{Type: "system", ID: "concurrent"},
					Action:   ActionSubmissionCreated,
					Target:   map[string]any{"worker": i},
				})
			})
		}()
	}
	for range writers {
		if err := <-errc; err != nil {
			t.Fatalf("concurrent write: %v", err)
		}
	}

	res, err := w.Verify(ctx, tenantID)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !res.Valid {
		t.Fatalf("concurrent writes forked the chain: %s (at %d)", res.Reason, res.BrokenAt)
	}
	if res.Entries != writers {
		t.Errorf("Entries = %d, want %d", res.Entries, writers)
	}
}
