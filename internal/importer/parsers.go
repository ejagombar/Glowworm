package importer

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/xuri/excelize/v2"
)

// utf8BOM is the byte-order mark some programs (Excel among them) put at the
// start of a text file. Left in, it becomes part of the first column's name.
var utf8BOM = []byte("\xef\xbb\xbf")

func ReadCSV(data []byte) ([][]string, string, error) {
	data = bytes.TrimPrefix(data, utf8BOM)
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
				break
			}
			return nil, "", err
		}
		out = append(out, rec)
	}
	out = trimPreambleRows(out)
	return out, string(delim), nil
}

func trimPreambleRows(table [][]string) [][]string {
	if len(table) < 2 {
		return table
	}
	window := len(table)
	if window > 10 {
		window = 10
	}
	counts := map[int]int{}
	maxCells := 0
	for _, row := range table[:window] {
		n := nonEmptyCells(row)
		counts[n]++
		if n > maxCells {
			maxCells = n
		}
	}
	target := 0
	best := 0
	for n, c := range counts {
		if n < 2 {
			continue
		}
		if c > best || (c == best && n > target) {
			target, best = n, c
		}
	}
	if target == 0 {
		target = maxCells
	}
	if target < 2 {
		return table
	}
	skip := 0
	for skip < len(table)-1 {
		if nonEmptyCells(table[skip]) >= target {
			break
		}
		skip++
	}
	if skip == 0 {
		return table
	}
	return table[skip:]
}

func nonEmptyCells(row []string) int {
	n := 0
	for _, c := range row {
		if strings.TrimSpace(c) != "" {
			n++
		}
	}
	return n
}

func sniffDelimiter(data []byte) rune {
	const maxLines = 10
	lines := make([][]byte, 0, maxLines)
	rest := data
	for len(rest) > 0 && len(lines) < maxLines {
		i := bytes.IndexByte(rest, '\n')
		var line []byte
		if i < 0 {
			line = rest
			rest = nil
		} else {
			line = rest[:i]
			rest = rest[i+1:]
		}
		line = bytes.TrimRight(line, "\r")
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return ','
	}
	best := ','
	bestScore := -1
	for _, c := range []rune{',', ';', '\t', '|'} {
		modeCount, modeFreq := delimiterMode(lines, byte(c))
		if modeCount == 0 {
			continue
		}
		score := modeCount * modeFreq
		if score > bestScore {
			best, bestScore = c, score
		}
	}
	return best
}

func delimiterMode(lines [][]byte, sep byte) (count, freq int) {
	counts := map[int]int{}
	for _, line := range lines {
		n := bytes.Count(line, []byte{sep})
		if n == 0 {
			continue
		}
		counts[n]++
	}
	for n, f := range counts {
		if f > freq || (f == freq && n > count) {
			count, freq = n, f
		}
	}
	return count, freq
}

// maxXLSXUnpacked caps how far a workbook may unpack. An xlsx is a zip, and a
// few kilobytes of zip can unpack to gigabytes; a statement is nowhere near
// this size.
const maxXLSXUnpacked = 128 << 20

func ReadXLSX(data []byte, sheet string) (sheets []string, table [][]string, err error) {
	f, err := excelize.OpenReader(bytes.NewReader(data), excelize.Options{UnzipSizeLimit: maxXLSXUnpacked})
	if err != nil {
		return nil, nil, fmt.Errorf("couldn't read the Excel file: %w", err)
	}
	defer func() { _ = f.Close() }()
	sheets = f.GetSheetList()
	if len(sheets) == 0 {
		return nil, nil, fmt.Errorf("the workbook has no sheets")
	}
	var rows [][]string
	if sheet == "" {
		// The first sheet with anything on it; its rows are kept rather than
		// read a second time.
		for _, s := range sheets {
			if r, _ := f.GetRows(s); len(r) > 0 {
				sheet, rows = s, r
				break
			}
		}
		if sheet == "" {
			sheet = sheets[0]
		}
	}
	if rows == nil {
		rows, err = f.GetRows(sheet)
		if err != nil {
			return sheets, nil, fmt.Errorf("couldn't read sheet %q: %w", sheet, err)
		}
	}
	// GetRows hands over fresh slices, so they are the table as they are.
	return sheets, rows, nil
}

// qifFields are the QIF record codes shown as columns, in display order.
// Split lines (S, E, $) and addresses are left out.
var qifFields = []struct {
	code byte
	name string
}{
	{'D', "Date"}, {'P', "Payee"}, {'M', "Memo"}, {'T', "Amount"},
	{'N', "Number"}, {'L', "Category"}, {'C', "Cleared"},
}

