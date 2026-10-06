package importer

import (
	"fmt"
	"strings"
	"time"

	"glowworm/internal/money"
)

const (
	RoleDate        = "date"
	RoleDesc        = "description"
	RoleAmount      = "amount"
	RoleAmountIn    = "amount_in"
	RoleAmountOut   = "amount_out"
	RoleCurrency    = "currency"
	RoleBalance     = "balance"
	RoleTypeHint    = "type_hint"
	RoleMemo        = "memo"
	RoleSource      = "source"
	RoleDestination = "destination"
	RoleIgnore      = "ignore"
)

type ColumnMap struct {
	Source     string `json:"source"`
	Role       string `json:"role"`
	DateFormat string `json:"date_format,omitempty"`
	DecimalSep string `json:"decimal_separator,omitempty"`
	Negatives  string `json:"negatives,omitempty"`
}

type Mapping struct {
	Kind         Kind        `json:"kind"`
	Sheet        string      `json:"sheet,omitempty"`
	HasHeader    bool        `json:"has_header"`
	Delimiter    string      `json:"delimiter,omitempty"`
	Columns      []ColumnMap `json:"columns"`
	BankHint     string      `json:"bank_hint,omitempty"`
	ProposedName string      `json:"proposed_name,omitempty"`
	Confidence   float64     `json:"confidence"`
	DateFormat   string      `json:"date_format,omitempty"`
}

func ParseRows(m *Mapping, table [][]string, currency string) ([]Row, error) {
	idx := map[string]int{}
	for i, name := range tableHeader(table, m) {
		idx[strings.TrimSpace(name)] = i
	}
	rows := []Row{}
	// Report real file lines: the header is line 1.
	lineOffset := 1
	if m.HasHeader {
		lineOffset = 2
	}
	for line, rec := range tableRecords(table, m) {
		row := Row{Line: line + lineOffset, Raw: map[string]string{}, Currency: currency}
		var descParts []string
		amountSet := false
		for _, col := range m.Columns {
			i, ok := idx[col.Source]
			if !ok || i >= len(rec) {
				continue
			}
			val := strings.TrimSpace(rec[i])
			row.Raw[col.Source] = val
			switch col.Role {
			case RoleDate:
				fmtStr := col.DateFormat
				if fmtStr == "" {
					fmtStr = m.DateFormat
				}
				if fmtStr != "" {
					if t, err := time.Parse(fmtStr, val); err == nil {
						row.Date = t.Format("2006-01-02")
					}
				}
				if row.Date == "" {
					iso, detected := tryDate(val)
					if iso == "" {
						return nil, &ParseError{Line: row.Line, Message: fmt.Sprintf("couldn't read the date %q", val)}
					}
					row.Date = iso
					if m.DateFormat == "" {
						m.DateFormat = detected
					}
				}
			case RoleDesc:
				descParts = append(descParts, val)
			case RoleMemo:
				if val != "" {
					descParts = append(descParts, val)
				}
			case RoleAmount:
				if val == "" {
					continue
				}
				minor, err := parseAmount(col, val)
				if err != nil {
					return nil, &ParseError{Line: row.Line, Message: err.Error()}
				}
				row.AmountMinor += minor
				amountSet = true
			case RoleAmountIn:
				if val != "" {
					minor, err := parseAmount(col, val)
					if err != nil {
						return nil, &ParseError{Line: row.Line, Message: err.Error()}
					}
					row.AmountMinor += minor
					amountSet = true
				}
			case RoleAmountOut:
				if val != "" {
					minor, err := parseAmount(col, val)
					if err != nil {
						return nil, &ParseError{Line: row.Line, Message: err.Error()}
					}
					row.AmountMinor -= minor
					amountSet = true
				}
			case RoleCurrency:
				row.Currency = strings.ToUpper(val)
			case RoleTypeHint:
				row.TypeHint = val
			case RoleSource:
				row.SourceText = val
			case RoleDestination:
				row.DestinationText = val
			}
		}
		row.Description = strings.Join(descParts, " — ")
		row.Description = strings.TrimSpace(row.Description)
		if row.Currency == "" {
			row.Currency = currency
		}
		if !amountSet || row.Date == "" {
			continue
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func tableHeader(table [][]string, m *Mapping) []string {
	if len(table) == 0 {
		return nil
	}
	if m.HasHeader {
		return table[0]
	}
	n := len(table[0])
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("Column %d", i+1)
	}
	return out
}

func tableRecords(table [][]string, m *Mapping) [][]string {
	if len(table) == 0 {
		return nil
	}
	if m.HasHeader {
		return table[1:]
	}
	return table
}

func parseAmount(col ColumnMap, val string) (int64, error) {
	v := strings.TrimSpace(val)
	if col.DecimalSep == "," {
		// European: 1.234,56 -> 1234.56 (before generic comma stripping)
		v = strings.ReplaceAll(v, ".", "")
		v = strings.ReplaceAll(v, ",", ".")
	}
	v = cleanAmount(v)
	neg := false
	switch col.Negatives {
	case "paren":
		if strings.HasPrefix(v, "(") && strings.HasSuffix(v, ")") {
			neg = true
			v = v[1 : len(v)-1]
		}
	case "dr_cr":
		if strings.HasSuffix(strings.ToUpper(v), "DR") {
			neg = true
			v = v[:len(v)-2]
		}
		if strings.HasSuffix(strings.ToUpper(v), "CR") {
			v = v[:len(v)-2]
		}
	}
	if strings.HasPrefix(v, "-") {
		neg = !neg
		v = v[1:]
	}
	v = strings.TrimPrefix(v, "+")
	minor, err := money.Parse(v, "GBP")
	if err != nil {
		return 0, fmt.Errorf("couldn't read the amount %q", val)
	}
	if neg {
		minor = -minor
	}
	return minor, nil
}

func AutoMapping(kind Kind, headers []string, sample [][]string) *Mapping {
	m := &Mapping{Kind: kind, HasHeader: true, Delimiter: ",", Confidence: 0.5}
	for _, h := range headers {
		role := RoleIgnore
		l := strings.ToLower(h)
		switch {
		case strings.Contains(l, "date") || l == "posted" || l == "transaction date":
			role = RoleDate
		case strings.Contains(l, "description") || strings.Contains(l, "narrative") || strings.Contains(l, "details") || strings.Contains(l, "payee") || strings.Contains(l, "name"):
			role = RoleDesc
		case strings.Contains(l, "memo") || strings.Contains(l, "note"):
			role = RoleMemo
		case strings.Contains(l, "amount") || l == "value" || l == "amt":
			role = RoleAmount
		case strings.Contains(l, "paid in") || strings.Contains(l, "money in") || strings.Contains(l, "credit") || strings.Contains(l, "deposit") || strings.Contains(l, "dividend") || strings.Contains(l, "interest"):
			role = RoleAmountIn
		case strings.Contains(l, "paid out") || strings.Contains(l, "money out") || strings.Contains(l, "debit") || strings.Contains(l, "withdrawal"):
			role = RoleAmountOut
		case strings.Contains(l, "balance"):
			role = RoleBalance
		case strings.Contains(l, "currency"):
			role = RoleCurrency
		case strings.Contains(l, "type"):
			role = RoleTypeHint
		}
		m.Columns = append(m.Columns, ColumnMap{Source: h, Role: role})
	}
	return m
}
