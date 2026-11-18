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

	panel := func(body, id string) bool {
		return strings.Contains(body, `data-balance-panel data-account-id="`+id+`"`)
	}

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

// Goals are added, changed and removed through the API, which reports
// their progress, and the Savings tab shows them with each account's
// balance age.
func TestGoalsAndBalanceAge(t *testing.T) {
	a, c := newLockTestApp(t)
	insertAccount(t, a.DB, "ISA", 0, "")
	if _, err := a.DB.Exec(`UPDATE accounts SET type = 'savings' WHERE id = 'ISA'`); err != nil {
		t.Fatal(err)
	}
	old := "2020-01-01"
	if _, err := a.DB.Exec(`INSERT INTO valuations (id, account_id, value_minor, date, source, created_at, updated_at)
		VALUES ('v1', 'ISA', 500000, ?, 'manual', ?, ?)`, old, old, old); err != nil {
		t.Fatal(err)
	}
	rec := c.do("POST", "/api/v1/goals", "application/json", `{"name":"House","target_minor":1000000,"target_date":"2030-01-01","account_ids":["ISA"]}`)
	if rec.Code != 200 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	id := strings.Split(strings.Split(rec.Body.String(), `"id":"`)[1], `"`)[0]
	if rec = c.do("POST", "/api/v1/goals", "application/json", `{"name":"","target_minor":5}`); rec.Code != 400 {
		t.Errorf("a goal with no name: %d", rec.Code)
	}
	list := c.do("GET", "/api/v1/goals", "", "").Body.String()
	if !strings.Contains(list, `"current_minor":500000`) || !strings.Contains(list, `"progress":0.5`) || !strings.Contains(list, `"account_names":["ISA"]`) {
		t.Errorf("list: %s", list)
	}
	page := c.do("GET", "/savings", "", "").Body.String()
	for _, want := range []string{"House", "50%", "years ago", "is-due"} {
		if !strings.Contains(page, want) {
			t.Errorf("Savings page lacks %q", want)
		}
	}
	// Accounts are edited in Settings only.
	if strings.Contains(page, "data-account-form") {
		t.Error("Savings page has a way to edit the account")
	}
	if rec = c.do("PATCH", "/api/v1/goals/"+id, "application/json", `{"name":"Flat","target_minor":500000}`); rec.Code != 200 {
		t.Fatalf("update: %d %s", rec.Code, rec.Body)
	}
	if list = c.do("GET", "/api/v1/goals", "", "").Body.String(); !strings.Contains(list, `"name":"Flat"`) || !strings.Contains(list, `"account_ids":[]`) {
		t.Errorf("after update: %s", list)
	}
	if rec = c.do("DELETE", "/api/v1/goals/"+id, "", ""); rec.Code != 200 {
		t.Fatalf("delete: %d", rec.Code)
	}
	if list = c.do("GET", "/api/v1/goals", "", "").Body.String(); strings.Contains(list, "Flat") {
		t.Errorf("deleted goal still listed: %s", list)
	}
}

// The Savings tab counts the out-of-date savings balances on every page,
// the Savings page too, and the count goes down only as they're updated.
func TestSavingsTabCount(t *testing.T) {
	a, c := newLockTestApp(t)
	insertAccount(t, a.DB, "ISA", 0, "")
	insertAccount(t, a.DB, "Pension", 0, "")
	insertAccount(t, a.DB, "Current", 0, "")
	if _, err := a.DB.Exec(`UPDATE accounts SET type = 'savings' WHERE id IN ('ISA', 'Pension')`); err != nil {
		t.Fatal(err)
	}
	count := func(path string) string {
		t.Helper()
		body := c.do("GET", path, "", "").Body.String()
		_, rest, ok := strings.Cut(body, `data-balances-due="`)
		if !ok {
			return ""
		}
		n, _, _ := strings.Cut(rest, `"`)
		return n
	}
	if got := count("/transactions"); got != "2" {
		t.Fatalf("count %q, want 2 (the current account is never due)", got)
	}
	if got := count("/savings"); got != "2" {
		t.Errorf("opening Savings changed the count to %q", got)
	}
	if got := count("/transactions"); got != "2" {
		t.Errorf("count %q after opening Savings", got)
	}
	today := a.Clock.Today()
	if _, err := a.DB.Exec(`INSERT INTO valuations (id, account_id, value_minor, date, source, created_at, updated_at)
		VALUES ('v1', 'ISA', 1000, ?, 'manual', ?, ?)`, today, today, today); err != nil {
		t.Fatal(err)
	}
	if got := count("/transactions"); got != "1" {
		t.Errorf("count %q after updating one balance, want 1", got)
	}
	if _, err := a.DB.Exec(`INSERT INTO valuations (id, account_id, value_minor, date, source, created_at, updated_at)
		VALUES ('v2', 'Pension', 1000, ?, 'manual', ?, ?)`, today, today, today); err != nil {
		t.Fatal(err)
	}
	if got := count("/transactions"); got != "" {
		t.Errorf("count %q with everything up to date", got)
	}
}
