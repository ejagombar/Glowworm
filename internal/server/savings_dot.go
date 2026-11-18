package server

import "context"

// balancesDue is how many savings balances are out of date: the count on
// the Savings tab, which goes down only as they're updated.
func (a *App) balancesDue(ctx context.Context) int {
	b, err := a.book(ctx)
	if err != nil {
		a.Log.Warn("balances due", "err", err)
		return 0
	}
	n := 0
	for _, st := range b.Statuses() {
		if st.Due {
			n++
		}
	}
	return n
}
