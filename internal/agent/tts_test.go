package agent

import (
	"testing"
)

func TestExpandVoiceName(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"ru-RU-DmitryNeural", "Microsoft Server Speech Text to Speech Voice (ru-RU, DmitryNeural)"},
		{"en-US-EmmaMultilingualNeural", "Microsoft Server Speech Text to Speech Voice (en-US, EmmaMultilingualNeural)"},
		{"Microsoft Server Speech Text to Speech Voice (ru-RU, DmitryNeural)", "Microsoft Server Speech Text to Speech Voice (ru-RU, DmitryNeural)"},
	}
	for _, tt := range tests {
		got := expandVoiceName(tt.input)
		if got != tt.want {
			t.Errorf("expandVoiceName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestXmlEscape(t *testing.T) {
	got := xmlEscape(`Hello & "world" <test>`)
	want := "Hello &amp; &quot;world&quot; &lt;test&gt;"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestGenerateGECToken(t *testing.T) {
	token := generateGECToken(0)
	if len(token) != 64 { // SHA256 hex = 64 chars
		t.Errorf("expected 64 char hex, got %d chars: %s", len(token), token)
	}
	// Should be uppercase hex
	for _, c := range token {
		if !((c >= '0' && c <= '9') || (c >= 'A' && c <= 'F')) {
			t.Errorf("unexpected char %c in token", c)
			break
		}
	}
}

func TestExtractAudioData(t *testing.T) {
	// Header: 2 bytes (length=3), 3 header bytes, then audio data
	data := []byte{0, 3, 'a', 'b', 'c', 0xFF, 0xFE}
	got := extractAudioData(data)
	if len(got) != 2 || got[0] != 0xFF || got[1] != 0xFE {
		t.Errorf("got %v, want [0xFF 0xFE]", got)
	}
}

func TestExtractAudioData_TooShort(t *testing.T) {
	got := extractAudioData([]byte{0})
	if got != nil {
		t.Errorf("expected nil for short data, got %v", got)
	}
}

func TestExtractAudioData_HeaderOnly(t *testing.T) {
	// Header says 5 bytes but total is only 7 = 2 + 5, no audio
	data := []byte{0, 5, 'a', 'b', 'c', 'd', 'e'}
	got := extractAudioData(data)
	if got != nil {
		t.Errorf("expected nil when no audio data after header, got %v", got)
	}
}
