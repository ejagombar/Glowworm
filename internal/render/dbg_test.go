package render

import (
	"strings"
	"testing"
)

func TestParseNames(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	tmpl := r.tmpl["dashboard"]
	names := []string{}
	for _, def := range tmpl.Templates() {
		names = append(names, def.Name())
	}
	t.Log("count:", len(names), "names:", strings.Join(names, " | "))
}
