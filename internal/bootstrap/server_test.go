package bootstrap

import "testing"

func TestRunEntryPointExists(t *testing.T) {
	run := Run
	if run == nil {
		t.Fatal("expected bootstrap.Run to exist")
	}
}

func TestServerAddrFromEnvUsesAppPort(t *testing.T) {
	t.Setenv("APP_ADDR", "")
	t.Setenv("APP_PORT", "8890")

	if got := serverAddrFromEnv(); got != ":8890" {
		t.Fatalf("expected :8890, got %q", got)
	}
}

func TestServerAddrFromEnvPrefersAppAddr(t *testing.T) {
	t.Setenv("APP_ADDR", "127.0.0.1:8891")
	t.Setenv("APP_PORT", "8890")

	if got := serverAddrFromEnv(); got != "127.0.0.1:8891" {
		t.Fatalf("expected app addr, got %q", got)
	}
}
