package server

import (
	"net/http"
	"strings"
	"testing"
)

// Only widgets the dashboard can re-run can be pinned: a chart without its
// saved query would freeze at the moment it was pinned and quietly drift
// from the data.
func TestPinRequiresASavedQuery(t *testing.T) {
	h := newTestApp(t)
	c := &testClient{t: t, h: h, cookies: map[string]*http.Cookie{}}
	c.login("longpassword1")
	pin := func(body string) int {
		t.Helper()
		return c.do("POST", "/api/v1/widgets/pinned", "application/json", body).Code
	}
	snapshot := `{"title":"Spend","type":"bar","data":{"labels":["2026-01"],"series":[{"name":"Spend","values":[100]}]}}`
	if rec := c.do("POST", "/api/v1/widgets/pinned", "application/json", snapshot); rec.Code != 400 || !strings.Contains(rec.Body.String(), "no saved query") {
		t.Errorf("pinning a chart with no query: %d %s", rec.Code, rec.Body)
	}
	live := `{"title":"Spend","type":"bar","data":{},"plan":{"v":1,"sql":"SELECT 'a' AS label, 1 AS n"}}`
	if code := pin(live); code != 200 {
		t.Errorf("pinning a chart with a saved query: %d", code)
	}
	if code := pin(`{"title":"Note","type":"text","data":{"body":"Hi"}}`); code != 200 {
		t.Errorf("pinning text: %d", code)
	}
}
