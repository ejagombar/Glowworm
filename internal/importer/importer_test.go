package importer

import (
	"os"
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