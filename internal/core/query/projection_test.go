package query

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
)

func fp(vs ...float64) []*float64 {
	out := make([]*float64, len(vs))
	for i := range vs {
		out[i] = &vs[i]
	}
	return out
}

func TestFutureLabels(t *testing.T) {
	cases := []struct {
		labels []string
		p      SpecProjection
		want   []string
	}{
		{[]string{"2024-11", "2024-12"}, SpecProjection{Periods: 3}, []string{"2025-01", "2025-02", "2025-03"}},
		{[]string{"2023", "2024"}, SpecProjection{Until: "+2y"}, []string{"2025", "2026"}},
		{[]string{"2024-12"}, SpecProjection{Until: "2025-02"}, []string{"2025-01", "2025-02"}},
		{[]string{"2024-12-02", "2024-12-09"}, SpecProjection{Periods: 2}, []string{"2024-12-16", "2024-12-23"}},
		{[]string{"2024-12-30", "2024-12-31"}, SpecProjection{Until: "+2d"}, []string{"2025-01-01", "2025-01-02"}},
		{[]string{"2024-01-31", "2024-02-29"}, SpecProjection{Periods: 1}, []string{"2024-03-29"}},
	}
	for _, c := range cases {
		st, err := stepOf(c.labels)
		if err != nil {
			t.Fatal(err)
		}
		if got := c.p.futureLabels(st); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%v %+v: got %v, want %v", c.labels, c.p, got, c.want)
		}
	}
	if len((&SpecProjection{}).futureLabels(labelStep{kind: "month", shape: "2006-01"})) != 12 {
		t.Error("default isn't 12 periods")
	}
}

func TestProjectValues(t *testing.T) {
	st, _ := stepOf([]string{"2024-01", "2024-02"})
	trend := (&SpecProjection{}).projectValues(fp(100, 110, 120, 130), 2, st)
	if !reflect.DeepEqual(trend, []float64{140, 150}) {
		t.Errorf("trend %v", trend)
	}
	basis := (&SpecProjection{Method: "average", Basis: 2}).projectValues(fp(0, 100, 120, 130), 1, st)
	if !reflect.DeepEqual(basis, []float64{140}) {
		t.Errorf("average over basis %v", basis)
	}
	// 12% a year compounds to 12% over 12 monthly periods, plus 12 contributions.
	comp := (&SpecProjection{Method: "compound", Rate: 0.12, Contribution: 0}).projectValues(fp(1000), 12, st)
	if math.Abs(comp[11]-1120) > 1e-6 {
		t.Errorf("compound %v", comp[11])
	}
	comp = (&SpecProjection{Method: "compound", Contribution: 50}).projectValues(fp(1000), 12, st)
	if math.Abs(comp[11]-1600) > 1e-6 {
		t.Errorf("contributions %v", comp[11])
	}
	yst, _ := stepOf([]string{"2024"})
	comp = (&SpecProjection{Method: "compound", Contribution: 50}).projectValues(fp(1000), 1, yst)
	if math.Abs(comp[0]-1600) > 1e-6 {
		t.Errorf("yearly contributions %v", comp[0])
	}
	if (&SpecProjection{}).projectValues(fp(5), 3, st) != nil {
		t.Error("trend from one point")
	}
}

func TestProjectedLineChart(t *testing.T) {
	sp := &Spec{V: 1, Project: &SpecProjection{Periods: 2},
		Columns: map[string]SpecColumn{"v": {Label: "Savings", Format: "money"}}}
	res := &QueryResult{Columns: []string{"m", "v"}, Rows: [][]any{{"2024-01", int64(100)}, {"2024-02", int64(200)}}}
	w, err := shapeSpec(sp, "line", nil, res, "GBP")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(w.Data)
	want := `{"format":"money","labels":["2024-01","2024-02","2024-03","2024-04"],"series":[` +
		`{"color":"#0e6f51","name":"Savings","values":[100,200,null,null]},` +
		`{"color":"#0e6f51","name":"Savings (projected)","projected":true,"values":[null,200,300,400]}]}`
	if string(b) != want {
		t.Fatalf("got  %s\nwant %s", b, want)
	}
	// Bars don't repeat the last real bar.
	w, err = shapeSpec(sp, "bar", nil, res, "GBP")
	if err != nil {
		t.Fatal(err)
	}
	b, _ = json.Marshal(w.Data)
	if !strings.Contains(string(b), `"values":[null,null,300,400]`) {
		t.Fatalf("bar %s", b)
	}
}

func TestProjectedNumber(t *testing.T) {
	sp := &Spec{V: 1, Caption: "savings", Project: &SpecProjection{Until: "2024-06"},
		Columns: map[string]SpecColumn{"v": {Format: "money"}}}
	res := &QueryResult{Columns: []string{"m", "v"}, Rows: [][]any{{"2024-01", int64(10000)}, {"2024-02", int64(20000)}}}
	w, err := shapeSpec(sp, "number", nil, res, "GBP")
	if err != nil {
		t.Fatal(err)
	}
	d := w.Data.(map[string]any)
	if d["value"] != "£600.00" || d["caption"] != "savings · projected to 2024-06" {
		t.Fatalf("%v", d)
	}
}

func TestProjectionErrors(t *testing.T) {
	res := &QueryResult{Columns: []string{"c", "v"}, Rows: [][]any{{"Food", int64(1)}, {"Rent", int64(2)}}}
	for _, c := range []struct {
		typ string
		p   SpecProjection
		msg string
	}{
		{"line", SpecProjection{}, "needs dates"},
		{"table", SpecProjection{}, "only line, bar and number"},
		{"line", SpecProjection{Method: "magic"}, "isn't one of"},
		{"line", SpecProjection{Until: "soon"}, "neither a date"},
	} {
		p := c.p
		_, err := shapeSpec(&Spec{V: 1, Project: &p}, c.typ, nil, res, "GBP")
		if err == nil || !strings.Contains(err.Error(), c.msg) {
			t.Errorf("%s %+v: %v", c.typ, c.p, err)
		}
	}
}

// A spec saved before projections existed still reads, and one saved with
// a projection keeps it.
func TestSpecProjectionRoundTrip(t *testing.T) {
	old := ParseSpec(`{"v":1,"sql":"SELECT 1","fill":"previous"}`)
	if old == nil || old.Project != nil {
		t.Fatal("old spec")
	}
	sp := ParseSpec(`{"v":1,"sql":"SELECT 1","project":{"method":"compound","until":"+5y","rate":0.04,"contribution":20000}}`)
	if sp == nil || sp.Project == nil || sp.Project.Rate != 0.04 || sp.Project.Until != "+5y" {
		t.Fatalf("%+v", sp)
	}
}
