package main

import "testing"

func TestServerEntryPointExists(t *testing.T) {
	entry := main
	if entry == nil {
		t.Fatal("expected server main entry point to exist")
	}
}
