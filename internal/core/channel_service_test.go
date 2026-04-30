package liqmap

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

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
