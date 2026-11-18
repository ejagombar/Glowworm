package server

import (
	"strings"
	"testing"
)

// The Savings tab keeps each savings account's balance; current and credit
// accounts record a statement balance from History's Reconciled card, and the
// account pop-up in Settings no longer records balances at all.
func TestBalancesRecordedOnSavingsAndHistory(t *testing.T) {
	a, c := newLockTestApp(t)
	insertAccount(t, a.DB, "Easy Saver", 0, "2024-01-01")
	insertAccount(t, a.DB, "Current", 0, "2024-01-01")
	if _, err := a.DB.Exec(`UPDATE accounts SET type = 'savings' WHERE id = 'Easy Saver'`); err != nil {
		t.Fatal(err)
	}

	panel := func(body, id string) bool { return strings.Contains(body, `data-balance-panel data-account-id="`+id+`"`) }

	savings := c.do("GET", "/savings", "", "").Body.String()
	if !panel(savings, "Easy Saver") {
		t.Error("Savings has no balance panel for the savings account")
	}
	if panel(savings, "Current") {
		t.Error("Savings shows a current account")
	}

	history := c.do("GET", "/history", "", "").Body.String()
	if !strings.Contains(history, `id="reconcileOpen"`) {
		t.Error("History's Reconciled card doesn't open the statement balance pop-up")
	}
	if !panel(history, "Current") {
		t.Error("History can't record a statement balance for the current account")
	}
	if panel(history, "Easy Saver") {
		t.Error("History offers a statement balance for a savings account")
	}

	if settings := c.do("GET", "/settings", "", "").Body.String(); strings.Contains(settings, "data-balance-panel") {
		t.Error("the account pop-up still records balances")
	}
}
