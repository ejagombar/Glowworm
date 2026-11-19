package query

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTargetsProgressAndRateSQL(t *testing.T) {
	svc := seedPeriodsData(t)
	ten := 100000.0
	sp := &Spec{V: 1,
		SQL:     `SELECT period, closing_minor FROM account_periods WHERE grain = 'month' AND account_id = 'sav' ORDER BY period`,
		Columns: map[string]SpecColumn{"closing_minor": {Label: "ISA", Format: "money"}},
		Targets: []SpecTarget{{Label: "Floor", Value: &ten}, {SQL: `SELECT 'House', 300000`}},
		Project: &SpecProjection{Method: "compound", Periods: 12, RateSQL: `SELECT 0.12`},
	}
	w, err := svc.RunSpec(sp, "line", nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(w.Data)
	if !strings.Contains(string(b), `"targets":[{"label":"Floor","value":100000,`) || !strings.Contains(string(b), `{"label":"House","value":300000,`) {
		t.Errorf("targets: %s", b)
	}
	// 12% a year from £1,210 over 12 months.
	if !strings.Contains(string(b), `135520`) {
		t.Errorf("projection didn't use the rate from its query: %s", b)
	}
	if p, _ := json.Marshal(w.Plan); strings.Contains(string(p), `"rate":`) {
		t.Errorf("the saved plan gained the looked-up rate: %s", p)
	}

	w, err = svc.RunSpec(&Spec{V: 1, SQL: `SELECT 'House' AS goal, SUM(balance_minor) AS current_minor, 300000 AS target_minor FROM account_status WHERE account_type = 'savings'`,
		Columns: map[string]SpecColumn{"current_minor": {Format: "money"}, "target_minor": {Format: "money"}}}, "progress", nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ = json.Marshal(w.Data)
	if !strings.Contains(string(b), `"label":"House"`) || !strings.Contains(string(b), `"target_text":"£3,000.00"`) ||
		!strings.Contains(string(b), `"value_text":"£1,210.00"`) {
		t.Errorf("progress: %s", b)
	}

	if _, err := svc.RunSpec(&Spec{V: 1, SQL: `SELECT 'a', 1`, Targets: []SpecTarget{{Label: "x"}}}, "line", nil); err == nil {
		t.Error("a target with no value or query ran")
	}
	if _, err := svc.RunSpec(&Spec{V: 1, SQL: `SELECT 'a', 1`, Targets: []SpecTarget{{Value: &ten}}}, "pie", nil); err == nil {
		t.Error("a pie chart took target lines")
	}
}

// A widget's query isn't cut short at the AI's 200 rows: a year of daily
// points draws whole. The AI's own lookups stay capped.
func TestWidgetQueriesReturnMoreThanTheAIsCap(t *testing.T) {
	svc := seedPeriodsData(t)
	const days = `WITH RECURSIVE d(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM d WHERE n < 365)
		SELECT date('2024-01-01', '+' || n || ' days') AS day, n AS value FROM d`
	w, err := svc.RunSpec(&Spec{V: 1, SQL: days}, "line", nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(w.Data)
	if !strings.Contains(string(b), `"2024-12-31"`) {
		t.Errorf("the line stops short of its 365th day: %.200s…", b)
	}
	res, err := svc.runQuery(days)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != MaxRows || !res.Truncated {
		t.Errorf("AI query: %d rows, truncated %v", len(res.Rows), res.Truncated)
	}
}
