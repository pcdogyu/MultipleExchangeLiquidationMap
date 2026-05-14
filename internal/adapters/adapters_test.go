package adapters

import "testing"

func TestNewSetAllowsNilApp(t *testing.T) {
	set := NewSet(nil)
	if (set == Set{}) {
		t.Fatal("expected adapter set to be initialized")
	}
}
