package selkies

import (
	"hash/crc32"
	"image"
	"time"
)

// idleCadence uses decoded image changes, not packet arrivals: a CBR encoder
// can send identical frames forever. Hashing retains no old full-size image.
// The checksum is only a rate hint, never a media integrity/security decision.
type idleCadence struct {
	seen            bool
	checksum        uint32
	bounds          image.Rectangle
	changed         time.Time
	active, applied int
}

func (c *idleCadence) frame(frame *image.RGBA, now time.Time) {
	sum := crc32.ChecksumIEEE(frame.Pix)
	if !c.seen || sum != c.checksum || frame.Rect != c.bounds {
		c.changed = now
	}
	c.seen, c.checksum, c.bounds = true, sum, frame.Rect
}

func (c *idleCadence) desired(now, lastInput time.Time) int {
	lastChange := c.changed
	if lastInput.After(lastChange) {
		lastChange = lastInput
	}
	if c.seen && now.Sub(lastChange) >= time.Second {
		return min(5, c.active)
	}
	return c.active
}
