package liqmap

import (
	"strings"
	"testing"
	"time"
)

func TestWebDataSourceScheduleUsesConfiguredFiveMinuteSlots(t *testing.T) {
	now := time.Date(2026, 5, 15, 13, 33, 20, 0, time.Local)

	latest := time.UnixMilli(latestScheduledWebDataSourceCaptureTSForInterval(now, 5))
	next := time.UnixMilli(nextScheduledWebDataSourceCaptureTSForInterval(now, 5))

	if latest.Format("15:04:05") != "13:30:00" {
		t.Fatalf("expected latest 13:30:00, got %s", latest.Format("15:04:05"))
	}
	if next.Format("15:04:05") != "13:35:00" {
		t.Fatalf("expected next 13:35:00, got %s", next.Format("15:04:05"))
	}
}

func TestWebDataSourceScheduleUsesConfiguredInterval(t *testing.T) {
	now := time.Date(2026, 5, 15, 13, 33, 20, 0, time.Local)

	latest := time.UnixMilli(latestScheduledWebDataSourceCaptureTSForInterval(now, 15))
	next := time.UnixMilli(nextScheduledWebDataSourceCaptureTSForInterval(now, 15))

	if latest.Format("15:04:05") != "13:30:00" {
		t.Fatalf("expected latest 13:30:00, got %s", latest.Format("15:04:05"))
	}
	if next.Format("15:04:05") != "13:45:00" {
		t.Fatalf("expected next 13:45:00, got %s", next.Format("15:04:05"))
	}
}

func TestWebDataSourceScheduleDefaultsToSixtyMinutes(t *testing.T) {
	now := time.Date(2026, 5, 15, 13, 31, 0, 0, time.Local)

	next := time.UnixMilli(nextScheduledWebDataSourceCaptureTSForInterval(now, 0))

	if next.Format("15:04:05") != "14:00:00" {
		t.Fatalf("expected default next 14:00:00, got %s", next.Format("15:04:05"))
	}
}

func TestWebDataSourceScheduleUsesConfiguredTenMinuteSlots(t *testing.T) {
	now := time.Date(2026, 5, 15, 13, 55, 36, 0, time.Local)

	latest := time.UnixMilli(latestScheduledWebDataSourceCaptureTSForInterval(now, 10))
	next := time.UnixMilli(nextScheduledWebDataSourceCaptureTSForInterval(now, 10))

	if latest.Format("15:04:05") != "13:50:00" {
		t.Fatalf("expected latest 13:50:00, got %s", latest.Format("15:04:05"))
	}
	if next.Format("15:04:05") != "14:00:00" {
		t.Fatalf("expected next 14:00:00, got %s", next.Format("15:04:05"))
	}
}

func TestBuildWebDataSourceImmediatePushDetailShowsInterval(t *testing.T) {
	got := buildWebDataSourceImmediatePushDetail(7, false)

	if !strings.Contains(got, "每 7 分钟") {
		t.Fatalf("expected detail to include configured interval, got %q", got)
	}
}
