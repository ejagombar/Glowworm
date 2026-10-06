package server

import (
	"testing"
	"time"

	"glowworm/internal/clock"
)

func TestGroupAuditEntries(t *testing.T) {
	e := func(id int64, action, kind, state, path string) auditEntryView {
		return auditEntryView{ID: id, Action: action, Kind: kind, State: state, PathID: path}
	}
	// Newest first, as the log lists them.
	blocks := groupAuditEntries([]auditEntryView{
		e(9, "category.created", "you", "active", "p2"),
		e(8, "category.created", "you", "active", "p2"),
		e(7, "account.created", "you", "inactive", "main"),
		e(6, "category.created", "you", "inactive", "main"),
		e(5, "category.created", "you", "active", "main"),
		e(4, "category.created", "system", "active", "main"),
		e(3, "account.updated", "you", "active", "main"),
	})
	got := []string{}
	for _, b := range blocks {
		got = append(got, b.Kind)
	}
	want := []string{"repeat", "undone", "single", "single", "single"}
	if len(got) != len(want) {
		t.Fatalf("blocks = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("blocks = %v, want %v", got, want)
		}
	}
	if blocks[1].Count() != 2 || blocks[1].PathID != "main" {
		t.Fatalf("undone block should hold both inactive entries of its path: %+v", blocks[1])
	}
}

func TestAuditWhenUsesInstanceTimezone(t *testing.T) {
	a := &App{Clock: clock.New("Europe/London")}
	now := time.Date(2026, 10, 6, 21, 0, 0, 0, time.UTC)
	if got := a.auditWhen("2026-10-06T20:35:02Z", now); got != "Today, 21:35" {
		t.Fatalf("got %q", got)
	}
	if got := a.auditWhen("2026-03-01T09:00:00Z", now); got != "1 Mar, 09:00" {
		t.Fatalf("got %q", got)
	}
}
