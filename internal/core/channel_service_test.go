package liqmap

import (
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	dbpkg "multipleexchangeliquidationmap/internal/platform/db"
)

type telegramRoundTripFunc func(*http.Request) (*http.Response, error)

func (f telegramRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestNormalizeTelegramAPIBaseSetting(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "empty",
			raw:  "",
			want: "",
		},
		{
			name: "default base is not persisted",
			raw:  "https://api.telegram.org",
			want: "",
		},
		{
			name: "default base with trailing slash is not persisted",
			raw:  "https://api.telegram.org/",
			want: "",
		},
		{
			name: "custom base is trimmed",
			raw:  " https://telegram-proxy.example.com/bot-api/ ",
			want: "https://telegram-proxy.example.com/bot-api",
		},
		{
			name: "quoted custom base is normalized",
			raw:  `"https://telegram-proxy.example.com"`,
			want: "https://telegram-proxy.example.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeTelegramAPIBaseSetting(tt.raw); got != tt.want {
				t.Fatalf("normalizeTelegramAPIBaseSetting(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestWrapTelegramNetworkErrorRedactsBotToken(t *testing.T) {
	err := wrapTelegramNetworkError(
		defaultTelegramAPIBaseURL,
		fmt.Errorf(`Post "https://api.telegram.org/bot123456:ABCdefTOKEN/sendPhoto": context deadline exceeded`),
	)
	got := err.Error()
	if strings.Contains(got, "123456:ABCdefTOKEN") {
		t.Fatalf("expected token to be redacted, got %q", got)
	}
	for _, want := range []string{"/bot<redacted>/sendPhoto", "this host may not reach Telegram directly"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected error to contain %q, got %q", want, got)
		}
	}
}

func TestNormalizeLegacyTelegramHistoryErrorTextRedactsBotToken(t *testing.T) {
	got := normalizeLegacyTelegramHistoryErrorText(`发送失败: Post "https://api.telegram.org/bot123456:ABCdefTOKEN/sendPhoto": timeout`)
	if strings.Contains(got, "123456:ABCdefTOKEN") {
		t.Fatalf("expected token to be redacted, got %q", got)
	}
	if !strings.Contains(got, "/bot<redacted>/sendPhoto") {
		t.Fatalf("expected redacted bot path, got %q", got)
	}
}

func TestTelegramTimeoutFromEnv(t *testing.T) {
	t.Setenv("TELEGRAM_PHOTO_TIMEOUT_SEC", "120")
	if got := telegramTimeoutFromEnv("TELEGRAM_PHOTO_TIMEOUT_SEC", telegramPhotoTimeout); got != 120*time.Second {
		t.Fatalf("timeout = %s, want 120s", got)
	}

	t.Setenv("TELEGRAM_PHOTO_TIMEOUT_SEC", "bad")
	if got := telegramTimeoutFromEnv("TELEGRAM_PHOTO_TIMEOUT_SEC", telegramPhotoTimeout); got != telegramPhotoTimeout {
		t.Fatalf("timeout = %s, want fallback %s", got, telegramPhotoTimeout)
	}
}

func TestTelegramRequestAttemptsFromEnv(t *testing.T) {
	t.Setenv("TELEGRAM_REQUEST_ATTEMPTS", "7")
	if got := telegramRequestAttemptsFromEnv(); got != 7 {
		t.Fatalf("attempts = %d, want 7", got)
	}

	t.Setenv("TELEGRAM_REQUEST_ATTEMPTS", "999")
	if got := telegramRequestAttemptsFromEnv(); got != telegramMaxAttempts {
		t.Fatalf("attempts = %d, want max %d", got, telegramMaxAttempts)
	}

	t.Setenv("TELEGRAM_REQUEST_ATTEMPTS", "bad")
	if got := telegramRequestAttemptsFromEnv(); got != telegramRequestAttempts {
		t.Fatalf("attempts = %d, want fallback %d", got, telegramRequestAttempts)
	}
}

func TestTelegramRetryDelayFromEnv(t *testing.T) {
	t.Setenv("TELEGRAM_RETRY_DELAY_MS", "25")
	if got := telegramRetryDelayFromEnv(); got != 25*time.Millisecond {
		t.Fatalf("retry delay = %s, want 25ms", got)
	}

	t.Setenv("TELEGRAM_RETRY_DELAY_MS", "")
	t.Setenv("TELEGRAM_RETRY_DELAY_SEC", "1.5")
	if got := telegramRetryDelayFromEnv(); got != 1500*time.Millisecond {
		t.Fatalf("retry delay = %s, want 1.5s", got)
	}

	t.Setenv("TELEGRAM_RETRY_DELAY_SEC", "bad")
	if got := telegramRetryDelayFromEnv(); got != telegramRetryDelay {
		t.Fatalf("retry delay = %s, want fallback %s", got, telegramRetryDelay)
	}
}

func TestDoTelegramRequestRetriesNoSuchHost(t *testing.T) {
	t.Setenv("TELEGRAM_REQUEST_ATTEMPTS", "3")
	t.Setenv("TELEGRAM_RETRY_DELAY_MS", "1")
	calls := 0
	app := newTelegramRequestTestApp(t, telegramRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return nil, fmt.Errorf(`Post %q: dial tcp: lookup api.telegram.org: no such host`, req.URL.String())
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
		}, nil
	}))

	err := app.doTelegramRequest("/bot123456:ABCdefTOKEN/sendPhoto", "application/json", []byte(`{}`), time.Second)
	if err != nil {
		t.Fatalf("doTelegramRequest returned error after retry: %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

func TestDoTelegramRequestReportsAttemptsAfterRetryExhausted(t *testing.T) {
	t.Setenv("TELEGRAM_REQUEST_ATTEMPTS", "2")
	t.Setenv("TELEGRAM_RETRY_DELAY_MS", "1")
	app := newTelegramRequestTestApp(t, telegramRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf(`Post %q: dial tcp: lookup api.telegram.org: no such host`, req.URL.String())
	}))

	err := app.doTelegramRequest("/bot123456:ABCdefTOKEN/sendPhoto", "application/json", []byte(`{}`), time.Second)
	if err == nil {
		t.Fatal("expected exhausted retry error")
	}
	got := err.Error()
	for _, want := range []string{"after 2 attempts", "/bot<redacted>/sendPhoto", "this host may not reach Telegram directly"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected error to contain %q, got %q", want, got)
		}
	}
	if strings.Contains(got, "123456:ABCdefTOKEN") {
		t.Fatalf("expected token to be redacted, got %q", got)
	}
}

func newTelegramRequestTestApp(t *testing.T, transport http.RoundTripper) *App {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	if err := dbpkg.Configure(db); err != nil {
		t.Fatalf("configure db: %v", err)
	}
	if err := dbpkg.Init(db); err != nil {
		t.Fatalf("init db: %v", err)
	}
	return &App{
		db: db,
		httpClient: &http.Client{
			Transport: transport,
		},
	}
}
