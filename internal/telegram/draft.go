package telegram

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
)

// sendMessageDraft sets a draft message in the user's Telegram chat input field.
// Requires Bot API 9.3+ (March 2026). This is a raw HTTP call since
// go-telegram-bot-api/v5 does not support this method yet.
func sendMessageDraft(botToken string, chatID int64, text string) error {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessageDraft", botToken)
	body, _ := json.Marshal(map[string]any{
		"chat_id": chatID,
		"text":    text,
	})
	resp, err := http.Post(url, "application/json", bytes.NewReader(body)) //nolint:gosec
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var result struct {
		OK          bool   `json:"ok"`
		Description string `json:"description,omitempty"`
	}
	json.NewDecoder(resp.Body).Decode(&result) //nolint:errcheck
	if !result.OK {
		return fmt.Errorf("sendMessageDraft: %s", result.Description)
	}
	return nil
}
