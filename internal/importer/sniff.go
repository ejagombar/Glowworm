package importer

import (
	"archive/zip"
	"bytes"
	"fmt"
	"strings"
	"time"
)

type Kind string

const (
	KindCSV  Kind = "csv"
	KindXLSX Kind = "xlsx"
	KindOFX  Kind = "ofx"
	KindQIF  Kind = "qif"
	KindPDF  Kind = "pdf"
)

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

// Unsupported says why a file can't be imported, or "" when it can be read.
// Anything not recognised is read as CSV, so binary files are caught here
// rather than shown to the user as garbled columns.
func Unsupported(kind Kind, data []byte) string {
	const formats = "Download your statement from your bank as CSV, Excel (.xlsx), OFX or QIF instead."
	switch {
	case kind == KindPDF:
		return "Glowworm can't read PDF statements. " + formats
	case kind != KindCSV:
		return ""
	case bytes.HasPrefix(data, []byte("\xD0\xCF\x11\xE0")):
		return "That's an old-style Excel file (.xls). Open it in Excel or Numbers and save it as .xlsx or CSV, then import that."
	}
	head := data
	if len(head) > 8192 {
		head = head[:8192]
	}
	if bytes.IndexByte(head, 0) >= 0 && !isUTF16(head) {
		return "That doesn't look like a bank statement export. " + formats
	}
	return ""
}

// A UTF-16 CSV (some Excel exports) has NULs between its ASCII characters.
func isUTF16(data []byte) bool {
	return bytes.HasPrefix(data, []byte{0xFF, 0xFE}) || bytes.HasPrefix(data, []byte{0xFE, 0xFF})
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

type Row struct {
	Line            int
	Date            string
	Description     string
	AmountMinor     int64
	Currency        string
	SourceText      string
	DestinationText string
	TypeHint        string
	// OrigAmountMinor/OrigCurrency: what was actually paid when it wasn't in
	// the account's currency (signed like AmountMinor).
	OrigAmountMinor int64
	OrigCurrency    string
	Raw             map[string]string
}

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

// time.Parse matches month names and AM/PM case-insensitively, so "2 JUNE 2026"
// is covered.
var dateTimeFormats = []string{
	"2006-01-02T15:04:05Z07:00", "2006-01-02T15:04:05", "2006-01-02T15:04",
	"2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02 3:04 PM",
	"02/01/2006 15:04", "02/01/2006 3:04 PM", "01/02/2006 15:04",
	"2 January 2006 15:04", "2 January 2006 15:04:05", "2 January 2006 3:04 PM",
	"2 Jan 2006 15:04", "2 Jan 2006 3:04 PM", "Jan 2, 2006 15:04",
}

func TryDate(s string) (string, string) { return tryDate(s) }

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
