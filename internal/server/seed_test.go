package server

import (
	"io"
	"log/slog"
	"testing"

	"glowworm/internal/config"
	"glowworm/internal/crypto"
)

func TestSeedExamplesOnceAsSystem(t *testing.T) {
	db := newSeriesTestDB(t)
	secret, err := crypto.LoadOrCreate("", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := config.Config{AuthMode: "password", Timezone: "UTC", OwnerUsername: "owner", OwnerPassword: "longpassword1"}
	a, err := New(cfg, db, secret, log)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(cfg, db, secret, log); err != nil {
		t.Fatalf("second start must not re-seed or re-create the owner: %v", err)
	}
	count := func(q string) int {
		var n int
		if err := a.DB.QueryRow(q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(`SELECT COUNT(*) FROM categories WHERE is_system = 0`); n != len(exampleCategories) {
		t.Fatalf("example categories = %d, want %d", n, len(exampleCategories))
	}
	if n := count(`SELECT COUNT(*) FROM pinned_widgets`); n == 0 {
		t.Fatal("no example widgets pinned")
	}
	if n := count(`SELECT COUNT(*) FROM audit_entries WHERE actor = 'system' AND action IN ('setup.categories_seeded','setup.widgets_seeded')`); n != 2 {
		t.Fatalf("seed audit entries = %d, want one each for categories and widgets", n)
	}
	if n := count(`SELECT COUNT(*) FROM users`); n != 1 {
		t.Fatalf("owner from config: users = %d", n)
	}
}
