// Package periods resolves the global period selector (doc 04 §5.3) —
// prototype periodRange parity.
package periods

import "time"

// IDs of the period selector.
const (
	All       = "all"
	YTD       = "ytd"
	Last12M   = "last-12m"
	Last3M    = "last-3m"
	LastMonth = "last-month"
	LastDay   = "last-day"
	Custom    = "custom"
)

// Labels mirrors PERIOD_LABELS in the prototype.
var Labels = map[string]string{
	All: "All time", YTD: "Year to date", Last12M: "Last 12 months",
	Last3M: "Last 3 months", LastMonth: "Last month", LastDay: "Last day",
	Custom: "Custom range",
}

// Range resolves a period to [from, to] ISO dates ("" = open) plus a label.
func Resolve(period, from, to string, today time.Time) (string, string, string) {
	iso := func(t time.Time) string { return t.Format("2006-01-02") }
	switch period {
	case All:
		return "", "", Labels[All]
	case YTD:
		start := time.Date(today.Year(), 1, 1, 0, 0, 0, 0, today.Location())
		return iso(start), iso(today), Labels[YTD]
	case Last12M:
		start := today.AddDate(0, -12, 0)
		return iso(start), iso(today), Labels[Last12M]
	case Last3M:
		start := today.AddDate(0, -3, 0)
		return iso(start), iso(today), Labels[Last3M]
	case LastMonth:
		start := time.Date(today.Year(), today.Month()-1, 1, 0, 0, 0, 0, today.Location())
		end := time.Date(today.Year(), today.Month(), 0, 0, 0, 0, 0, today.Location())
		return iso(start), iso(end), Labels[LastMonth]
	case LastDay:
		start := today.AddDate(0, 0, -1)
		return iso(start), iso(today), Labels[LastDay]
	case Custom:
		if from != "" && to != "" {
			return from, to, "Custom range"
		}
		return "", "", Labels[Custom]
	default:
		return "", "", Labels[All]
	}
}

// ThisMonth returns [first of current month, today] (dashboard cards).
func ThisMonth(today time.Time) (string, string) {
	start := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, today.Location())
	return start.Format("2006-01-02"), today.Format("2006-01-02")
}

// PrevMonth returns [first, last] of the previous calendar month.
func PrevMonth(today time.Time) (string, string) {
	start := time.Date(today.Year(), today.Month()-1, 1, 0, 0, 0, 0, today.Location())
	end := time.Date(today.Year(), today.Month(), 0, 0, 0, 0, 0, today.Location())
	return start.Format("2006-01-02"), end.Format("2006-01-02")
}