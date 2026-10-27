package fx

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"glowworm/internal/store"
)

func TestFrankfurterRatesFillWeekendsAndCache(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("base") != "EUR" || r.URL.Query().Get("symbols") != "GBP" {
			t.Errorf("query = %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"base":"EUR","rates":{"2026-06-05":{"GBP":0.85},"2026-06-08":{"GBP":0.86}}}`))
	}))
	defer srv.Close()
	db, err := store.Open("sqlite", t.TempDir()+"/t.db")
	if err != nil {
		t.Fatal(err)
	}
	f := &Frankfurter{DB: db, BaseURL: srv.URL}
	got, err := f.Rates("eur", "gbp", []string{"2026-06-05", "2026-06-07", "2026-06-08"})
	if err != nil {
		t.Fatal(err)
	}
	if got["2026-06-05"] != 0.85 || got["2026-06-07"] != 0.85 || got["2026-06-08"] != 0.86 {
		t.Fatalf("rates = %v (Sunday should take Friday's rate)", got)
	}
	if _, err := f.Rates("EUR", "GBP", []string{"2026-06-07"}); err != nil || calls != 1 {
		t.Fatalf("second lookup should come from the cache: calls=%d err=%v", calls, err)
	}
}
