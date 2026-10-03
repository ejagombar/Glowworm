// Package money implements integer minor-unit monetary values (doc 02 §2).
// Money is never a float: amounts are int64 minor units (pence/cents) plus an
// ISO-4217 currency code with an exponent lookup for display.
package money

import (
	"fmt"
	"strings"
)

var exponents = map[string]int{
	"GBP": 2, "USD": 2, "EUR": 2, "JPY": 0, "CHF": 2, "CAD": 2, "AUD": 2,
	"NZD": 2, "SEK": 2, "NOK": 2, "DKK": 2, "PLN": 2, "CZK": 2, "HUF": 2,
	"INR": 2, "SGD": 2, "HKD": 2, "ZAR": 2, "BRL": 2, "MXN": 2, "KRW": 0,
	"TRY": 2, "RUB": 2, "CNY": 2, "ISK": 0, "BGN": 2, "RON": 2,
}

// Exponent returns the minor-unit exponent for a currency (default 2).
func Exponent(currency string) int {
	if e, ok := exponents[strings.ToUpper(currency)]; ok {
		return e
	}
	return 2
}

// Symbols maps the display currencies the UI supports (prototype settings).
var Symbols = map[string]string{"GBP": "£", "USD": "$", "EUR": "€"}

// Symbol returns the display symbol for a currency, defaulting to the code.
func Symbol(currency string) string {
	if s, ok := Symbols[strings.ToUpper(currency)]; ok {
		return s
	}
	return strings.ToUpper(currency) + " "
}

// Format renders minor units as "£1,234.56" (prototype fmtMoney parity).
// Negative amounts keep the sign after the symbol is omitted: "-£12.00" is
// rendered as "-£12.00" by callers that need it; Format itself renders
// the absolute value plus sign prefix when signed is requested via
// FormatSigned.
func Format(minor int64, currency string) string {
	neg := minor < 0
	var abs uint64
	if neg {
		abs = uint64(-minor)
	} else {
		abs = uint64(minor)
	}
	e := Exponent(currency)
	s := Symbol(currency)
	major := abs / uint64(pow10(e))
	frac := abs % uint64(pow10(e))
	if e == 0 {
		out := s + groupDigits(major)
		if neg {
			return "-" + out
		}
		return out
	}
	out := fmt.Sprintf("%s%s.%0*d", s, groupDigits(major), e, frac)
	if neg {
		return "-" + out
	}
	return out
}

// FormatSigned renders with an explicit +/− prefix (prototype tx-amount
// parity: "+£42.35", "−£8.75").
func FormatSigned(minor int64, currency string) string {
	switch {
	case minor > 0:
		return "+" + Format(minor, currency)
	case minor < 0:
		return "−" + Format(-minor, currency)
	default:
		return Format(0, currency)
	}
}

// Parse converts a decimal string ("42.35", "-1,234.50", "£12.99", "(3.20)")
// into minor units for the given currency exponent.
func Parse(s string, currency string) (int64, error) {
	t := strings.TrimSpace(s)
	neg := false
	if strings.HasPrefix(t, "(") && strings.HasSuffix(t, ")") {
		neg = true
		t = t[1 : len(t)-1]
	}
	t = strings.ReplaceAll(t, ",", "")
	t = strings.ReplaceAll(t, " ", "")
	t = strings.TrimPrefix(t, Symbol(currency))
	t = strings.TrimPrefix(t, "$")
	t = strings.TrimPrefix(t, "£")
	t = strings.TrimPrefix(t, "€")
	if strings.HasSuffix(strings.ToUpper(t), "DR") {
		neg = true
		t = t[:len(t)-2]
	}
	if strings.HasSuffix(strings.ToUpper(t), "CR") {
		t = t[:len(t)-2]
	}
	if t == "" {
		return 0, fmt.Errorf("empty amount")
	}
	negSign := false
	if strings.HasPrefix(t, "-") {
		negSign = true
		t = t[1:]
	} else if strings.HasPrefix(t, "+") {
		t = t[1:]
	}
	e := Exponent(currency)
	parts := strings.SplitN(t, ".", 2)
	major, err := parseUint(parts[0])
	if err != nil {
		return 0, fmt.Errorf("bad amount %q", s)
	}
	var frac int64
	if len(parts) == 2 {
		fracStr := parts[1]
		for len(fracStr) > e { // drop extra digits
			fracStr = fracStr[:len(fracStr)-1]
		}
		for len(fracStr) < e {
			fracStr += "0"
		}
		if fracStr != "" {
			u, err2 := parseUint(fracStr)
			if err2 != nil {
				return 0, fmt.Errorf("bad amount %q", s)
			}
			frac = int64(u)
		}
	}
	minor := int64(major)*pow10(e) + frac
	if neg || negSign {
		minor = -minor
	}
	return minor, nil
}

func pow10(n int) int64 {
	p := int64(1)
	for i := 0; i < n; i++ {
		p *= 10
	}
	return p
}

func parseUint(s string) (uint64, error) {
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	var v uint64
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("non-digit")
		}
		v = v*10 + uint64(c-'0')
	}
	return v, nil
}

// groupDigits renders 1234567 as "1,234,567".
func groupDigits(v uint64) string {
	s := fmt.Sprintf("%d", v)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
	}
	for i := pre; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}
