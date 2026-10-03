// Package importer parses statement files (doc 06): CSV/TSV, xlsx, OFX, QIF,
// PDF text. Detection = sniff + saved-schema fingerprint; the mapping model
// is doc 06 §5.2.
package importer

import (
	"archive/zip"
	"bytes"
	"fmt"
	"strings"
	"time"
)

// Kind is a statement format.
type Kind string

const (
	KindCSV  Kind = "csv"
	KindXLSX Kind = "xlsx"
	KindOFX  Kind = "ofx"
	KindQIF  Kind = "qif"
	KindPDF  Kind = "pdf"
)

// Sniff detects the format from content (doc 06 §3).
func Sniff(name string, data []byte) Kind {
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".ofx") || bytes.Contains(data, []byte("<OFX")):
		return KindOFX
	case strings.HasSuffix(lower, ".qif") || bytes.HasPrefix(data, []byte("!Type:")) || bytes.HasPrefix(data, []byte("!Account")):
		return KindQIF
	case strings.HasSuffix(lower, ".pdf") || bytes.HasPrefix(data, []byte("%PDF")):
		return KindPDF
	case strings.HasSuffix(lower, ".xlsx") || isZip(data):
		return KindXLSX
	default:
		return KindCSV
	}
}

func isZip(data []byte) bool {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return false
	}
	for _, f := range zr.File {
		if f.Name == "xl/workbook.xml" {
			return true
		}
	}
	return false
}

// Row is one parsed statement line.
type Row struct {
	Line            int
	Date            string // YYYY-MM-DD
	Description     string
	AmountMinor     int64
	Currency        string
	SourceText      string
	DestinationText string
	TypeHint        string
	FitID           string
	Raw             map[string]string
}

// ParseError reports the first bad row (doc 06 §11).
type ParseError struct {
	Line    int
	Message string
}

func (e *ParseError) Error() string { return fmt.Sprintf("line %d: %s", e.Line, e.Message) }

var dateRefFormats = []string{
	"2006-01-02", "02/01/2006", "01/02/2006", "02-01-2006", "2 Jan 2006",
	"02 Jan 2006", "Jan 2, 2006", "20060102", "01.02.2006", "2.1.2006",
	"2006/01/02", "02-Jan-2006", "2 January 2006", "January 2, 2006",
	"2 Jan 06", "Jan 2 2006",
}

// dateTimeFormats are datetime-first layouts (ISO etc.). Month names and
// AM/PM match case-insensitively in time.Parse, so "2 JUNE 2026" is covered.
var dateTimeFormats = []string{
	"2006-01-02T15:04:05Z07:00", "2006-01-02T15:04:05", "2006-01-02T15:04",
	"2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02 3:04 PM",
	"02/01/2006 15:04", "02/01/2006 3:04 PM", "01/02/2006 15:04",
	"2 January 2006 15:04", "2 January 2006 15:04:05", "2 January 2006 3:04 PM",
	"2 Jan 2006 15:04", "2 Jan 2006 3:04 PM", "Jan 2, 2006 15:04",
}

// tryDate attempts each reference format (bare dates, then with time
// suffixes) and returns (ISO date, matched layout). Tolerates extra
// whitespace and a comma between date and time ("02/06/2026, 11:36").
func tryDate(s string) (string, string) {
	s = strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
	candidates := []string{s}
	if strings.Contains(s, ",") {
		candidates = append(candidates, strings.Join(strings.Fields(strings.ReplaceAll(s, ",", " ")), " "))
	}
	for _, c := range candidates {
		for _, f := range dateTimeFormats {
			if t, err := time.Parse(f, c); err == nil {
				return t.Format("2006-01-02"), f
			}
		}
		for _, base := range dateRefFormats {
			if t, err := time.Parse(base, c); err == nil {
				return t.Format("2006-01-02"), base
			}
			for _, suf := range []string{" 15:04", " 15:04:05", " 3:04 PM"} {
				if t, err := time.Parse(base+suf, c); err == nil {
					return t.Format("2006-01-02"), base + suf
				}
			}
		}
	}
	return "", ""
}

func cleanAmount(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, ",", "")
	return strings.TrimSpace(s)
}
