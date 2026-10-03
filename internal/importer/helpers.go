package importer

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	"github.com/ledongthuc/pdf"

	"glowworm/internal/money"
)

func timeParse(layout, value string) (time.Time, error) {
	return time.Parse(layout, strings.TrimSpace(value))
}

type timeT = time.Time

// extractPDFText extracts the text layer with the ledongthuc/pdf reader
// (pure Go). Requires word-by-word iteration to preserve column spacing.
func extractPDFText(data []byte) (string, error) {
	pdfReader, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", err
	}
	var b strings.Builder
	pages := pdfReader.NumPage()
	for i := 1; i <= pages; i++ {
		p := pdfReader.Page(i)
		if p.V.IsNull() {
			continue
		}
		lines, err := extractPageLines(p)
		if err != nil {
			continue
		}
		b.WriteString(lines)
		b.WriteString("\n")
	}
	out := b.String()
	if strings.TrimSpace(out) == "" {
		return "", fmt.Errorf("no text layer (scanned PDF — vision path not enabled)")
	}
	return out, nil
}

// extractPageLines renders one page's words grouped into text lines.
func extractPageLines(p pdf.Page) (string, error) {
	rows, err := p.GetTextByRow()
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, row := range rows {
		for i, w := range row.Content {
			if i > 0 && !strings.HasSuffix(b.String(), " ") {
				b.WriteString(" ")
			}
			b.WriteString(w.S)
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}

var _ = money.Parse
var _ = atoi