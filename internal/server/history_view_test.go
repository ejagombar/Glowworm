package server

import (
	"fmt"
	"testing"
	"time"

	"glowworm/internal/clock"
	"glowworm/internal/core"
	"glowworm/internal/core/audit"
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

func TestListHistoryReachesOlderEntriesAndKeepsUndoneRunsWhole(t *testing.T) {
	// 300 entries, newest first; 251 down to 41 were undone by one rewind.
	var log []core.AuditEntry
	for id := int64(300); id >= 1; id-- {
		e := core.AuditEntry{ID: id, State: "active", PathID: "main"}
		if id <= 251 && id >= 41 {
			e.State = "inactive"
		}
		log = append(log, e)
	}
	calls := 0
	list := func(opt audit.ListOptions) ([]core.AuditEntry, string, error) {
		calls++
		var out []core.AuditEntry
		for _, e := range log {
			if opt.Cursor > 0 && e.ID >= opt.Cursor {
				continue
			}
			if len(out) == opt.Limit {
				return out, fmt.Sprint(out[len(out)-1].ID), nil
			}
			out = append(out, e)
		}
		return out, "", nil
	}
	for _, c := range []struct {
		show     int
		wantLast int64
		wantMore bool
	}{
		{20, 281, true},  // plain cut
		{50, 41, true},   // the cut falls in the undone run: all of it is listed
		{260, 41, true},  // and again from the middle of it
		{261, 40, true},  // just past it
		{300, 1, false},  // everything
		{1000, 1, false}, // more than there is
	} {
		got, more, err := listHistory(list, audit.ListOptions{}, c.show)
		if err != nil {
			t.Fatal(err)
		}
		if last := got[len(got)-1].ID; last != c.wantLast || more != c.wantMore {
			t.Errorf("show %d: ends at %d (more %v), want %d (more %v)", c.show, last, more, c.wantLast, c.wantMore)
		}
		if got[0].ID != 300 {
			t.Errorf("show %d: starts at %d", c.show, got[0].ID)
		}
	}
}
