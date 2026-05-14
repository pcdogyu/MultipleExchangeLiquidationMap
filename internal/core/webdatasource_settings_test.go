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
