package importer

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/xuri/excelize/v2"

	"glowworm/internal/money"
)

// ReadCSV parses CSV/TSV (delimiter sniffed) into a table (doc 06 §2).
func ReadCSV(data []byte) ([][]string, string, error) {
	delim := sniffDelimiter(data)
	r := csv.NewReader(bytes.NewReader(data))
	r.Comma = delim
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	var out [][]string
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			if len(out) > 0 {
				break // tolerate a malformed trailing line
			}
			return nil, "", err
		}
		out = append(out, rec)
	}
	return out, string(delim), nil
}

func sniffDelimiter(data []byte) rune {
	line := data
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		line = data[:i]
	}
	counts := map[rune]int{}
	for _, c := range []rune{',', ';', '\t', '|'} {
		counts[c] = bytes.Count(line, []byte{byte(c)})
	}
	best := ','
	bestN := 0
	for c, n := range counts {
		if n > bestN {
			best, bestN = c, n
		}
	}
	return best
}

// ReadXLSX parses an xlsx workbook (doc 06 §2 — v1 priority): returns the
// sheet names and the selected sheet's table.
func ReadXLSX(data []byte, sheet string) (sheets []string, table [][]string, err error) {
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		return nil, nil, fmt.Errorf("couldn't read the Excel file: %w", err)
	}
	defer f.Close()
	sheets = f.GetSheetList()
	if len(sheets) == 0 {
		return nil, nil, fmt.Errorf("the workbook has no sheets")
	}
	if sheet == "" {
		// First non-empty sheet.
		for _, s := range sheets {
			rows, _ := f.GetRows(s)
			if len(rows) > 0 {
				sheet = s
				break
			}
		}
		if sheet == "" {
			sheet = sheets[0]
		}
	}
	rows, err := f.GetRows(sheet)
	if err != nil {
		return sheets, nil, fmt.Errorf("couldn't read sheet %q: %w", sheet, err)
	}
	for _, r := range rows {
		row := make([]string, len(r))
		for i, cell := range r {
			row[i] = cell
		}
		table = append(table, row)
	}
	return sheets, table, nil
}

// ReadQIF parses QIF records (doc 06 §2).
func ReadQIF(data []byte, currency string) ([]Row, error) {
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	var rows []Row
	var cur *Row
	lineNo := 0
	for _, ln := range lines {
		lineNo++
		ln = strings.TrimRight(ln, "\r")
		if ln == "" {
			continue
		}
		if strings.HasPrefix(ln, "!") {
			cur = nil // section header; rows are pushed on D
			continue
		}
		if len(ln) < 2 {
			continue
		}
		code, val := ln[0], ln[1:]
		if cur == nil {
			if code != 'D' {
				continue
			}
			cur = &Row{Line: lineNo, Currency: currency, Raw: map[string]string{}}
		}
		switch code {
		case 'D':
			iso, _ := tryDate(val)
			if iso == "" {
				// QIF US format m/d/yyyy
				if t, err := timeParseQIF(val); err == nil {
					iso = t.Format("2006-01-02")
				}
			}
			if iso == "" {
				return nil, &ParseError{Line: lineNo, Message: fmt.Sprintf("couldn't read the date %q", val)}
			}
			if cur.Date != "" {
				// A new D without a '^' terminator starts a new record.
				rows = append(rows, *cur)
				cur = &Row{Line: lineNo, Currency: currency, Raw: map[string]string{}}
			}
			cur.Date = iso
			cur.Raw["date"] = val
		case 'T':
			minor, err := money.Parse(cleanAmount(val), currency)
			if err != nil {
				return nil, &ParseError{Line: lineNo, Message: fmt.Sprintf("couldn't read the amount %q", val)}
			}
			cur.AmountMinor = minor
		case 'P':
			cur.Description = strings.TrimSpace(val)
			cur.Raw["payee"] = val
		case 'M':
			cur.Description = strings.TrimSpace(cur.Description + " — " + strings.TrimSpace(val))
		case '^':
			if cur != nil && cur.Date != "" {
				rows = append(rows, *cur)
			}
			cur = nil
		}
	}
	if cur != nil && cur.Date != "" {
		rows = append(rows, *cur)
	}
	return rows, nil
}

var qifDateFormats = []string{"1/2/2006", "1/2/06", "1-2-2006", "2006/1/2"}

func timeParseQIF(s string) (t timeT, err error) {
	s = strings.TrimSpace(s)
	for _, f := range qifDateFormats {
		if t, err := timeParse(f, s); err == nil {
			return t, nil
		}
	}
	return timeT{}, fmt.Errorf("unparseable")
}

// ReadOFX parses OFX v1/v2 STMTTRN blocks (doc 06 §2).
var (
	reTag  = regexp.MustCompile(`<(DTPOSTED|TRNAMT|NAME|PAYEE|MEMO|FITID|TRNTYPE)>([^<\r\n]*)`)
	reBlock = regexp.MustCompile(`(?s)<STMTTRN>(.*?)</STMTTRN>`)
	reDT   = regexp.MustCompile(`^(\d{4})(\d{2})(\d{2})`)
)

func ReadOFX(data []byte, currency string) ([]Row, error) {
	text := string(data)
	// Strip SGML headers (OFX v1 has no XML declaration).
	if i := strings.Index(text, "<OFX"); i > 0 {
		text = text[i:]
	}
	var rows []Row
	for i, m := range reBlock.FindAllStringSubmatch(text, -1) {
		body := m[1]
		row := Row{Line: i + 1, Currency: currency, Raw: map[string]string{}}
		for _, tag := range reTag.FindAllStringSubmatch(body, -1) {
			key, val := tag[1], strings.TrimSpace(tag[2])
			row.Raw[key] = val
			switch key {
			case "DTPOSTED":
				if mm := reDT.FindStringSubmatch(val); mm != nil {
					row.Date = mm[1] + "-" + mm[2] + "-" + mm[3]
				}
			case "TRNAMT":
				minor, err := money.Parse(strings.ReplaceAll(val, ",", ""), currency)
				if err != nil {
					return nil, &ParseError{Line: row.Line, Message: fmt.Sprintf("couldn't read TRNAMT %q", val)}
				}
				row.AmountMinor = minor
			case "NAME":
				row.Description = val
			case "PAYEE":
				if row.Description == "" {
					row.Description = val
				}
			case "MEMO":
				if val != "" && !strings.Contains(row.Description, val) {
					row.Description = strings.TrimSpace(row.Description + " — " + val)
				}
			case "FITID":
				row.FitID = val
			case "TRNTYPE":
				row.TypeHint = val
			}
		}
		if row.Date != "" {
			rows = append(rows, row)
		}
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("no transactions found in the OFX file")
	}
	return rows, nil
}

// ExtractPDFText pulls the text layer from a PDF (doc 06 §2). Implementations
// use the embedded extraction helper; scanned PDFs fall to the optional
// vision path (not built in v1).
func ExtractPDFText(data []byte) (string, error) {
	return extractPDFText(data)
}

// convert helpers shared by parsers
func atoi(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return n
		}
		n = n*10 + int(s[i]-'0')
	}
	return n
}

var _ = strconv.Itoa