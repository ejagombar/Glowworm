package widgets

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"glowworm/internal/money"
)

type Dataset struct {
	Columns []Column `json:"columns"`
	Rows    [][]any  `json:"rows"`
	Meta    Meta     `json:"meta"`
}

type Column struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Type  string `json:"type"`
}

type Meta struct {
	Currency    string `json:"currency"`
	RowCount    int    `json:"row_count"`
	Truncated   bool   `json:"truncated"`
	TotalMinor  *int64 `json:"total_minor,omitempty"`
	EntityLabel string `json:"entity_label,omitempty"`
	TimeRange   struct {
		From string `json:"from"`
		To   string `json:"to"`
	} `json:"time_range"`
}

type Widget struct {
	ID      string   `json:"id,omitempty"`
	Type    string   `json:"type"`
	Title   string   `json:"title"`
	Data    any      `json:"data"`
	Dataset *Dataset `json:"dataset,omitempty"`
	Plan    any      `json:"plan,omitempty"`
}

var palette = []string{
	"#0e6f51", "#4f5bc7", "#c9822e", "#c0483f",
	"#2f7fb0", "#7a58b8", "#2e9d8f", "#6b7280",
}

func PaletteAt(i int) string {
	return palette[i%len(palette)]
}

func (w *Widget) DataJSON() string {
	b, _ := json.Marshal(w.Data)
	return string(b)
}

func Derive(datasetType string, ds *Dataset, title string) *Widget {
	if ds == nil {
		return &Widget{Type: datasetType, Title: title, Data: map[string]any{}}
	}
	switch datasetType {
	case "bar", "line":
		return deriveSeries(datasetType, ds, title)
	case "pie":
		return derivePie(ds, title)
	case "table":
		return deriveTable(ds, title)
	case "number":
		return deriveNumber(ds, title)
	case "text":
		return &Widget{Type: "text", Title: title, Data: map[string]any{"body": title}}
	default:
		return deriveSeries("bar", ds, title)
	}
}

func deriveSeries(kind string, ds *Dataset, title string) *Widget {
	xIdx, seriesCols := seriesColumns(ds)
	labels := make([]string, len(ds.Rows))
	for i, row := range ds.Rows {
		if xIdx >= 0 && xIdx < len(row) {
			labels[i] = fmt.Sprint(row[xIdx])
		}
	}
	type series struct {
		Name   string  `json:"name"`
		Color  string  `json:"color"`
		Values []int64 `json:"values"`
	}
	seriesOut := []series{}
	for si, colIdx := range seriesCols {
		vals := make([]int64, len(ds.Rows))
		for i, row := range ds.Rows {
			if colIdx < len(row) {
				if n, ok := row[colIdx].(float64); ok {
					vals[i] = int64(n)
				} else if n, ok := row[colIdx].(int64); ok {
					vals[i] = n
				}
			}
		}
		name := ds.Columns[colIdx].Label
		if name == "" {
			name = fmt.Sprintf("Series %d", si+1)
		}
		seriesOut = append(seriesOut, series{Name: name, Color: palette[si%len(palette)], Values: vals})
	}
	return &Widget{Type: kind, Title: title, Dataset: ds,
		Data: map[string]any{"labels": labels, "series": seriesOut}}
}

func seriesColumns(ds *Dataset) (int, []int) {
	xIdx := -1
	var valueCols []int
	for i, c := range ds.Columns {
		switch c.Type {
		case "string", "date":
			if xIdx < 0 {
				xIdx = i
			}
		case "money", "number":
			valueCols = append(valueCols, i)
		}
	}
	if xIdx < 0 && len(ds.Columns) > 1 {
		xIdx = 0
	}
	return xIdx, valueCols
}

func derivePie(ds *Dataset, title string) *Widget {
	xIdx, valueCols := seriesColumns(ds)
	if len(valueCols) == 0 {
		return &Widget{Type: "pie", Title: title, Dataset: ds, Data: map[string]any{"labels": []string{}, "values": []int64{}}}
	}
	valueIdx := valueCols[0]
	labels := []string{}
	values := []int64{}
	colors := []string{}
	for i, row := range ds.Rows {
		if xIdx >= 0 && xIdx < len(row) {
			labels = append(labels, fmt.Sprint(row[xIdx]))
		}
		var v int64
		if valueIdx < len(row) {
			switch n := row[valueIdx].(type) {
			case float64:
				v = int64(n)
			case int64:
				v = n
			}
		}
		values = append(values, v)
		colors = append(colors, palette[i%len(palette)])
	}
	return &Widget{Type: "pie", Title: title, Dataset: ds,
		Data: map[string]any{"labels": labels, "values": values, "colors": colors}}
}

func deriveTable(ds *Dataset, title string) *Widget {
	cols := make([]string, len(ds.Columns))
	for i, c := range ds.Columns {
		cols[i] = c.Label
	}
	rows := make([][]string, len(ds.Rows))
	for i, row := range ds.Rows {
		out := make([]string, len(row))
		for j, cell := range row {
			if j < len(ds.Columns) && ds.Columns[j].Type == "money" {
				if n, ok := cell.(float64); ok {
					out[j] = money.Format(int64(n), ds.Meta.Currency)
				} else if n, ok := cell.(int64); ok {
					out[j] = money.Format(n, ds.Meta.Currency)
				} else {
					out[j] = fmt.Sprint(cell)
				}
			} else {
				out[j] = fmt.Sprint(cell)
			}
		}
		rows[i] = out
	}
	return &Widget{Type: "table", Title: title, Dataset: ds,
		Data: map[string]any{"columns": cols, "rows": rows}}
}

func deriveNumber(ds *Dataset, title string) *Widget {
	var value int64
	currency := ds.Meta.Currency
	if ds.Meta.TotalMinor != nil {
		value = *ds.Meta.TotalMinor
	} else if len(ds.Rows) > 0 {
		for j, cell := range ds.Rows[len(ds.Rows)-1] {
			if j < len(ds.Columns) && (ds.Columns[j].Type == "money" || ds.Columns[j].Type == "number") {
				switch n := cell.(type) {
				case float64:
					value = int64(n)
				case int64:
					value = n
				}
				break
			}
		}
	}
	display := value
	if value < 0 {
		display = -value
	}
	caption := ds.Meta.EntityLabel
	if ds.Meta.TimeRange.From != "" {
		caption = strings.TrimSpace(caption + " · " + ds.Meta.TimeRange.From + " → " + ds.Meta.TimeRange.To)
	}
	return &Widget{Type: "number", Title: title, Dataset: ds,
		Data: map[string]any{"value": money.Format(display, currency), "caption": caption}}
}

func SortedKeys(m map[string]int64) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
