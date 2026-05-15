package liqmap

import "testing"

func TestNormalizeQuotedInputHandlesWindowsPaths(t *testing.T) {
	got := normalizeQuotedInput(`"C:\Program Files\Google\Chrome\Application\chrome.exe"`)
	want := `C:\Program Files\Google\Chrome\Application\chrome.exe`

	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestNormalizeQuotedInputHandlesEscapedWindowsPaths(t *testing.T) {
	got := normalizeQuotedInput(`\"C:\Program Files\Google\Chrome\Application\chrome.exe\"`)
	want := `C:\Program Files\Google\Chrome\Application\chrome.exe`

	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestNormalizePathInputAllowsClearingProfileDir(t *testing.T) {
	got := normalizePathInput("   ", true)
	if got != "" {
		t.Fatalf("expected empty path, got %q", got)
	}
}

func TestNormalizePathInputCleansQuotedWindowsPath(t *testing.T) {
	got := normalizePathInput(`"C:\MultipleExchangeLiquidationMap\coinglass_profile\"`, true)
	want := `C:\MultipleExchangeLiquidationMap\coinglass_profile`
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}
