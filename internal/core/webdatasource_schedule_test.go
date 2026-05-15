package liqmap

import (
	"strings"
	"testing"
	"time"
)

func TestWebDataSourceScheduleUsesConfiguredFiveMinuteSlots(t *testing.T) {
	now := time.Date(2026, 5, 15, 13, 33, 20, 0, time.Local)

	latest := time.UnixMilli(latestScheduledWebDataSourceCaptureTS(now, 5))
	next := time.UnixMilli(nextScheduledWebDataSourceCaptureTS(now, 5))

	if latest.Format("15:04:05") != "13:30:00" {
		t.Fatalf("expected latest 13:30:00, got %s", latest.Format("15:04:05"))
	}
	if next.Format("15:04:05") != "13:35:00" {
		t.Fatalf("expected next 13:35:00, got %s", next.Format("15:04:05"))
	}
}

func TestWebDataSourceScheduleCapsSavedIntervalsToFiveMinutes(t *testing.T) {
	now := time.Date(2026, 5, 15, 13, 33, 20, 0, time.Local)

	latest := time.UnixMilli(latestScheduledWebDataSourceCaptureTS(now, 15))
	next := time.UnixMilli(nextScheduledWebDataSourceCaptureTS(now, 15))

	if latest.Format("15:04:05") != "13:30:00" {
		t.Fatalf("expected latest 13:30:00, got %s", latest.Format("15:04:05"))
	}
	if next.Format("15:04:05") != "13:35:00" {
		t.Fatalf("expected next 13:35:00, got %s", next.Format("15:04:05"))
	}
}

func TestWebDataSourceScheduleDefaultsToFiveMinutes(t *testing.T) {
	now := time.Date(2026, 5, 15, 13, 31, 0, 0, time.Local)

	next := time.UnixMilli(nextScheduledWebDataSourceCaptureTS(now, 0))

	if next.Format("15:04:05") != "13:35:00" {
		t.Fatalf("expected default next 13:35:00, got %s", next.Format("15:04:05"))
	}
}

func TestBuildWebDataSourceImmediatePushDetailShowsInterval(t *testing.T) {
	got := buildWebDataSourceImmediatePushDetail(7, false)

	if !strings.Contains(got, "每 5 分钟") {
		t.Fatalf("expected detail to include configured interval, got %q", got)
	}
}
