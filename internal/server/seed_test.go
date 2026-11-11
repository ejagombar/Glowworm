package server

import (
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

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

// The example widgets are saved queries the dashboard runs itself, so they
// show whatever is imported later; Top merchants links each row to its
// transactions.
func TestExampleWidgetsRun(t *testing.T) {
	a, _ := newLockTestApp(t)
	insertAccount(t, a.DB, "main", 0, nil)
	insertTx(t, a.DB, "t1", "main", time.Now().UTC().Format("2006-01-02"), -2500)
	list, err := a.WidgetsList("all", "", "")
	if err != nil {
		t.Fatal(err)
	}
	byTitle := map[string]*WidgetView{}
	for _, w := range list {
		if w.Stale {
			t.Errorf("%s: its query failed", w.Title)
		}
		byTitle[w.Title] = w
	}
	for _, title := range []string{"Spending by category — this month", "Spending mix — this month"} {
		w := byTitle[title]
		if w == nil || !strings.Contains(w.DataJSON, `"labels":["Uncategorised"]`) || !strings.Contains(w.DataJSON, "2500") {
			t.Errorf("%s = %+v", title, w)
		}
	}
	if w := byTitle["Savings rate"]; w == nil || w.NumberValue != "—" {
		t.Errorf("savings rate with no income = %+v", w)
	}
	top := byTitle["Top merchants"]
	if top == nil || len(top.TableRows) != 1 || strings.Join(top.TableRows[0], "|") != "tx|£25.00" {
		t.Fatalf("top merchants = %+v", top)
	}
	if len(top.TableLinks) != 1 || top.TableLinks[0] != "/transactions?q=tx&type=expense" {
		t.Errorf("top merchants links = %q", top.TableLinks)
	}
}
