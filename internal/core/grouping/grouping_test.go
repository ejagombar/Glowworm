package grouping

import (
	"fmt"
	"testing"

	"github.com/ejagombar/Glowworm/internal/core/audit"
	"github.com/ejagombar/Glowworm/internal/store"
)

func newService(t *testing.T) *Service {
	t.Helper()
	db, err := store.Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`INSERT INTO accounts (id, name, type, color, currency, created_at, updated_at)
		VALUES ('a1','Main','debit','#000','GBP','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	for i, d := range []string{
		"To Jaeren Rodrigues - rent and water", "Jaeren Rodrigues - rent and water",
		"To Jaeren Rodrigues - Rent and water", "To Jaeren Rodrigues - otter chaos",
		"Lime", "Lime - LimePass", "Limestone Café",
	} {
		if _, err := db.Exec(`INSERT INTO transactions (id, account_id, date, description, amount_minor, currency, type, dedupe_hash, created_at, updated_at)
			VALUES (?,?,'2026-03-01',?,-100,'GBP','expense','h','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
			fmt.Sprintf("t%d", i), "a1", d); err != nil {
			t.Fatal(err)
		}
	}
	return &Service{DB: db, Audit: &audit.Writer{DB: db}}
}

func groups(t *testing.T, s *Service) map[string]string {
	t.Helper()
	rows, err := s.DB.Query(`SELECT description, COALESCE(merchant, '') FROM transactions`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]string{}
	for rows.Next() {
		var d, m string
		if err := rows.Scan(&d, &m); err != nil {
			t.Fatal(err)
		}
		out[d] = m
	}
	return out
}

func TestRulesGroupOnlyWhatTheyMatch(t *testing.T) {
	s := newService(t)
	rent := Draft{Group: "Rent and water", Rules: []DraftRule{{Match: "contains", Text: "rent and water"}}}
	p, err := s.Preview(rent)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Matched) != 3 || p.Created {
		t.Fatalf("preview = %+v", p)
	}
	if _, err := s.Create(rent); err != nil {
		t.Fatal(err)
	}
	lime := Draft{Group: "Lime", Rules: []DraftRule{{Match: "is", Text: "Lime"}, {Match: "is", Text: "Lime - LimePass"}}}
	if _, err := s.Create(lime); err != nil {
		t.Fatal(err)
	}
	g := groups(t, s)
	for d, want := range map[string]string{
		"To Jaeren Rodrigues - rent and water": "Rent and water",
		"Jaeren Rodrigues - rent and water":    "Rent and water",
		"To Jaeren Rodrigues - Rent and water": "Rent and water",
		"To Jaeren Rodrigues - otter chaos":    "",
		"Lime":                                 "Lime",
		"Lime - LimePass":                      "Lime",
		"Limestone Café":                       "",
	} {
		if g[d] != want {
			t.Errorf("%q in group %q, want %q", d, g[d], want)
		}
	}

	// The same rule again is a no-op; under another name it's refused.
	if created, err := s.Create(rent); err != nil || len(created) != 0 {
		t.Errorf("repeat create: %v, %v", created, err)
	}
	if _, err := s.Create(Draft{Group: "Housing", Rules: rent.Rules}); err == nil {
		t.Error("a rule already used by another group was accepted")
	}
	if _, err := s.Preview(Draft{Group: "x", Rules: []DraftRule{{Match: "contains", Text: "to"}}}); err == nil {
		t.Error("a two-letter contains rule was accepted")
	}

	// Deleting a group's last rule ungroups its transactions.
	rules, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 3 || rules[2].Group != "Rent and water" || rules[2].Count != 3 {
		t.Fatalf("list = %+v", rules)
	}
	if err := s.Delete(rules[2].ID); err != nil {
		t.Fatal(err)
	}
	if g := groups(t, s); g["Jaeren Rodrigues - rent and water"] != "" {
		t.Error("deleting the rule left its transactions grouped")
	}
}

// A more specific rule wins: "is" before "contains", then longer text.
func TestRulePrecedence(t *testing.T) {
	rules := []*Rule{
		{Group: "Rides", Match: "contains", Text: "lime"},
		{Group: "LimePass", Match: "is", Text: "Lime - LimePass"},
	}
	s := newService(t)
	for _, r := range rules {
		if _, err := s.Create(Draft{Group: r.Group, Rules: []DraftRule{{Match: r.Match, Text: r.Text}}}); err != nil {
			t.Fatal(err)
		}
	}
	g := groups(t, s)
	if g["Lime - LimePass"] != "LimePass" || g["Lime"] != "Rides" || g["Limestone Café"] != "" {
		t.Errorf("groups = %v", g)
	}
}
