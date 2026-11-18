package server

import (
	"context"
	"net/http"

	"github.com/ejagombar/Glowworm/internal/core/goals"
	"github.com/ejagombar/Glowworm/internal/core/ledger"
)

// book is the ledger as of today, shared with widget queries' cache.
func (a *App) book(ctx context.Context) (*ledger.Book, error) {
	return ledger.Cached(ctx, a.DB.DB, a.DB.DB, a.Clock.Today())
}

// goalView is a goal with how it's going.
type goalView struct {
	*goals.Goal
	CurrentMinor  int64    `json:"current_minor"`
	Progress      float64  `json:"progress"`
	RemainingMin  int64    `json:"remaining_minor"`
	PaceMinor     int64    `json:"pace_minor"`
	MonthsLeft    int      `json:"months_left"`
	MonthlyNeeded int64    `json:"monthly_needed_minor"`
	Projected     string   `json:"projected_month"`
	OnTrack       *bool    `json:"on_track"`
	AccountNames  []string `json:"account_names"`
	// Track is "on" or "behind" for a goal with a date, else "".
	Track string `json:"track"`
}

func (a *App) goalViews(ctx context.Context) ([]goalView, error) {
	list, err := a.Goals.List()
	if err != nil {
		return nil, err
	}
	b, err := a.book(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]goalView, 0, len(list))
	for _, g := range list {
		p := b.GoalProgress(&ledger.Goal{ID: g.ID, Name: g.Name, Color: g.Color, TargetDate: g.TargetDate,
			TargetMinor: g.TargetMinor, AccountIDs: g.AccountIDs})
		v := goalView{Goal: g, CurrentMinor: p.Current, Progress: p.Progress, RemainingMin: p.Remaining,
			PaceMinor: int64(p.Pace), MonthsLeft: p.MonthsLeft, MonthlyNeeded: p.MonthlyNeeded,
			Projected: p.Projected, OnTrack: p.OnTrack, AccountNames: []string{}}
		if p.OnTrack != nil {
			v.Track = "behind"
			if *p.OnTrack {
				v.Track = "on"
			}
		}
		for _, acc := range p.Accounts {
			v.AccountNames = append(v.AccountNames, acc.Name)
		}
		out = append(out, v)
	}
	return out, nil
}

func (a *App) apiGoalsList(w http.ResponseWriter, r *http.Request) error {
	list, err := a.goalViews(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, map[string]any{"items": list})
	return nil
}

func (a *App) apiGoalCreate(w http.ResponseWriter, r *http.Request) error {
	var in goals.Input
	if err := decodeBody(r, &in); err != nil {
		return err
	}
	g, err := a.Goals.Create(in)
	if err != nil {
		return err
	}
	writeJSON(w, g)
	return nil
}

func (a *App) apiGoalUpdate(w http.ResponseWriter, r *http.Request) error {
	var in goals.Input
	if err := decodeBody(r, &in); err != nil {
		return err
	}
	g, err := a.Goals.Update(r.PathValue("id"), in)
	if err != nil {
		return err
	}
	writeJSON(w, g)
	return nil
}

func (a *App) apiGoalDelete(w http.ResponseWriter, r *http.Request) error {
	if err := a.Goals.Delete(r.PathValue("id")); err != nil {
		return err
	}
	writeJSON(w, map[string]any{"ok": true})
	return nil
}
