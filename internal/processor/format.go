package processor

import (
	timing "gitlab.com/fightmaster1/timing-core"
)

// FormatCleanTime renders the elapsed time between two unix-millis instants as
// HH:MM:SS(.mmm). Port of rfid-sync's domain.FormatCleanTime — keep identical:
// milliseconds are zero-padded and the suffix is omitted when they are zero.
func FormatCleanTime(startMs, finishMs int64) string {
	return timing.FormatCleanTimeMillis(startMs, finishMs)
}
