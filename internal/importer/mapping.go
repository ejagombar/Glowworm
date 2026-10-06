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
	// The amount in the currency actually paid, when the statement gives it
	// alongside the amount in the account's currency (e.g. Monzo's "Local
	// amount" / "Local currency").
	RoleOrigAmount   = "original_amount"
	RoleOrigCurrency = "original_currency"
)

// Amount sign conventions for a single amount column.
const (
	SignSigned   = ""         // money out is negative (most current accounts)
	SignInverted = "inverted" // money out is positive (many credit-card exports)
)

// AmountSpec says how a statement stores the direction of money.
type AmountSpec struct {
	Sign string `json:"sign,omitempty"`
	// DirectionColumn holds a debit/credit marker for an always-positive
	// amount; rows whose marker is in OutValues are money out.
	DirectionColumn string   `json:"direction_column,omitempty"`
	OutValues       []string `json:"out_values,omitempty"`
}

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
	Amount       AmountSpec  `json:"amount,omitempty"`
	// FXRates are user-entered rates (1 unit of the key currency in the
	// account's currency); they win over fetched daily rates.
	FXRates map[string]float64 `json:"fx_rates,omitempty"`
}

// DefaultOutValues are direction markers taken to mean money out.
var DefaultOutValues = []string{"DR", "D", "DEBIT", "OUT", "PAYMENT", "WITHDRAWAL", "PURCHASE"}

func (a AmountSpec) isOut(marker string) bool {
	vals := a.OutValues
	if len(vals) == 0 {
		vals = DefaultOutValues
	}
	m := strings.TrimSpace(marker)
	for _, v := range vals {
		if strings.EqualFold(strings.TrimSpace(v), m) {
			return true
		}
	}
	return false
}

// Amounts are parsed as hundredths; ToCurrencyMinor rescales them to the
// currency's own minor unit (e.g. yen has none).
func ToCurrencyMinor(hundredths int64, currency string) int64 {
	switch e := money.Exponent(currency); {
	case e < 2:
		div := int64(1)
		for i := e; i < 2; i++ {
			div *= 10
		}
		return hundredths / div
	case e > 2:
		mul := int64(1)
		for i := 2; i < e; i++ {
			mul *= 10
		}
		return hundredths * mul
	}
	return hundredths
}

func abs64(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
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
			// Separate columns hold sizes, whatever sign the bank wrote.
			case RoleAmountIn:
				if val != "" {
					minor, err := parseAmount(col, val)
					if err != nil {
						return nil, &ParseError{Line: row.Line, Message: err.Error()}
					}
					row.AmountMinor += abs64(minor)
					amountSet = true
				}
			case RoleAmountOut:
				if val != "" {
					minor, err := parseAmount(col, val)
					if err != nil {
						return nil, &ParseError{Line: row.Line, Message: err.Error()}
					}
					row.AmountMinor -= abs64(minor)
					amountSet = true
				}
			case RoleOrigAmount:
				if val != "" {
					minor, err := parseAmount(col, val)
					if err != nil {
						return nil, &ParseError{Line: row.Line, Message: err.Error()}
					}
					row.OrigAmountMinor = abs64(minor)
				}
			case RoleOrigCurrency:
				row.OrigCurrency = strings.ToUpper(val)
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
		switch {
		case m.Amount.DirectionColumn != "":
			marker := ""
			if i, ok := idx[m.Amount.DirectionColumn]; ok && i < len(rec) {
				marker = rec[i]
			}
			row.AmountMinor = abs64(row.AmountMinor)
			if m.Amount.isOut(marker) {
				row.AmountMinor = -row.AmountMinor
			}
		case m.Amount.Sign == SignInverted:
			row.AmountMinor = -row.AmountMinor
		}
		// The original amount takes the row's direction.
		if row.OrigAmountMinor != 0 && row.AmountMinor < 0 {
			row.OrigAmountMinor = -row.OrigAmountMinor
		}
		if row.OrigCurrency == "" || row.OrigCurrency == row.Currency {
			row.OrigAmountMinor, row.OrigCurrency = 0, ""
		} else {
			row.OrigAmountMinor = ToCurrencyMinor(row.OrigAmountMinor, row.OrigCurrency)
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
	// Brackets and a trailing DR mean negative whatever the column says;
	// a trailing CR is just a marker.
	neg := false
	if strings.HasPrefix(v, "(") && strings.HasSuffix(v, ")") {
		neg = true
		v = strings.TrimSpace(v[1 : len(v)-1])
	}
	switch up := strings.ToUpper(v); {
	case strings.HasSuffix(up, "DR"):
		neg = !neg
		v = strings.TrimSpace(v[:len(v)-2])
	case strings.HasSuffix(up, "CR"):
		v = strings.TrimSpace(v[:len(v)-2])
	}
	if strings.HasSuffix(v, "-") { // trailing minus, e.g. "42.35-"
		neg = !neg
		v = strings.TrimSpace(v[:len(v)-1])
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
		case strings.Contains(l, "local") && strings.Contains(l, "currency"),
			strings.Contains(l, "original") && strings.Contains(l, "currency"):
			role = RoleOrigCurrency
		case strings.Contains(l, "local") && strings.Contains(l, "amount"),
			strings.Contains(l, "original") && strings.Contains(l, "amount"):
			role = RoleOrigAmount
		case strings.Contains(l, "date") || l == "posted" || l == "transaction date":
			role = RoleDate
		case strings.Contains(l, "description") || strings.Contains(l, "narrative") || strings.Contains(l, "details") || strings.Contains(l, "payee") || strings.Contains(l, "name") || strings.Contains(l, "merchant"):
			role = RoleDesc
		case strings.Contains(l, "memo") || strings.Contains(l, "note"):
			role = RoleMemo
		case strings.Contains(l, "amount") || l == "value" || l == "amt" || l == "billed" || l == "charged":
			role = RoleAmount
		case strings.Contains(l, "paid in") || strings.Contains(l, "money in") || strings.Contains(l, "credit") || strings.Contains(l, "deposit") || strings.Contains(l, "dividend") || strings.Contains(l, "interest"):
			role = RoleAmountIn
		case strings.Contains(l, "paid out") || strings.Contains(l, "money out") || strings.Contains(l, "debit") || strings.Contains(l, "withdrawal"):
			role = RoleAmountOut
		case strings.Contains(l, "balance"):
			role = RoleBalance
		case strings.Contains(l, "currency") || l == "curr" || l == "ccy":
			role = RoleCurrency
		case strings.Contains(l, "type"):
			role = RoleTypeHint
		}
		m.Columns = append(m.Columns, ColumnMap{Source: h, Role: role})
	}
	return m
}
