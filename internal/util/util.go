package util

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

func NewID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return hex.EncodeToString(b)
}

func Now() time.Time { return time.Now().UTC() }

func NowStr() string { return Now().Format(time.RFC3339) }

func TodayStr(loc *time.Location) string { return time.Now().In(loc).Format("2006-01-02") }

func ParseDate(s string) (time.Time, error) {
	return time.ParseInLocation("2006-01-02", s, time.UTC)
}

func DateInRange(date string, from, to string) bool {
	if from != "" && date < from {
		return false
	}
	if to != "" && date > to {
		return false
	}
	return true
}
