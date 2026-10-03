package query

import (
	"fmt"

	"glowworm/internal/money"
)

func fmtMoney(minor int64, currency string) string {
	return money.Format(minor, currency)
}

var _ = fmt.Sprintf