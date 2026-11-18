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

// The widget settings change a pinned widget's projection: the preview
// runs the saved query with it, and saving keeps it, so the dashboard
// draws it from then on.
func TestWidgetProjectionPreviewAndSave(t *testing.T) {
	h := newTestApp(t)
	c := &testClient{t: t, h: h, cookies: map[string]*http.Cookie{}}
	c.login("longpassword1")
	const sql = `SELECT '2024-01' AS m, 100 AS v UNION ALL SELECT '2024-02', 200`
	rec := c.do("POST", "/api/v1/widgets/pinned", "application/json",
		`{"title":"Savings","type":"line","data":{},"plan":{"v":1,"sql":"`+sql+`","columns":{"v":"money"}}}`)
	if rec.Code != 200 {
		t.Fatalf("pin: %d %s", rec.Code, rec.Body)
	}
	id := strings.Split(strings.Split(rec.Body.String(), `"id":"`)[1], `"`)[0]
	plan := `{"v":1,"sql":"` + sql + `","columns":{"v":"money"},"project":{"method":"trend","until":"+2m"}}`

	rec = c.do("POST", "/api/v1/widgets/preview", "application/json", `{"type":"line","plan":`+plan+`}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"2024-04"`) || !strings.Contains(rec.Body.String(), `"projected":true`) {
		t.Fatalf("preview: %d %s", rec.Code, rec.Body)
	}
	rec = c.do("POST", "/api/v1/widgets/preview", "application/json",
		`{"type":"line","plan":{"v":1,"sql":"SELECT 'Food' AS c, 1 AS v","columns":{"v":"money"},"project":{}}}`)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "needs dates") {
		t.Errorf("preview of a series without dates: %d %s", rec.Code, rec.Body)
	}

	if rec = c.do("PATCH", "/api/v1/widgets/pinned/"+id, "application/json", `{"plan":`+plan+`}`); rec.Code != 200 {
		t.Fatalf("save: %d %s", rec.Code, rec.Body)
	}
	if rec = c.do("PATCH", "/api/v1/widgets/pinned/"+id, "application/json", `{"plan":{"sql":"nope"}}`); rec.Code != 400 {
		t.Errorf("saving a broken plan: %d", rec.Code)
	}
	rec = c.do("GET", "/api/v1/widgets/pinned", "", "")
	if !strings.Contains(rec.Body.String(), `2024-04`) || !strings.Contains(rec.Body.String(), `projected`) {
		t.Fatalf("dashboard doesn't draw the saved projection: %s", rec.Body)
	}
}
