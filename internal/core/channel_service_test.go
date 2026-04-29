package liqmap

import "testing"

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
