// Package util holds small shared helpers.
package util

import (
	"crypto/rand"
	"fmt"
)

// NewID returns a random RFC-4122 v4 UUID string (no external deps).
func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// rand should never fail; fall back to a less-random but unique-ish value.
		return fmt.Sprintf("%x", b)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
