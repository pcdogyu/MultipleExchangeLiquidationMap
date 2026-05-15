package system

import (
	"net/http"
	"testing"
)

func TestMountRegistersSystemRoutes(t *testing.T) {
	mux := http.NewServeMux()
	Mount(mux, false)

	paths := []string{
		"/api/upgrade/pull",
		"/api/upgrade/progress",
		"/api/version",
		"/api/logs",
	}
	for _, path := range paths {
		req, err := http.NewRequest(http.MethodGet, path, nil)
		if err != nil {
			t.Fatalf("new request for %s: %v", path, err)
		}
		_, pattern := mux.Handler(req)
		if pattern == "" {
			t.Fatalf("expected route %s to be registered", path)
		}
	}
}
