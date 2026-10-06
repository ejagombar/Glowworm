package server

import (
	"fmt"
	"time"

	"glowworm/internal/core"
)

type auditEntryView struct {
	ID          int64
	Timestamp   string
	When        string
	Actor       string
	Kind        string
	Title       string
	Sub         string
	DiffLines   []core.DiffLine
	State       string
	PathID      string
	Action      string
	Revertible  bool
	Jumpable    bool
	Forgettable bool
	Deletable   bool
}

// auditBlock is one row of the audit log: a single entry, a run of the same
// action repeated back to back, or a run of entries undone by a restore.
type auditBlock struct {
	Kind    string // "single" | "repeat" | "undone"
	Entries []auditEntryView
	PathID  string
	When    string
}

func (b auditBlock) First() auditEntryView { return b.Entries[0] }
func (b auditBlock) Count() int            { return len(b.Entries) }

func (a *App) toEntryViews(entries []core.AuditEntry) []auditEntryView {
	now := time.Now()
	out := make([]auditEntryView, 0, len(entries))
	for _, e := range entries {
		kind := "you"
		switch {
		case e.Actor == core.ActorSystem && len(e.Action) >= 6 && e.Action[:6] == "import":
			kind = "import"
		case e.Actor != core.ActorUser:
			kind = "system"
		}
		out = append(out, auditEntryView{
			ID: e.ID, Timestamp: e.Timestamp, When: a.auditWhen(e.Timestamp, now),
			Actor: actorLabel(e.Actor), Kind: kind,
			Title: e.Title, Sub: e.Sub, DiffLines: e.DiffLines, State: e.State,
			PathID: e.PathID, Action: e.Action,
		})
	}
	return out
}

func actorLabel(actor string) string {
	if actor == core.ActorUser {
		return "You"
	}
	return "System"
}

// auditWhen shows stored UTC timestamps in the instance's timezone.
func (a *App) auditWhen(ts string, now time.Time) string {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return ts
	}
	loc := time.Local
	if a.Clock != nil {
		loc = a.Clock.Location()
	}
	t, now = t.In(loc), now.In(loc)
	day := func(x time.Time) string { return x.Format("2006-01-02") }
	switch {
	case day(t) == day(now):
		return "Today, " + t.Format("15:04")
	case day(t) == day(now.AddDate(0, 0, -1)):
		return "Yesterday, " + t.Format("15:04")
	case t.Year() == now.Year():
		return t.Format("2 Jan, 15:04")
	default:
		return t.Format("2 Jan 2006, 15:04")
	}
}

// groupAuditEntries folds newest-first entries into blocks: consecutive
// undone entries from one path collapse together, and so do consecutive
// active entries of the same action by the same actor.
func groupAuditEntries(views []auditEntryView) []auditBlock {
	var blocks []auditBlock
	for _, v := range views {
		if n := len(blocks); n > 0 {
			last := &blocks[n-1]
			prev := last.Entries[len(last.Entries)-1]
			switch {
			case v.State == "inactive" && last.Kind == "undone" && last.PathID == v.PathID:
				last.Entries = append(last.Entries, v)
				continue
			case v.State != "inactive" && prev.State != "inactive" &&
				v.Action == prev.Action && v.Kind == prev.Kind && last.Kind != "undone":
				last.Kind = "repeat"
				last.Entries = append(last.Entries, v)
				continue
			}
		}
		kind := "single"
		if v.State == "inactive" {
			kind = "undone"
		}
		blocks = append(blocks, auditBlock{Kind: kind, Entries: []auditEntryView{v}, PathID: v.PathID})
	}
	for i := range blocks {
		b := &blocks[i]
		if len(b.Entries) == 1 {
			b.When = b.Entries[0].When
			continue
		}
		newest, oldest := b.Entries[0].When, b.Entries[len(b.Entries)-1].When
		b.When = newest
		if newest != oldest {
			b.When = fmt.Sprintf("%s – %s", oldest, newest)
		}
	}
	return blocks
}
