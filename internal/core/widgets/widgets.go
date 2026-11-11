package widgets

import "encoding/json"

type Widget struct {
	ID    string `json:"id,omitempty"`
	Type  string `json:"type"`
	Title string `json:"title"`
	Data  any    `json:"data"`
	Plan  any    `json:"plan,omitempty"`
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
