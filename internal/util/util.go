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
