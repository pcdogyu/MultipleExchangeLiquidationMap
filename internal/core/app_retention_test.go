package liqmap

import (
	"testing"
	"time"
)

func TestDataRetentionCleanupOnStartDefaultsToFalse(t *testing.T) {
	t.Setenv("RETENTION_CLEANUP_ON_START", "")
	if dataRetentionCleanupOnStart() {
		t.Fatal("expected cleanup on start to default to false")
	}
}

func TestDataRetentionCleanupOnStartParsesEnabledValues(t *testing.T) {
	for _, value := range []string{"1", "true", "yes", "on"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("RETENTION_CLEANUP_ON_START", value)
			if !dataRetentionCleanupOnStart() {
				t.Fatalf("expected %q to enable cleanup on start", value)
			}
		})
	}
}

func TestDataRetentionCleanupIntervalUsesConfiguredHours(t *testing.T) {
	t.Setenv("RETENTION_CLEANUP_INTERVAL_HOURS", "12")
	if got := dataRetentionCleanupInterval(); got != 12*time.Hour {
		t.Fatalf("dataRetentionCleanupInterval() = %s, want 12h", got)
	}
}

func TestNextDataRetentionCleanupTimeAlignsToFixedTwelveHourPoints(t *testing.T) {
	loc := time.FixedZone("test", 8*60*60)
	interval := 12 * time.Hour
	tests := []struct {
		name string
		now  time.Time
		want time.Time
	}{
		{
			name: "morning schedules noon",
			now:  time.Date(2026, 7, 9, 8, 30, 0, 0, loc),
			want: time.Date(2026, 7, 9, 12, 0, 0, 0, loc),
		},
		{
			name: "afternoon schedules next midnight",
			now:  time.Date(2026, 7, 9, 13, 0, 0, 0, loc),
			want: time.Date(2026, 7, 10, 0, 0, 0, 0, loc),
		},
		{
			name: "midnight boundary schedules noon",
			now:  time.Date(2026, 7, 9, 0, 0, 0, 0, loc),
			want: time.Date(2026, 7, 9, 12, 0, 0, 0, loc),
		},
		{
			name: "noon boundary schedules next midnight",
			now:  time.Date(2026, 7, 9, 12, 0, 0, 0, loc),
			want: time.Date(2026, 7, 10, 0, 0, 0, 0, loc),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := nextDataRetentionCleanupTime(tt.now, interval)
			if !got.Equal(tt.want) {
				t.Fatalf("nextDataRetentionCleanupTime() = %s, want %s", got, tt.want)
			}
		})
	}
}
