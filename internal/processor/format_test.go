package processor

import "testing"

func TestFormatCleanTime(t *testing.T) {
	cases := []struct {
		name    string
		startMs int64
		endMs   int64
		want    string
	}{
		{"whole_seconds", 0, 6*3600*1000 + 30*60*1000 + 5*1000, "06:30:05"},
		// Regression for rfid-sync 2e9087e: 74ms must zero-pad to .074,
		// not render as .74 (which re-parses as 740ms).
		{"zero_padded_millis", 0, 74, "00:00:00.074"},
		{"millis_suffix", 0, 5*60*1000 + 74, "00:05:00.074"},
		{"zero_millis_no_suffix", 0, 42 * 1000, "00:00:42"},
		{"swapped_args", 10_000, 0, "00:00:10"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := FormatCleanTime(tc.startMs, tc.endMs); got != tc.want {
				t.Errorf("FormatCleanTime(%d, %d) = %q, want %q", tc.startMs, tc.endMs, got, tc.want)
			}
		})
	}
}
