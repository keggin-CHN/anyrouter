package client

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// NewUUID generates a random RFC 4122 version 4 UUID string.
func NewUUID() string {
	var b [16]byte
	_, err := rand.Read(b[:])
	if err != nil {
		// Fallback if crypto/rand fails (extremely rare)
		return "00000000-0000-4000-8000-000000000000"
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// RandomHex returns a cryptographically secure random hexadecimal string of n bytes (length 2n).
func RandomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
