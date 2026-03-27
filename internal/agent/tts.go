package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

const (
	edgeTTSURL         = "wss://speech.platform.bing.com/consumer/speech/synthesize/readaloud/edge/v1"
	edgeTrustedToken   = "6A5AA1D4EAFF4E9FB37E23D68491D6F4"
	edgeChromiumVersion = "143.0.3650.75"
	edgeUserAgent       = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/143.0.0.0 Safari/537.36 Edg/143.0.0.0"
	edgeOrigin          = "chrome-extension://jdiccldimpdaibmpdkjnbmckianbfold"

	ttsTimeout    = 30 * time.Second
	ttsMaxTextLen = 3500 // keep under SSML limit
)

// TTSConfig holds Edge TTS configuration.
type TTSConfig struct {
	Voice  string // e.g. "ru-RU-DmitryNeural", "en-US-EmmaMultilingualNeural"
	Rate   string // e.g. "+0%", "+20%"
	Pitch  string // e.g. "+0Hz"
	Volume string // e.g. "+0%"
}

// Synthesize converts text to OGG Opus audio using Edge TTS.
func (cfg TTSConfig) Synthesize(ctx context.Context, text string) ([]byte, error) {
	if len(text) > ttsMaxTextLen {
		text = text[:ttsMaxTextLen]
	}

	connID := randomHexUUID()
	reqID := randomHexUUID()
	gecToken := generateGECToken(0)

	wsURL := fmt.Sprintf("%s?TrustedClientToken=%s&ConnectionId=%s&Sec-MS-GEC=%s&Sec-MS-GEC-Version=1-%s",
		edgeTTSURL, edgeTrustedToken, connID, gecToken, edgeChromiumVersion)

	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}
	headers := http.Header{
		"User-Agent":      {edgeUserAgent},
		"Origin":          {edgeOrigin},
		"Pragma":          {"no-cache"},
		"Cache-Control":   {"no-cache"},
		"Accept-Encoding": {"gzip, deflate, br"},
		"Accept-Language": {"en-US,en;q=0.9"},
	}

	conn, _, err := dialer.DialContext(ctx, wsURL, headers)
	if err != nil {
		return nil, fmt.Errorf("tts: dial: %w", err)
	}
	defer conn.Close()

	// Set read deadline
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(ttsTimeout)
	}
	conn.SetReadDeadline(deadline)

	// Phase 1: send config
	ts := jsTimestamp()
	configMsg := fmt.Sprintf(
		"X-Timestamp:%s\r\nContent-Type:application/json; charset=utf-8\r\nPath:speech.config\r\n\r\n"+
			`{"context":{"synthesis":{"audio":{"metadataoptions":{"sentenceBoundaryEnabled":"false","wordBoundaryEnabled":"false"},"outputFormat":"ogg-24khz-16bit-mono-opus"}}}}`,
		ts)
	if err := conn.WriteMessage(websocket.TextMessage, []byte(configMsg)); err != nil {
		return nil, fmt.Errorf("tts: send config: %w", err)
	}

	// Phase 2: send SSML
	voice := cfg.Voice
	if voice == "" {
		voice = "ru-RU-DmitryNeural"
	}
	rate := cfg.Rate
	if rate == "" {
		rate = "+0%"
	}
	pitch := cfg.Pitch
	if pitch == "" {
		pitch = "+0Hz"
	}
	volume := cfg.Volume
	if volume == "" {
		volume = "+0%"
	}

	escapedText := xmlEscape(text)
	voiceFull := expandVoiceName(voice)

	ssml := fmt.Sprintf(
		"X-RequestId:%s\r\nContent-Type:application/ssml+xml\r\nX-Timestamp:%sZ\r\nPath:ssml\r\n\r\n"+
			"<speak version='1.0' xmlns='http://www.w3.org/2001/10/synthesis' xml:lang='en-US'>"+
			"<voice name='%s'><prosody pitch='%s' rate='%s' volume='%s'>%s</prosody></voice></speak>",
		reqID, ts, voiceFull, pitch, rate, volume, escapedText)
	if err := conn.WriteMessage(websocket.TextMessage, []byte(ssml)); err != nil {
		return nil, fmt.Errorf("tts: send ssml: %w", err)
	}

	// Phase 3: receive audio
	var audio bytes.Buffer
	for {
		msgType, data, err := conn.ReadMessage()
		if err != nil {
			if audio.Len() > 0 {
				return audio.Bytes(), nil // got some audio before error
			}
			return nil, fmt.Errorf("tts: read: %w", err)
		}

		switch msgType {
		case websocket.TextMessage:
			if strings.Contains(string(data), "Path:turn.end") {
				return audio.Bytes(), nil
			}
		case websocket.BinaryMessage:
			chunk := extractAudioData(data)
			if len(chunk) > 0 {
				audio.Write(chunk)
			}
		}
	}
}

// extractAudioData parses Edge TTS binary frame:
// [2 bytes: header length (big-endian)] [header bytes] [audio data]
func extractAudioData(data []byte) []byte {
	if len(data) < 2 {
		return nil
	}
	headerLen := int(binary.BigEndian.Uint16(data[:2]))
	offset := 2 + headerLen
	if offset >= len(data) {
		return nil
	}
	return data[offset:]
}

// generateGECToken creates the Sec-MS-GEC DRM token.
func generateGECToken(clockSkew int64) string {
	now := time.Now().Unix() + clockSkew
	ticks := now + 11644473600 // Windows epoch offset
	ticks -= ticks % 300       // round to 5 minutes
	ticks *= 10_000_000        // convert to 100-nanosecond intervals

	input := fmt.Sprintf("%d%s", ticks, edgeTrustedToken)
	hash := sha256.Sum256([]byte(input))
	return strings.ToUpper(hex.EncodeToString(hash[:]))
}

func randomHexUUID() string {
	// Simple random hex string (no need for crypto-grade)
	h := sha256.Sum256([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))
	return hex.EncodeToString(h[:16])
}

func jsTimestamp() string {
	return time.Now().UTC().Format("Mon Jan 02 2006 15:04:05 GMT-0700 (MST)")
}

func xmlEscape(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		"'", "&apos;",
		`"`, "&quot;",
	)
	return r.Replace(s)
}

// expandVoiceName converts short voice name to the full form expected by Edge TTS.
// e.g. "ru-RU-DmitryNeural" → "Microsoft Server Speech Text to Speech Voice (ru-RU, DmitryNeural)"
func expandVoiceName(short string) string {
	if strings.HasPrefix(short, "Microsoft Server") {
		return short // already full form
	}
	parts := strings.SplitN(short, "-", 3)
	if len(parts) < 3 {
		return short
	}
	locale := parts[0] + "-" + parts[1]
	name := parts[2]
	return fmt.Sprintf("Microsoft Server Speech Text to Speech Voice (%s, %s)", locale, name)
}
