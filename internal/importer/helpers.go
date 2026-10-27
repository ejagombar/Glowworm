package importer

import (
	"strings"
	"time"

	"glowworm/internal/money"
)

func timeParse(layout, value string) (time.Time, error) {
	return time.Parse(layout, strings.TrimSpace(value))
}

type timeT = time.Time

var _ = money.Parse
var _ = atoi
