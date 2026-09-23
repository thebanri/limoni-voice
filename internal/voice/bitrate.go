package voice

import "time"

// BitrateLadder lists the voice bitrates the controller moves between, lowest first. Opus
// speech stays intelligible at 12 kbps and is transparent around 32 kbps.
var BitrateLadder = []int{12000, 16000, 24000, DefaultBitrate}

// Thresholds of the bitrate controller. Stepping down is quick, because a congested link
// loses speech; stepping up waits for a stretch of clean reports so a flaky link does not
// oscillate.
const (
	downLossPct = 8.0
	downRTT     = 400 * time.Millisecond
	upLossPct   = 2.0
	upRTT       = 250 * time.Millisecond
	downAfter   = 2 * time.Second  // sustained bad reports before stepping down
	upAfter     = 10 * time.Second // sustained good reports before stepping up
	stepGap     = 3 * time.Second  // minimum time between two changes
)

// BitrateController picks the voice bitrate from the worst packet loss and round-trip time
// the room reports. The bitrate follows the network, never the speech, so constant bitrate
// encoding keeps packet sizes independent of what is said. The zero value starts at the top
// of the ladder.
type BitrateController struct {
	down      int // steps below the top of the ladder
	badSince  time.Time
	goodSince time.Time
	changedAt time.Time
}

// NewBitrateController starts at the top of the ladder.
func NewBitrateController() *BitrateController {
	return &BitrateController{}
}

// Bitrate returns the current target in bits per second.
func (c *BitrateController) Bitrate() int { return BitrateLadder[len(BitrateLadder)-1-c.down] }

// Update feeds one quality report: the worst loss (percent) and round-trip time among the
// receivers. It returns the bitrate to use and whether it changed. A zero rtt means unknown.
func (c *BitrateController) Update(lossPct float64, rtt time.Duration, now time.Time) (int, bool) {
	bad := lossPct >= downLossPct || rtt >= downRTT
	good := lossPct < upLossPct && rtt < upRTT

	switch {
	case bad:
		c.goodSince = time.Time{}
		if c.badSince.IsZero() {
			c.badSince = now
		}
		if c.down < len(BitrateLadder)-1 && now.Sub(c.badSince) >= downAfter && now.Sub(c.changedAt) >= stepGap {
			c.down++
			c.changedAt, c.badSince = now, now
			return c.Bitrate(), true
		}
	case good:
		c.badSince = time.Time{}
		if c.goodSince.IsZero() {
			c.goodSince = now
		}
		if c.down > 0 && now.Sub(c.goodSince) >= upAfter && now.Sub(c.changedAt) >= stepGap {
			c.down--
			c.changedAt, c.goodSince = now, now
			return c.Bitrate(), true
		}
	default:
		// In between: hold the current rate and start both timers over.
		c.badSince, c.goodSince = time.Time{}, time.Time{}
	}
	return c.Bitrate(), false
}

// Reset returns to the top of the ladder, e.g. when the room changes.
func (c *BitrateController) Reset() {
	*c = BitrateController{}
}
