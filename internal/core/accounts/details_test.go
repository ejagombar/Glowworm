package accounts

import "testing"

func strp(s string) *string { return &s }

// details describes an account's bank details and notes as stored.
func (f *fixture) details(t *testing.T, id string) [5]string {
	t.Helper()
	var d [5]string
	if err := f.s.DB.QueryRow(`SELECT account_number, sort_code, iban, bic, notes FROM accounts WHERE id = ?`, id).
		Scan(&d[0], &d[1], &d[2], &d[3], &d[4]); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestAnEditKeepsTheBankDetailsItDoesNotSend(t *testing.T) {
	f := newFixture(t)
	a, err := f.s.Create(Input{Name: "Joint", Type: "debit", Currency: "GBP",
		AccountNumber: strp(" 12345678 "), SortCode: strp("11-22-33"), IBAN: strp("GB00TEST"), BIC: strp("TESTGB"), Notes: strp("bills")})
	if err != nil {
		t.Fatal(err)
	}
	full := [5]string{"12345678", "11-22-33", "GB00TEST", "TESTGB", "bills"}
	if got := f.details(t, a.ID); got != full {
		t.Fatalf("created with %q, want %q", got, full)
	}

	// A rename that sends none of them leaves them all as they were.
	if _, err := f.s.Update(a.ID, Input{Name: "Household", Type: "debit"}); err != nil {
		t.Fatal(err)
	}
	if got := f.details(t, a.ID); got != full {
		t.Fatalf("after a rename: %q, want %q", got, full)
	}

	// One that is sent changes, and only that one.
	if _, err := f.s.Update(a.ID, Input{Name: "Household", Type: "debit", IBAN: strp("GB99NEW")}); err != nil {
		t.Fatal(err)
	}
	if got, want := f.details(t, a.ID), [5]string{"12345678", "11-22-33", "GB99NEW", "TESTGB", "bills"}; got != want {
		t.Fatalf("after changing the IBAN: %q, want %q", got, want)
	}

	// Sent empty, it is cleared.
	if _, err := f.s.Update(a.ID, Input{Name: "Household", Type: "debit", SortCode: strp("")}); err != nil {
		t.Fatal(err)
	}
	if got, want := f.details(t, a.ID), [5]string{"12345678", "", "GB99NEW", "TESTGB", "bills"}; got != want {
		t.Fatalf("after clearing the sort code: %q, want %q", got, want)
	}
}
