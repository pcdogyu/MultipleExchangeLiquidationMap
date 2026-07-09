package liqmap

import "testing"

func TestCapturePageURLUsesDefaultLoopback(t *testing.T) {
	t.Setenv("CAPTURE_BASE_URL", "")
	t.Setenv("PUBLIC_BASE_URL", "")
	t.Setenv("APP_ADDR", "")
	t.Setenv("APP_PORT", "")

	got := capturePageURL("/analysis?capture=1")
	want := "http://127.0.0.1:80/analysis?capture=1"
	if got != want {
		t.Fatalf("capturePageURL() = %q, want %q", got, want)
	}
}

func TestCapturePageURLUsesConfiguredBaseURL(t *testing.T) {
	t.Setenv("CAPTURE_BASE_URL", "10.15.0.6")
	t.Setenv("PUBLIC_BASE_URL", "")
	t.Setenv("APP_ADDR", "")
	t.Setenv("APP_PORT", "")

	got := capturePageURL("analysis?capture=1")
	want := "http://10.15.0.6/analysis?capture=1"
	if got != want {
		t.Fatalf("capturePageURL() = %q, want %q", got, want)
	}
}

func TestCaptureHostPortFromWildcardAddr(t *testing.T) {
	got := captureHostPortFromAddr("0.0.0.0:8080")
	want := "127.0.0.1:8080"
	if got != want {
		t.Fatalf("captureHostPortFromAddr() = %q, want %q", got, want)
	}
}
