package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	geminiGenerateURL  = "https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent"
	transcribePrompt   = "Transcribe this voice message exactly as spoken. Return only the transcription, no commentary."
	transcribeTimeout_ = 30 * time.Second
)

var transcribeHTTPClient = &http.Client{Timeout: transcribeTimeout_}

// TranscribeConfig holds the Gemini model and API key used for audio transcription.
type TranscribeConfig struct {
	Model  string // e.g. "gemini-2.0-flash"
	APIKey string
}

// transcribeViaGemini calls the native Gemini generateContent API with inline audio data.
// This bypasses the OpenAI-compat layer which does not reliably support input_audio.
func transcribeViaGemini(ctx context.Context, cfg TranscribeConfig, audioData []byte, mimeType string) (string, error) {
	reqBody := map[string]any{
		"contents": []map[string]any{
			{
				"parts": []map[string]any{
					{"text": transcribePrompt},
					{
						"inline_data": map[string]any{
							"mime_type": mimeType,
							"data":      base64.StdEncoding.EncodeToString(audioData),
						},
					},
				},
			},
		},
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("transcribe: marshal: %w", err)
	}

	url := fmt.Sprintf(geminiGenerateURL, cfg.Model)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("transcribe: request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", cfg.APIKey)

	resp, err := transcribeHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("transcribe: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024))
	if err != nil {
		return "", fmt.Errorf("transcribe: read: %w", err)
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("transcribe: HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("transcribe: parse: %w", err)
	}
	if len(result.Candidates) == 0 || len(result.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("transcribe: empty response")
	}
	return result.Candidates[0].Content.Parts[0].Text, nil
}
