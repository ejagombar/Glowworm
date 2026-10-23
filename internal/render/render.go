package render

import (
	"embed"
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"time"

	"glowworm/internal/money"
	"glowworm/web/static"
)

//go:embed templates/*.html
var templatesFS embed.FS

type Renderer struct {
	tmpl map[string]*template.Template
}

func New() (*Renderer, error) {
	r := &Renderer{tmpl: map[string]*template.Template{}}
	funcs := template.FuncMap{
		// asset is a static file's URL with its content version, so it can be
		// cached for good (see static.Version).
		"asset": func(path string) string {
			return "/static/" + path + "?v=" + static.Version(path)
		},
		"accountTypeBadge": func(t string) string {
			switch t {
			case "savings":
				return "badge-soft"
			case "credit":
				return "badge-expense"
			default:
				return "badge-income"
			}
		},
		"money":        func(minor int64, cc string) string { return money.Format(minor, cc) },
		"moneySigned":  func(minor int64, cc string) string { return money.FormatSigned(minor, cc) },
		"moneyPtr": func(minor *int64, cc string) string {
			if minor == nil {
				return ""
			}
			return money.FormatSigned(*minor, cc)
		},
		"symbol":       money.Symbol,
		"dayMonth":     func(iso string) string { return fmtDate(iso, "2 Jan") },
		"dayMonthYear": func(iso string) string { return fmtDate(iso, "2 Jan 2006") },
		"monthName": func(mm string) string {
			months := []string{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"}
			i := 0
			if len(mm) == 2 {
				i = int(mm[0]-'0')*10 + int(mm[1]-'0')
			}
			if i < 1 || i > 12 {
				return mm
			}
			return months[i-1]
		},
		"initial":  func(s string) string { return strings.ToUpper(firstRune(s)) },
		"safeHTML": func(s template.HTML) template.HTML { return s },
		"dict": func(pairs ...any) map[string]any {
			m := map[string]any{}
			for i := 0; i+1 < len(pairs); i += 2 {
				k, _ := pairs[i].(string)
				m[k] = pairs[i+1]
			}
			return m
		},
		"mul": func(a, b int64) int64 { return a * b },
		// stepParts splits a "How I worked this out" step into its heading
		// and the code (SQL) on the lines after it.
		"stepParts": func(s string) map[string]string {
			head, code, _ := strings.Cut(s, "\n")
			return map[string]string{"Text": head, "Code": strings.TrimSpace(code)}
		},
		"pct": func(f float64) string { return fmt.Sprintf("%.0f%%", f*100) },
		"sub": func(a, b int) int { return a - b },
		"add": func(a, b int64) int64 { return a + b },
		"json": func(v any) template.JS {
			b, _ := jsonMarshal(v)
			return template.JS(b)
		},
		// html/template escapes quotes to &quot;, which the browser decodes
		// back.
		"jsonAttr": func(v any) template.HTMLAttr {
			if s, ok := v.(string); ok {
				return template.HTMLAttr(s)
			}
			b, _ := jsonMarshal(v)
			return template.HTMLAttr(b)
		},
		"weekdayGreeting": func() string { return "" },
		"prefix":          func(s, p string) bool { return strings.HasPrefix(s, p) },
	}
	entries, err := templatesFS.ReadDir("templates")
	if err != nil {
		return nil, err
	}
	fragFiles := []string{}
	views := []string{}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "frag_") {
			fragFiles = append(fragFiles, "templates/"+name)
		} else if name != "layout.html" {
			views = append(views, name)
		}
	}
	for _, v := range views {
		files := append([]string{"templates/layout.html"}, fragFiles...)
		files = append(files, "templates/"+v)
		t, err := template.New(v).Funcs(funcs).ParseFS(templatesFS, files...)
		if err != nil {
			return nil, fmt.Errorf("render: %s: %w", v, err)
		}
		r.tmpl[strings.TrimSuffix(v, ".html")] = t
	}
	return r, nil
}

func (r *Renderer) Render(w http.ResponseWriter, name string, data any) error {
	// No heuristic caching: binaries are updated in place, and a stale page
	// with fresh JS breaks its handlers.
	w.Header().Set("Cache-Control", "no-store")
	_, ok := r.tmpl[name]
	if !ok {
		return fmt.Errorf("render: unknown template %q", name)
	}
	return r.tmpl[name].ExecuteTemplate(w, "layout.html", data)
}

func (r *Renderer) RenderFragment(w http.ResponseWriter, name string, data any) error {
	w.Header().Set("Cache-Control", "no-store")
	for _, t := range r.tmpl {
		if tt := t.Lookup(name); tt != nil {
			return tt.Execute(w, data)
		}
	}
	return fmt.Errorf("render: unknown fragment %q", name)
}

func fmtDate(iso, layout string) string {
	t, err := time.Parse("2006-01-02", iso)
	if err != nil {
		return iso
	}
	return t.Format(layout)
}

func firstRune(s string) string {
	for _, r := range s {
		return string(r)
	}
	return "?"
}
