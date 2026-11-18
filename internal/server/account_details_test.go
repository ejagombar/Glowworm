package server

import (
	"encoding/json"
	"strings"
	"testing"
)

// The account settings form loads one account, shows its bank details, and
// sends back only what was changed.
func TestTheAccountFormSeesBankDetailsAndKeepsThoseItDoesNotSend(t *testing.T) {
	a, c := newLockTestApp(t)
	rec := c.do("POST", "/api/v1/accounts", "application/json",
		`{"name":"Main","type":"debit","currency":"GBP","account_number":"12345678","sort_code":"11-22-33","iban":"GB00TEST","bic":"TESTGB","notes":"bills"}`)
	if rec.Code != 200 && rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	var made struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &made); err != nil || made.ID == "" {
		t.Fatalf("create returned %s", rec.Body)
	}

	rec = c.do("GET", "/api/v1/accounts/"+made.ID, "", "")
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("get: %d %s", rec.Code, rec.Body)
	}
	for key, want := range map[string]string{"name": "Main", "account_number": "12345678", "sort_code": "11-22-33", "iban": "GB00TEST", "bic": "TESTGB", "notes": "bills"} {
		if got[key] != want {
			t.Errorf("get: %s = %v, want %s", key, got[key], want)
		}
	}
	// Only there: the list of accounts still leaves them out.
	if list := c.do("GET", "/api/v1/accounts", "", "").Body.String(); !strings.Contains(list, made.ID) || strings.Contains(list, "GB00TEST") || strings.Contains(list, "12345678") {
		t.Errorf("the account list gives bank details away, or is missing the account: %s", list)
	}

	stored := func() string {
		t.Helper()
		var d [5]string
		if err := a.DB.QueryRow(`SELECT account_number, sort_code, iban, bic, notes FROM accounts WHERE id = ?`, made.ID).
			Scan(&d[0], &d[1], &d[2], &d[3], &d[4]); err != nil {
			t.Fatal(err)
		}
		return strings.Join(d[:], "|")
	}
	// A rename from the form: no bank details changed, so none are sent.
	if rec := c.do("PATCH", "/api/v1/accounts/"+made.ID, "application/json", `{"name":"Joint","type":"debit","opening_balance_minor":0,"opening_balance_date":""}`); rec.Code != 200 {
		t.Fatalf("rename: %d %s", rec.Code, rec.Body)
	}
	if got, want := stored(), "12345678|11-22-33|GB00TEST|TESTGB|bills"; got != want {
		t.Fatalf("after a rename: %s, want %s", got, want)
	}
	// One changed and one cleared.
	if rec := c.do("PATCH", "/api/v1/accounts/"+made.ID, "application/json", `{"name":"Joint","type":"debit","iban":"GB99NEW","bic":""}`); rec.Code != 200 {
		t.Fatalf("edit: %d %s", rec.Code, rec.Body)
	}
	if got, want := stored(), "12345678|11-22-33|GB99NEW||bills"; got != want {
		t.Fatalf("after changing the IBAN and clearing the BIC: %s, want %s", got, want)
	}
}

// Deleting the account sends the owner straight to making a new one, the
// setup token filled in, and that page takes it.
func TestInstanceDeleteLeadsToSetup(t *testing.T) {
	a, c := newLockTestApp(t)
	a.Cfg.SetupToken = "tok en/1"
	rec := c.do("POST", "/api/v1/instance/delete", "application/json", `{"password":"longpassword1","confirm":"DELETE"}`)
	if rec.Code != 200 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	var out struct {
		Next string `json:"next"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Next != "/setup?token=tok+en%2F1" {
		t.Fatalf("next %q", out.Next)
	}
	if rec := c.do("GET", out.Next, "", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `action="/setup?token=`) {
		t.Errorf("setup page with the token: %d", rec.Code)
	}
	if rec := c.do("GET", "/setup", "", ""); rec.Code != 403 {
		t.Errorf("setup without the token: %d", rec.Code)
	}
	// With no token set, plain /setup.
	a.Cfg.SetupToken = ""
	if got := a.setupURL(); got != "/setup" {
		t.Errorf("no token: %q", got)
	}
}
