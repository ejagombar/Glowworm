package importer

import (
	"os"
	"strings"
	"testing"
)

func testdata(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("../../testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSniff(t *testing.T) {
	cases := []struct {
		name string
		data string
		want Kind
	}{
		{"monzo.csv", "Date,Amount\n", KindCSV},
		{"broker.xlsx", "PK\x03\x04", KindXLSX},
		{"barclays.ofx", "<OFX>", KindOFX},
		{"natwest.qif", "!Type:Bank\n", KindQIF},
		{"statement.pdf", "%PDF-1.4", KindPDF},
	}
	for _, c := range cases {
		if got := Sniff(c.name, []byte(c.data)); got != c.want {
			t.Errorf("Sniff(%s) = %s, want %s", c.name, got, c.want)
		}
	}
}

func TestReadCSVParse(t *testing.T) {
	data := testdata(t, "monzo.csv")
	table, _, err := ReadCSV(data)
	if err != nil {
		t.Fatal(err)
	}
	m := AutoMapping(KindCSV, table[0], nil)
	rows, err := ParseRows(m, table, "GBP")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3", len(rows))
	}
	if rows[0].Date != "2024-06-02" || rows[0].Description != "Tesco Express" || rows[0].AmountMinor != -4235 {
		t.Errorf("row0 = %+v", rows[0])
	}
	if rows[1].AmountMinor != 245000 {
		t.Errorf("row1 amount = %d, want 245000", rows[1].AmountMinor)
	}
}

