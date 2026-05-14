package main

import "testing"

func TestMainEntryPointExists(t *testing.T) {
	entry := main
	if entry == nil {
		t.Fatal("expected main entry point to exist")
	}
}
