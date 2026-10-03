// Package clock provides timezone-aware "today"/"now" for services
// (GLOWWORM_TIMEZONE, doc 13 §3).
package clock

import (
	"time"
)

// Clock resolves "today" in the configured timezone.
type Clock struct{ Loc *time.Location }

// New builds a clock from an IANA timezone name ("Local" = system).
func New(name string) *Clock {
	loc, err := time.LoadLocation(name)
	if err != nil {
		loc = time.Local
	}
	return &Clock{Loc: loc}
}

// Today returns today's date (YYYY-MM-DD) in the clock's timezone.
func (c *Clock) Today() string { return time.Now().In(c.Loc).Format("2006-01-02") }

// TodayTime returns today as a time.Time in the clock's timezone.
func (c *Clock) TodayTime() time.Time { return time.Now().In(c.Loc) }

// Location returns the configured location.
func (c *Clock) Location() *time.Location { return c.Loc }

// Greeting returns morning/afternoon/evening (dashboard greeting).
func (c *Clock) Greeting() string {
	h := time.Now().In(c.Loc).Hour()
	switch {
	case h < 12:
		return "Good morning"
	case h < 18:
		return "Good afternoon"
	default:
		return "Good evening"
	}
}
