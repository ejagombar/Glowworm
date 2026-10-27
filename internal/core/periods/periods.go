package periods

import "time"

const (
	All       = "all"
	YTD       = "ytd"
	Last12M   = "last-12m"
	Last3M    = "last-3m"
	LastMonth = "last-month"
	LastDay   = "last-day"
	Custom    = "custom"
	// ThisMonthPreset isn't offered in the date range menu; it's for widgets
	// that say "this month" and should roll over with the calendar.
	ThisMonthPreset = "this-month"
)

var Labels = map[string]string{
	All: "All time", YTD: "Year to date", Last12M: "Last 12 months",
	Last3M: "Last 3 months", LastMonth: "Last month", LastDay: "Last day",
	Custom: "Custom range", ThisMonthPreset: "This month",
}

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
	case ThisMonthPreset:
		f, t := ThisMonth(today)
		return f, t, Labels[ThisMonthPreset]
	case Custom:
		if from != "" && to != "" {
			return from, to, "Custom range"
		}
		return "", "", Labels[Custom]
	default:
		return "", "", Labels[All]
	}
}

func ThisMonth(today time.Time) (string, string) {
	start := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, today.Location())
	return start.Format("2006-01-02"), today.Format("2006-01-02")
}

func PrevMonth(today time.Time) (string, string) {
	start := time.Date(today.Year(), today.Month()-1, 1, 0, 0, 0, 0, today.Location())
	end := time.Date(today.Year(), today.Month(), 0, 0, 0, 0, 0, today.Location())
	return start.Format("2006-01-02"), end.Format("2006-01-02")
}
