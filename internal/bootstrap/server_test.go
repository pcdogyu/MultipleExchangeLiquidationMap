package bootstrap

import "testing"

func TestRunEntryPointExists(t *testing.T) {
	run := Run
	if run == nil {
		t.Fatal("expected bootstrap.Run to exist")
	}
}
