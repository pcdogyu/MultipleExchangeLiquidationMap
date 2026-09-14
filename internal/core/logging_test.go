package liqmap

import (
	"bytes"
	"os"
	"testing"
)

func TestDebugLogWriterSkipsRedirectedWindowsStdout(t *testing.T) {
	var stdout bytes.Buffer
	var logFile bytes.Buffer

	w := debugLogWriter("windows", os.ModeNamedPipe, &stdout, &logFile)
	if _, err := w.Write([]byte("capture timed out\n")); err != nil {
		t.Fatal(err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("redirected Windows stdout must not receive logs, got %q", stdout.String())
	}
	if got := logFile.String(); got != "capture timed out\n" {
		t.Fatalf("unexpected file log %q", got)
	}
}

func TestDebugLogWriterKeepsInteractiveConsoleMirroring(t *testing.T) {
	var stdout bytes.Buffer
	var logFile bytes.Buffer

	w := debugLogWriter("windows", os.ModeCharDevice, &stdout, &logFile)
	if _, err := w.Write([]byte("server ready\n")); err != nil {
		t.Fatal(err)
	}
	if got := stdout.String(); got != "server ready\n" {
		t.Fatalf("unexpected console log %q", got)
	}
	if got := logFile.String(); got != "server ready\n" {
		t.Fatalf("unexpected file log %q", got)
	}
}
