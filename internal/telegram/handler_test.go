package telegram

import (
	"slices"
	"strings"
	"testing"
)

func TestNewUpdateConfigIncludesMessages(t *testing.T) {
	cfg := newUpdateConfig()
	if cfg.Timeout != 60 {
		t.Fatalf("timeout = %d, want 60", cfg.Timeout)
	}
	if !slices.Contains(cfg.AllowedUpdates, "message") {
		t.Fatalf("AllowedUpdates must include message: %#v", cfg.AllowedUpdates)
	}
	if !slices.Contains(cfg.AllowedUpdates, "callback_query") {
		t.Fatalf("AllowedUpdates must include callback_query: %#v", cfg.AllowedUpdates)
	}
}

func TestRedactTelegramToken(t *testing.T) {
	in := `Post "https://api.telegram.org/bot123456:AAEAddH9Do8xR5N5ubvh7nB9zUhT-2ws3iw/getUpdates": read tcp reset`
	got := redactTelegramToken(in)
	if strings.Contains(got, "123456:AAEAddH9Do8xR5N5ubvh7nB9zUhT-2ws3iw") {
		t.Fatalf("token was not redacted: %s", got)
	}
	if !strings.Contains(got, "bot<redacted>/getUpdates") {
		t.Fatalf("redacted URL shape not preserved: %s", got)
	}
}
