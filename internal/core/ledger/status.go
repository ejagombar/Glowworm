package ledger

import "math"

// Status is how up to date an account's recorded balance is.
type Status struct {
	Account *Account
	Balance int64
	// LastValued is the latest recorded balance's date ("" for none), and
	// DaysSince how long ago that was (-1 for none).
	LastValued string
	DaysSince  int
	// ReminderDays is the interval that applies (the setting, for savings
	// accounts), 0 for none. Due says the balance is older than that, or
	// was never recorded.
	ReminderDays int
	Due          bool
}

// Status works out the account's status as of the book's day.
func (b *Book) Status(a *Account) Status {
	s := Status{Account: a, Balance: a.BalanceOn(b.Today), LastValued: a.lastValue, DaysSince: -1}
	if a.lastValue != "" {
		s.DaysSince = daysBetween(a.lastValue, b.Today)
	}
	if a.Type == "savings" {
		s.ReminderDays = b.ReminderDays
	}
	if s.ReminderDays > 0 {
		s.Due = s.DaysSince < 0 || s.DaysSince >= s.ReminderDays
	}
	return s
}

// Statuses are every account's status, in the book's order.
func (b *Book) Statuses() []Status {
	out := make([]Status, 0, len(b.Accounts))
	for _, a := range b.Accounts {
		out = append(out, b.Status(a))
	}
	return out
}

// GoalProgress is how far a goal has got, and how it's going.
type GoalProgress struct {
	Goal     *Goal
	Accounts []*Account
	Current  int64
	// Remaining is what's left to reach the target (0 once reached), and
	// Progress Current over the target (can pass 1).
	Remaining int64
	Progress  float64
	// Pace is the average change per month of the goal's accounts over the
	// last PaceMonths months (or since they started, if sooner).
	Pace float64
	// MonthsLeft until the target date (-1 with no date); MonthlyNeeded is
	// what each of those months must add to get there.
	MonthsLeft    int
	MonthlyNeeded int64
	// Projected is the month the target is reached at Pace ("" when the
	// pace never gets there; today once reached). OnTrack says Projected is
	// no later than the target date; nil with no date.
	Projected string
	OnTrack   *bool
}

// PaceMonths is the window a goal's pace is measured over.
const PaceMonths = 6

// Progress works out every goal's progress.
func (b *Book) Progress() []GoalProgress {
	var out []GoalProgress
	for _, g := range b.Goals {
		out = append(out, b.GoalProgress(g))
	}
	return out
}

// GoalProgress works out one goal's progress.
func (b *Book) GoalProgress(g *Goal) GoalProgress {
	p := GoalProgress{Goal: g, MonthsLeft: -1}
	if len(g.AccountIDs) == 0 {
		for _, a := range b.Accounts {
			if a.Type == "savings" {
				p.Accounts = append(p.Accounts, a)
			}
		}
	} else {
		for _, id := range g.AccountIDs {
			if a := b.byID[id]; a != nil {
				p.Accounts = append(p.Accounts, a)
			}
		}
	}
	since := addMonths(b.Today, -PaceMonths)
	months := float64(PaceMonths)
	start := ""
	var then int64
	for _, a := range p.Accounts {
		p.Current += a.BalanceOn(b.Today)
		if s := a.Start(); s != "" && (start == "" || s < start) {
			start = s
		}
		then += a.BalanceOn(since)
	}
	if start != "" && start > since {
		// Younger than the window: measure from the start.
		then = 0
		for _, a := range p.Accounts {
			then += a.Opening
		}
		months = math.Max(float64(daysBetween(start, b.Today))/30.44, 1)
	}
	if start != "" {
		p.Pace = float64(p.Current-then) / months
	}
	if g.TargetMinor > 0 {
		p.Progress = float64(p.Current) / float64(g.TargetMinor)
	}
	p.Remaining = max(g.TargetMinor-p.Current, 0)
	if g.TargetDate != "" {
		p.MonthsLeft = max(int(math.Ceil(float64(daysBetween(b.Today, g.TargetDate))/30.44)), 0)
		if p.MonthsLeft > 0 {
			p.MonthlyNeeded = int64(math.Ceil(float64(p.Remaining) / float64(p.MonthsLeft)))
		} else {
			p.MonthlyNeeded = p.Remaining
		}
	}
	switch {
	case p.Remaining == 0:
		p.Projected = b.Today[:7]
	case p.Pace > 0:
		n := int(math.Ceil(float64(p.Remaining) / p.Pace))
		if n <= 1200 {
			p.Projected = addMonths(b.Today, n)[:7]
		}
	}
	if g.TargetDate != "" {
		on := p.Projected != "" && p.Projected <= g.TargetDate[:min(7, len(g.TargetDate))]
		p.OnTrack = &on
	}
	return p
}
