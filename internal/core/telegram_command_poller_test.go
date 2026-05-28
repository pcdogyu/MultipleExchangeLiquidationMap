package liqmap

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

func TestTelegramPullAllCommandParsing(t *testing.T) {
	for _, text := range []string{"/pullall", "/pullall@HY_claw_2026_bot", " /pullall now "} {
		if !isTelegramPullAllCommand(text) {
			t.Fatalf("expected %q to be recognized as pullall command", text)
		}
	}
	if isTelegramPullAllCommand("/pull30d") {
		t.Fatal("expected pull30d not to be recognized as pullall")
	}
}

func TestTelegramCommandMenuIncludesPullAll(t *testing.T) {
	var body string
	app := newTelegramRequestTestApp(t, telegramRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		raw, _ := io.ReadAll(req.Body)
		body = string(raw)
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":1}}`)),
		}, nil
	}))

	if err := app.sendTelegramCommandMenu(context.Background(), "123456:ABCdef", 42); err != nil {
		t.Fatalf("sendTelegramCommandMenu: %v", err)
	}
	if !strings.Contains(body, `"callback_data":"pull:all"`) {
		t.Fatalf("expected menu to include pull:all callback, got %s", body)
	}
	if !strings.Contains(body, "抓取全部并发送 8 组") {
		t.Fatalf("expected menu to include pullall label, got %s", body)
	}
}

func TestIsTelegramGetUpdatesConflict(t *testing.T) {
	if !isTelegramGetUpdatesConflict(assertErr("telegram api returned 409 Conflict: terminated by other getUpdates request")) {
		t.Fatal("expected getUpdates conflict to be detected")
	}
	if isTelegramGetUpdatesConflict(assertErr("telegram api sendMessage returned 409 Conflict")) {
		t.Fatal("did not expect sendMessage conflict to be treated as getUpdates conflict")
	}
	if isTelegramGetUpdatesConflict(assertErr("timeout")) {
		t.Fatal("did not expect generic timeout to be treated as getUpdates conflict")
	}
}

func TestTelegramCommandPollerEnabled(t *testing.T) {
	unset := os.Getenv("TELEGRAM_COMMAND_POLLER_ENABLED")
	t.Cleanup(func() {
		if unset == "" {
			_ = os.Unsetenv("TELEGRAM_COMMAND_POLLER_ENABLED")
			return
		}
		_ = os.Setenv("TELEGRAM_COMMAND_POLLER_ENABLED", unset)
	})

	cases := []struct {
		value string
		want  bool
	}{
		{"", true},
		{"1", true},
		{"true", true},
		{"on", true},
		{"0", false},
		{"false", false},
		{"off", false},
		{"bad", true},
	}
	for _, tc := range cases {
		if tc.value == "" {
			_ = os.Unsetenv("TELEGRAM_COMMAND_POLLER_ENABLED")
		} else {
			_ = os.Setenv("TELEGRAM_COMMAND_POLLER_ENABLED", tc.value)
		}
		if got := telegramCommandPollerEnabled(); got != tc.want {
			t.Fatalf("value=%q want=%v got=%v", tc.value, tc.want, got)
		}
	}
}

type assertErr string

func (e assertErr) Error() string { return string(e) }
