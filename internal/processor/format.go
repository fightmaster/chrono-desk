package processor

import (
	"fmt"
	"time"
)

// FormatCleanTime renders the elapsed time between two unix-millis instants as
// HH:MM:SS(.mmm). Port of rfid-sync's domain.FormatCleanTime — keep identical:
// milliseconds are zero-padded and the suffix is omitted when they are zero.
func FormatCleanTime(startMs, finishMs int64) string {
	if finishMs < startMs {
		startMs, finishMs = finishMs, startMs
	}
	diff := time.Duration(finishMs-startMs) * time.Millisecond
	seconds := int64(diff / time.Second)
	hours := seconds / 3600
	minutes := (seconds % 3600) / 60
	secs := seconds % 60
	millis := diff.Milliseconds() % 1000
	suffix := ""
	if millis > 0 {
		suffix = fmt.Sprintf(".%03d", millis)
	}
	return fmt.Sprintf("%02d:%02d:%02d%s", hours, minutes, secs, suffix)
}
