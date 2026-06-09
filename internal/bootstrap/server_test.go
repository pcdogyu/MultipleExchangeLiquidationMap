package bootstrap

import (
	"strings"
	"testing"
)

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

func TestMaybeRunPruneCommandIgnoresNonPruneArgs(t *testing.T) {
	handled, err := maybeRunPruneCommand([]string{"serve"})
	if handled {
		t.Fatal("expected non-prune args to be ignored")
	}
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
}

func TestMaybeRunPruneCommandRejectsInvalidRetentionDays(t *testing.T) {
	handled, err := maybeRunPruneCommand([]string{"prune", "-retention-days", "0"})
	if !handled {
		t.Fatal("expected prune args to be handled")
	}
	if err == nil || !strings.Contains(err.Error(), "retention-days") {
		t.Fatalf("expected retention-days validation error, got %v", err)
	}
}
