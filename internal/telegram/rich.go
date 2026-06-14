package telegram

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const richMessageLimit = 32768

var (
	richHTTPClient      = &http.Client{Timeout: 30 * time.Second}
	richAPIEndpoint     = "https://api.telegram.org/bot%s/%s"
	reRichMarkdownImage = regexp.MustCompile(`!\[([^\]]*)\]\(([^)\s]+)(?:\s+"[^"]*")?\)`)
	reRichMediaHTMLTag  = regexp.MustCompile(`(?i)</?(?:img|video|audio|figure|figcaption|tg-collage|tg-slideshow|tg-map)\b[^>]*>`)
)

type richHTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type inputRichMessage struct {
	Markdown            string `json:"markdown,omitempty"`
	HTML                string `json:"html,omitempty"`
	IsRTL               bool   `json:"is_rtl,omitempty"`
	SkipEntityDetection bool   `json:"skip_entity_detection,omitempty"`
}

type sendRichMessageRequest struct {
	ChatID      int64            `json:"chat_id"`
	RichMessage inputRichMessage `json:"rich_message"`
}

// normalizeRichMarkdown keeps the model's Markdown intact for Telegram Rich
// Messages while disabling implicit media blocks from arbitrary URLs.
func normalizeRichMarkdown(src string) string {
	text := reRichMarkdownImage.ReplaceAllStringFunc(src, func(match string) string {
		parts := reRichMarkdownImage.FindStringSubmatch(match)
		if len(parts) < 3 {
			return match
		}
		alt := strings.TrimSpace(parts[1])
		rawURL := strings.TrimSpace(parts[2])
		u, err := url.Parse(rawURL)
		if err != nil || u.Scheme == "" || u.Host == "" {
			if alt != "" {
				return alt
			}
			return rawURL
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			if alt != "" {
				return alt
			}
			return rawURL
		}
		label := alt
		if label == "" {
			label = rawURL
		}
		return "[" + label + "](" + rawURL + ")"
	})

	return reRichMediaHTMLTag.ReplaceAllStringFunc(text, func(tag string) string {
		return strings.ReplaceAll(strings.ReplaceAll(tag, "<", "&lt;"), ">", "&gt;")
	})
}

func richMarkdownEligible(markdown string) bool {
	return markdown != "" && len([]rune(markdown)) <= richMessageLimit
}

func sendRichMessage(botToken string, chatID int64, markdown string) error {
	return sendRichMessageWithClient(richHTTPClient, richAPIEndpoint, botToken, chatID, markdown)
}

func sendRichMessageWithClient(client richHTTPDoer, endpoint, botToken string, chatID int64, markdown string) error {
	if client == nil {
		return fmt.Errorf("sendRichMessage: nil http client")
	}
	if !richMarkdownEligible(markdown) {
		return fmt.Errorf("sendRichMessage: markdown length %d exceeds rich limit %d", len([]rune(markdown)), richMessageLimit)
	}

	body, err := json.Marshal(sendRichMessageRequest{
		ChatID: chatID,
		RichMessage: inputRichMessage{
			Markdown: normalizeRichMarkdown(markdown),
		},
	})
	if err != nil {
		return fmt.Errorf("sendRichMessage marshal: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf(endpoint, botToken, "sendRichMessage"), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("sendRichMessage request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var result struct {
		OK          bool   `json:"ok"`
		Description string `json:"description,omitempty"`
	}
	limited := io.LimitReader(resp.Body, 1<<20)
	if err := json.NewDecoder(limited).Decode(&result); err != nil {
		return fmt.Errorf("sendRichMessage decode: %w", err)
	}
	if !result.OK {
		if result.Description == "" {
			result.Description = resp.Status
		}
		return fmt.Errorf("sendRichMessage: %s", result.Description)
	}
	return nil
}