func TestReadCSVSkipsPreamble(t *testing.T) {
	data := []byte("Exported from BankX on 02/06/2024\n\n" +
		"Date,Description,Amount\n" +
		"02/06/2024,Tesco Express,-42.35\n" +
		"01/06/2024,\"Acme Ltd — Salary\",2450.00\n")
	table, delim, err := ReadCSV(data)
	if err != nil {
		t.Fatal(err)
	}
	if delim != "," {
		t.Errorf("delimiter = %q, want \",\"", delim)
	}
	if len(table) != 3 {
		t.Fatalf("got %d rows (header + 2), want 3; table = %v", len(table), table)
	}
	if table[0][0] != "Date" || table[0][1] != "Description" || table[0][2] != "Amount" {
		t.Errorf("header row = %v, want [Date Description Amount]", table[0])
	}
	m := AutoMapping(KindCSV, table[0], nil)
	rows, err := ParseRows(m, table, "GBP")
	if err != nil {
		t.Fatalf("ParseRows: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("parsed %d rows, want 2", len(rows))
	}
	if rows[0].Description != "Tesco Express" || rows[0].AmountMinor != -4235 {
		t.Errorf("row0 = %+v", rows[0])
	}
}

func TestReadQIF(t *testing.T) {
	rows, err := ReadQIF(testdata(t, "natwest.qif"), "GBP")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	if rows[0].Date != "2024-06-01" || rows[0].AmountMinor != 245000 {
		t.Errorf("row0 = %+v", rows[0])
	}
	if rows[1].AmountMinor != -4235 {
		t.Errorf("row1 amount = %d", rows[1].AmountMinor)
	}
}

func TestReadOFX(t *testing.T) {
	rows, err := ReadOFX(testdata(t, "barclays.ofx"), "GBP")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	if rows[0].Date != "2024-06-01" {
		t.Errorf("date = %s", rows[0].Date)
	}
	if rows[0].Description == "" || rows[0].FitID != "OFX-1" {
		t.Errorf("row0 = %+v", rows[0])
	}
	if rows[1].AmountMinor != -4235 {
		t.Errorf("row1 amount = %d", rows[1].AmountMinor)
	}
}

func TestReadXLSX(t *testing.T) {
	sheets, table, err := ReadXLSX(testdata(t, "broker.xlsx"), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(sheets) != 2 || sheets[0] != "June" {
		t.Errorf("sheets = %v", sheets)
	}
	if len(table) != 3 {
		t.Fatalf("table rows = %d, want 3", len(table))
	}
	m := AutoMapping(KindXLSX, table[0], nil)
	rows, err := ParseRows(m, table, "GBP")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if rows[0].AmountMinor != -4235 {
		t.Errorf("amount = %d", rows[0].AmountMinor)
	}
}

func TestParseAmountConventions(t *testing.T) {
	cases := []struct {
		col  ColumnMap
		val  string
		want int64
	}{
		{ColumnMap{Role: RoleAmount}, "-42.35", -4235},
		{ColumnMap{Role: RoleAmount, Negatives: "paren"}, "(42.35)", -4235},
		{ColumnMap{Role: RoleAmount, Negatives: "dr_cr"}, "42.35DR", -4235},
		{ColumnMap{Role: RoleAmount, DecimalSep: ","}, "1.234,56", 123456},
		{ColumnMap{Role: RoleAmount}, "£1,234.56", 123456},
	}
	for _, c := range cases {
		got, err := parseAmount(c.col, c.val)
		if err != nil {
			t.Errorf("parseAmount(%v, %q): %v", c.col, c.val, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseAmount(%q) = %d, want %d", c.val, got, c.want)
		}
	}
}

func TestTryDateFormats(t *testing.T) {
	cases := map[string]string{
		"2 June 2026 11:36":    "2026-06-02",
		"2 June 2026 11:36:44": "2026-06-02",
		"2 June 2026 11:36 AM": "2026-06-02",
		"2 JUNE 2026 11:36":    "2026-06-02",
		"2  June  2026  11:36": "2026-06-02",
		"02/06/2026, 11:36":    "2026-06-02",
		"02/06/2026 23:04":     "2026-06-02",
		"2026-06-02T11:36:00":  "2026-06-02",
		"2026-06-02T11:36:00Z": "2026-06-02",
		"2026-06-02 11:36:00":  "2026-06-02",
		"June 2, 2026":         "2026-06-02",
		"2 Jun 2026":           "2026-06-02",
		"02 Jun 2026 3:04 PM":  "2026-06-02",
		"02/06/2026":           "2026-06-02",
		"2026-06-02":           "2026-06-02",
		" 2 June 2026 11:36 ":  "2026-06-02",
	}
	for in, want := range cases {
		got, f := tryDate(in)
		if got != want {
			t.Errorf("tryDate(%q) = %q (layout %q), want %q", in, got, f, want)
		}
	}
	if iso, _ := tryDate("not a date"); iso != "" {
		t.Errorf(`tryDate("not a date") = %q, want ""`, iso)
	}
}

func TestParseRowsLineNumbers(t *testing.T) {
	m := &Mapping{Kind: "csv", HasHeader: true, Columns: []ColumnMap{
		{Source: "Date", Role: RoleDate}, {Source: "Amount", Role: RoleAmount},
	}}
	table := [][]string{
		{"Date", "Amount"},
		{"2 June 2026 11:36", "-5.00"},
		{"bad-date", "-7.00"},
		{"3 June 2026", "-9.00"},
	}
	_, err := ParseRows(m, table, "GBP")
	if err == nil {
		t.Fatal("expected parse error for bad-date row")
	}
	pe, ok := err.(*ParseError)
	if !ok {
		t.Fatalf("expected *ParseError, got %T", err)
	}
	if pe.Line != 3 {
		t.Errorf("reported line = %d, want 3 (real file line)", pe.Line)
	}
}

func TestParseRowsAmountConventions(t *testing.T) {
	parse := func(m *Mapping, table [][]string) []int64 {
		t.Helper()
		m.Kind, m.HasHeader = "csv", true
		rows, err := ParseRows(m, table, "GBP")
		if err != nil {
			t.Fatal(err)
		}
		out := []int64{}
		for _, r := range rows {
			out = append(out, r.AmountMinor)
		}
		return out
	}
	eq := func(name string, got, want []int64) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s: got %v, want %v", name, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("%s: got %v, want %v", name, got, want)
			}
		}
	}
	dated := []ColumnMap{{Source: "Date", Role: RoleDate}, {Source: "Amount", Role: RoleAmount}}
	single := [][]string{{"Date", "Amount"}, {"2026-06-01", "12.50"}, {"2026-06-02", "-30.00"}}

	eq("signed", parse(&Mapping{Columns: dated}, single), []int64{1250, -3000})
	eq("inverted (credit card)", parse(&Mapping{Columns: dated, Amount: AmountSpec{Sign: SignInverted}}, single), []int64{-1250, 3000})

	split := [][]string{{"Date", "In", "Out"}, {"2026-06-01", "", "-9.99"}, {"2026-06-02", "100", ""}}
	eq("split columns ignore the bank's signs", parse(&Mapping{Columns: []ColumnMap{
		{Source: "Date", Role: RoleDate}, {Source: "In", Role: RoleAmountIn}, {Source: "Out", Role: RoleAmountOut},
	}}, split), []int64{-999, 10000})

	marked := [][]string{{"Date", "Amount", "Dr/Cr"}, {"2026-06-01", "5.00", "Debit"}, {"2026-06-02", "7.00", "Credit"}}
	eq("direction column", parse(&Mapping{Columns: dated, Amount: AmountSpec{DirectionColumn: "Dr/Cr"}}, marked), []int64{-500, 700})
	eq("direction column, custom values", parse(&Mapping{Columns: dated, Amount: AmountSpec{DirectionColumn: "Dr/Cr", OutValues: []string{"credit"}}}, marked), []int64{500, -700})

	bracketed := [][]string{{"Date", "Amount"}, {"2026-06-01", "(4.20)"}, {"2026-06-02", "4.20DR"}, {"2026-06-03", "4.20CR"}, {"2026-06-04", "4.20-"}}
	eq("brackets / DR / CR / trailing minus", parse(&Mapping{Columns: dated}, bracketed), []int64{-420, -420, 420, -420})
}