// QIFTable lays a QIF file out as a table with a header row, so it can be
// mapped like a CSV. Only the columns some record uses are included, apart
// from Date and Amount, which are always there.
func QIFTable(data []byte) ([][]string, error) {
	lines := strings.Split(strings.ReplaceAll(string(bytes.TrimPrefix(data, utf8BOM)), "\r\n", "\n"), "\n")
	var recs []map[byte]string
	var cur map[byte]string
	flush := func() {
		if cur != nil && cur['D'] != "" {
			recs = append(recs, cur)
		}
		cur = nil
	}
	for _, ln := range lines {
		ln = strings.TrimRight(ln, "\r")
		if strings.HasPrefix(ln, "!") {
			flush()
			continue
		}
		if ln == "^" {
			flush()
			continue
		}
		if len(ln) < 2 {
			continue
		}
		code, val := ln[0], strings.TrimSpace(ln[1:])
		if cur == nil {
			if code != 'D' {
				continue
			}
			cur = map[byte]string{}
		} else if code == 'D' && cur['D'] != "" {
			flush() // a record missing its closing ^
			cur = map[byte]string{}
		}
		if code == 'U' { // older files repeat the amount as U
			if cur['T'] == "" {
				cur['T'] = val
			}
			continue
		}
		if code == 'D' {
			// Quicken writes 1/ 2'26 for 1/2/26.
			val = strings.ReplaceAll(strings.ReplaceAll(val, " ", ""), "'", "/")
		}
		if _, seen := cur[code]; !seen {
			cur[code] = val
		}
	}
	flush()
	if len(recs) == 0 {
		return nil, fmt.Errorf("no transactions found in the QIF file")
	}
	var header []string
	var codes []byte
	for _, f := range qifFields {
		used := f.code == 'D' || f.code == 'T'
		for _, r := range recs {
			if r[f.code] != "" {
				used = true
				break
			}
		}
		if used {
			header = append(header, f.name)
			codes = append(codes, f.code)
		}
	}
	table := [][]string{header}
	for _, r := range recs {
		row := make([]string, len(codes))
		for i, c := range codes {
			row[i] = r[c]
		}
		table = append(table, row)
	}
	return table, nil
}

// QIFDateLayout picks a layout for QIF dates the usual formats can't read
// (e.g. US 1/2/06), or "" when they read as they are.
func QIFDateLayout(dates []string) string {
	auto := true
	for _, d := range dates {
		if iso, _ := tryDate(d); iso == "" {
			auto = false
			break
		}
	}
	if auto {
		return ""
	}
	for _, f := range qifDateFormats {
		ok := true
		for _, d := range dates {
			if _, err := timeParse(f, d); err != nil {
				ok = false
				break
			}
		}
		if ok {
			return f
		}
	}
	return ""
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

var (
	reTag   = regexp.MustCompile(`<(DTPOSTED|TRNAMT|NAME|PAYEE|MEMO|FITID|TRNTYPE)>([^<\r\n]*)`)
	reBlock = regexp.MustCompile(`(?s)<STMTTRN>(.*?)</STMTTRN>`)
	reDT    = regexp.MustCompile(`^(\d{4})(\d{2})(\d{2})`)
)

// OFXTable lays an OFX file's transactions out as a table with a header row,
// so it can be mapped like a CSV. Name is the NAME tag, else PAYEE.
func OFXTable(data []byte) ([][]string, error) {
	text := string(data)
	// Strip SGML headers (OFX v1 has no XML declaration).
	if i := strings.Index(text, "<OFX"); i > 0 {
		text = text[i:]
	}
	header := []string{"Date", "Name", "Memo", "Amount", "Type", "ID"}
	table := [][]string{header}
	for _, m := range reBlock.FindAllStringSubmatch(text, -1) {
		tags := map[string]string{}
		for _, tag := range reTag.FindAllStringSubmatch(m[1], -1) {
			if _, seen := tags[tag[1]]; !seen {
				tags[tag[1]] = strings.TrimSpace(tag[2])
			}
		}
		dm := reDT.FindStringSubmatch(tags["DTPOSTED"])
		if dm == nil {
			continue
		}
		name := tags["NAME"]
		if name == "" {
			name = tags["PAYEE"]
		}
		memo := tags["MEMO"]
		if memo != "" && strings.Contains(name, memo) {
			memo = ""
		}
		table = append(table, []string{dm[1] + "-" + dm[2] + "-" + dm[3], name, memo, tags["TRNAMT"], tags["TRNTYPE"], tags["FITID"]})
	}
	if len(table) == 1 {
		return nil, fmt.Errorf("no transactions found in the OFX file")
	}
	return table, nil
}

// StructuredTable reads a QIF or OFX file as a table.
func StructuredTable(kind Kind, data []byte) ([][]string, error) {
	if kind == KindOFX {
		return OFXTable(data)
	}
	return QIFTable(data)
}
