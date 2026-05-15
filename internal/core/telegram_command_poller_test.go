package liqmap

import (
	"context"
	"io"
	"net/http"
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
