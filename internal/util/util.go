// Package util holds small shared helpers: UUID generation, time helpers.
package util

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// NewID returns a random 128-bit identifier (hex, 32 chars) — UUIDv4-grade
// uniqueness without a dependency.
func NewID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return hex.EncodeToString(b)
}

// Now returns the current UTC time (RFC3339 helpers below).
func Now() time.Time { return time.Now().UTC() }

// NowStr returns the current time as RFC3339 UTC.
func NowStr() string { return Now().Format(time.RFC3339) }

// TodayStr returns today's calendar date (YYYY-MM-DD) in loc.
func TodayStr(loc *time.Location) string { return time.Now().In(loc).Format("2006-01-02") }

// ParseDate parses "YYYY-MM-DD".
func ParseDate(s string) (time.Time, error) {
	return time.ParseInLocation("2006-01-02", s, time.UTC)
}

// DateInRange reports whether date (inclusive, lexicographic ISO) falls in
// [from, to]; nil bounds are open.
func DateInRange(date string, from, to string) bool {
	if from != "" && date < from {
		return false
	}
	if to != "" && date > to {
		return false
	}
	return true
}
