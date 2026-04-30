package runtime

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleVersionReturnsGitMetadata(t *testing.T) {
	m := &Manager{
		run: func(name string, args ...string) ([]byte, error) {
			cmd := strings.Join(append([]string{name}, args...), " ")
			switch {
			case strings.Contains(cmd, "abbrev-ref HEAD"):
				return []byte("golangv2\n"), nil
			case strings.Contains(cmd, "rev-parse --short HEAD"):
				return []byte("abc123\n"), nil
			case strings.Contains(cmd, "show -s --format=%ci HEAD"):
				return []byte("2026-04-24 12:00:00 +0800\n"), nil
			default:
				t.Fatalf("unexpected command: %s", cmd)
				return nil, nil
			}
		},
		async: func(fn func()) { fn() },
	}

	req := httptest.NewRequest(http.MethodGet, "/api/version", nil)
	rec := httptest.NewRecorder()
	m.HandleVersion(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if got := body["branch"]; got != "golangv2" {
		t.Fatalf("expected branch golangv2, got %v", got)
	}
	if got := body["commit_id"]; got != "abc123" {
		t.Fatalf("expected commit_id abc123, got %v", got)
	}
	if got := body["commit_time"]; got != "2026-04-24 12:00:00 +0800" {
		t.Fatalf("expected commit_time to be trimmed, got %v", got)
	}
}

func TestHandleUpgradeProgressBuildsResponse(t *testing.T) {
	m := &Manager{
		run: func(name string, args ...string) ([]byte, error) {
			cmd := strings.Join(append([]string{name}, args...), " ")
			switch {
			case strings.Contains(cmd, "tail -n 260 /tmp/liqmap-upgrade.log"):
				return []byte("upgrade log"), nil
			case strings.Contains(cmd, "systemctl is-active --quiet"):
				return []byte("0\n"), nil
			case strings.Contains(cmd, "cat /tmp/liqmap-upgrade.exit"):
				return []byte("0\n"), nil
			default:
				t.Fatalf("unexpected command: %s", cmd)
				return nil, nil
			}
		},
		async: func(fn func()) { fn() },
	}

	req := httptest.NewRequest(http.MethodGet, "/api/upgrade/progress", nil)
	rec := httptest.NewRecorder()
	m.HandleUpgradeProgress(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if got := body["running"]; got != false {
		t.Fatalf("expected running=false, got %v", got)
	}
	if got := body["done"]; got != true {
		t.Fatalf("expected done=true, got %v", got)
	}
	if got := body["exit_code"]; got != "0" {
		t.Fatalf("expected exit_code 0, got %v", got)
	}
	if got := body["log"]; got != "upgrade log" {
		t.Fatalf("expected log payload, got %v", got)
	}
}

func TestHandleUpgradePullQueuesWork(t *testing.T) {
	var ran bool
	m := &Manager{
		run: func(name string, args ...string) ([]byte, error) {
			ran = true
			if name != "bash" {
				t.Fatalf("expected bash command, got %s", name)
			}
			if len(args) < 2 || args[0] != "-lc" || !strings.Contains(args[1], "systemd-run --scope") {
				t.Fatalf("unexpected upgrade command: %v", args)
			}
			return nil, nil
		},
		async: func(fn func()) { fn() },
	}

	req := httptest.NewRequest(http.MethodPost, "/api/upgrade/pull", nil)
	rec := httptest.NewRecorder()
	m.HandleUpgradePull(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !ran {
		t.Fatalf("expected upgrade command to run asynchronously")
	}
	if !strings.Contains(rec.Body.String(), "upgrade queued") {
		t.Fatalf("expected queued response, got %q", rec.Body.String())
	}
}

func TestHandleVersionRejectsWrongMethod(t *testing.T) {
	m := New(false)

	req := httptest.NewRequest(http.MethodPost, "/api/version", nil)
	rec := httptest.NewRecorder()
	m.HandleVersion(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}
