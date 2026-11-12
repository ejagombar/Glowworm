package importer

import (
	"strings"
	"time"
)

func timeParse(layout, value string) (time.Time, error) {
	return time.Parse(layout, strings.TrimSpace(value))
}

type timeT = time.Time