func TestParseRowsOriginalCurrency(t *testing.T) {
	m := &Mapping{Kind: "csv", HasHeader: true, Columns: []ColumnMap{
		{Source: "Date", Role: RoleDate}, {Source: "Amount", Role: RoleAmount},
		{Source: "Local amount", Role: RoleOrigAmount}, {Source: "Local currency", Role: RoleOrigCurrency},
	}}
	rows, err := ParseRows(m, [][]string{
		{"Date", "Amount", "Local amount", "Local currency"},
		{"2026-06-01", "-8.60", "-10.00", "EUR"},
		{"2026-06-02", "-3.00", "-3.00", "GBP"},
	}, "GBP")
	if err != nil {
		t.Fatal(err)
	}
	if r := rows[0]; r.AmountMinor != -860 || r.OrigAmountMinor != -1000 || r.OrigCurrency != "EUR" {
		t.Fatalf("foreign row = %+v", r)
	}
	if r := rows[1]; r.OrigCurrency != "" || r.OrigAmountMinor != 0 {
		t.Fatalf("home-currency row should carry no original amount: %+v", r)
	}
	auto := AutoMapping("csv", []string{"Date", "Amount", "Local amount", "Local currency"}, nil)
	if auto.Columns[2].Role != RoleOrigAmount || auto.Columns[3].Role != RoleOrigCurrency {
		t.Fatalf("auto mapping = %+v", auto.Columns)
	}
}

func TestUnsupported(t *testing.T) {
	cases := []struct {
		name, data, want string
	}{
		{"statement.pdf", "%PDF-1.4\n", "PDF"},
		{"scan.png", "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR", "doesn't look like"},
		{"old.xls", "\xD0\xCF\x11\xE0\xA1\xB1\x1a\xe1\x00", ".xls"},
		{"monzo.csv", "Date,Amount\n01/06/2026,-1.00\n", ""},
		{"excel.csv", "\xFF\xFED\x00a\x00t\x00e\x00", ""},
		{"barclays.ofx", "<OFX>\x00", ""},
	}
	for _, c := range cases {
		got := Unsupported(Sniff(c.name, []byte(c.data)), []byte(c.data))
		if (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
			t.Errorf("Unsupported(%s) = %q, want it to mention %q", c.name, got, c.want)
		}
	}
}
