package ledger

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
