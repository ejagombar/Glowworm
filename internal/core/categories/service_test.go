package categories_test

import (
	"strings"
	"testing"

	"github.com/ejagombar/Glowworm/internal/core/audit"
	"github.com/ejagombar/Glowworm/internal/core/auditexec"
	"github.com/ejagombar/Glowworm/internal/core/categories"
	"github.com/ejagombar/Glowworm/internal/store"
	"github.com/ejagombar/Glowworm/internal/store/repos"
)

func strp(s string) *string { return &s }

func TestDescriptionKeptClearedAndUndone(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	w := &audit.Writer{DB: db}
	cats := &repos.Categories{DB: db}
	s := &categories.Service{DB: db, Repos: cats, Txs: &repos.Transactions{DB: db}, Audit: w}
	description := func(id string) string {
		t.Helper()
		c, err := cats.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		return c.Description
	}

	c, err := s.Create(categories.Input{Name: "Groceries", Description: strp("  Supermarkets, not restaurants \n")})
	if err != nil {
		t.Fatal(err)
	}
	if got := description(c.ID); got != "Supermarkets, not restaurants" {
		t.Fatalf("created with description %q", got)
	}

	// Leaving the field out of an update keeps the description.
	if _, err := s.Update(c.ID, categories.Input{Name: "Food shopping"}); err != nil {
		t.Fatal(err)
	}
	if got := description(c.ID); got != "Supermarkets, not restaurants" {
		t.Fatalf("update without a description changed it to %q", got)
	}

	if _, err := s.Update(c.ID, categories.Input{Name: "Food shopping", Description: strp(strings.Repeat("x", 501))}); err == nil {
		t.Fatal("a 501-character description was accepted")
	}

	if _, err := s.Update(c.ID, categories.Input{Name: "Food shopping", Description: strp("")}); err != nil {
		t.Fatal(err)
	}
	if got := description(c.ID); got != "" {
		t.Fatalf("clearing left %q", got)
	}

	// Undoing the clear brings the description back.
	var entry int64
	if err := db.QueryRow(`SELECT MAX(id) FROM audit_entries WHERE action = 'category.updated'`).Scan(&entry); err != nil {
		t.Fatal(err)
	}
	x := &auditexec.Executor{DB: db, Categories: cats}
	if _, err := audit.NewEngine(db, w).Undo(entry, x.Apply); err != nil {
		t.Fatal(err)
	}
	if got := description(c.ID); got != "Supermarkets, not restaurants" {
		t.Fatalf("undo restored %q", got)
	}
}
