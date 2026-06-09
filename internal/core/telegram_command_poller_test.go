package liqmap

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func TestTelegramPullAllCommandParsing(t *testing.T) {
	for _, text := range []string{"/pullall", "/pullall@HY_claw_2026_bot", " /pullall now ", "pullall", " pullall now "} {
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
	if !strings.Contains(body, "/pull30d - 拉取30天") {
		t.Fatalf("expected menu to include help text, got %s", body)
	}
}

func TestTelegramHelpCommandParsing(t *testing.T) {
	for _, text := range []string{"/help", "/help@HY_claw_2026_bot", "help"} {
		if !isTelegramHelpCommand(text) {
			t.Fatalf("expected %q to be recognized as help command", text)
		}
	}
}

func TestSplitTelegramCommandRejectsUnknownPlainText(t *testing.T) {
	cmd, args := splitTelegramCommand("hello world")
	if cmd != "" || args != "" {
		t.Fatalf("expected unknown plain text to be ignored, got cmd=%q args=%q", cmd, args)
	}
}

func TestTelegramCaptureBusyTextUsesRunningWindow(t *testing.T) {
	app := newTelegramRequestTestApp(t, telegramRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
		}, nil
	}))
	app.webds = &WebDataSourceManager{app: app, running: true}
	if _, err := app.db.Exec(`INSERT INTO webdatasource_runs(started_at, finished_at, status, window_days, error_message, records_count, source_meta_json)
		VALUES(1, 0, 'running', 7, '', 0, '')`); err != nil {
		t.Fatalf("insert running run: %v", err)
	}
	if got := app.telegramCaptureBusyText(); got != "已经有抓取7 Day任务抓取中，请稍后" {
		t.Fatalf("busy text = %q", got)
	}
}

func TestStartTelegramCommandPullReportsExistingRunningTaskWithoutAck(t *testing.T) {
	bodyCh := make(chan string, 2)
	app := newTelegramRequestTestApp(t, telegramRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		raw, _ := io.ReadAll(req.Body)
		bodyCh <- string(raw)
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
		}, nil
	}))
	app.webds = &WebDataSourceManager{app: app, running: true}
	if err := app.setSetting("telegram_bot_token", "123456:ABCdef"); err != nil {
		t.Fatalf("set bot token: %v", err)
	}
	if _, err := app.db.Exec(`INSERT INTO webdatasource_runs(started_at, finished_at, status, window_days, error_message, records_count, source_meta_json)
		VALUES(1, 0, 'running', 7, '', 0, '')`); err != nil {
		t.Fatalf("insert running run: %v", err)
	}

	app.startTelegramCommandPull(context.Background(), 42, 1)

	var body string
	select {
	case body = <-bodyCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for telegram message")
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	text, _ := payload["text"].(string)
	if text != "已经有抓取7 Day任务抓取中，请稍后" {
		t.Fatalf("text = %q", text)
	}
	select {
	case extra := <-bodyCh:
		t.Fatalf("expected only one telegram message, got extra payload: %s", extra)
	case <-time.After(200 * time.Millisecond):
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
