package clock

import (
	"time"
)

type Clock struct{ Loc *time.Location }

func New(name string) *Clock {
	loc, err := time.LoadLocation(name)
	if err != nil {
		loc = time.Local
	}
	return &Clock{Loc: loc}
}

func (c *Clock) Today() string { return time.Now().In(c.Loc).Format("2006-01-02") }

func (c *Clock) TodayTime() time.Time { return time.Now().In(c.Loc) }

func (c *Clock) Location() *time.Location { return c.Loc }

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
