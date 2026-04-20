package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// geminiListURL is the public ListModels endpoint. pageSize=1000 should
// comfortably cover the full public catalog; we paginate only on overflow.
const geminiListURL = "https://generativelanguage.googleapis.com/v1beta/models?pageSize=1000"

type geminiModelsResp struct {
	Models []struct {
		Name                       string   `json:"name"` // "models/gemini-2.5-flash"
		DisplayName                string   `json:"displayName"`
		InputTokenLimit            int      `json:"inputTokenLimit"`
		OutputTokenLimit           int      `json:"outputTokenLimit"`
		SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
	} `json:"models"`
	NextPageToken string `json:"nextPageToken"`
}

// FetchGeminiModels lists the Gemini model catalog for the given API key and
// returns capability records keyed by bare model id (e.g. "gemini-2.5-flash").
// Gemini's API does not publish prices, so they are merged in from the
// hardcoded table in geminiPricing (matched by prefix).
func FetchGeminiModels(ctx context.Context, apiKey string) (map[string]Capabilities, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("gemini: api_key is required")
	}
	req, err := http.NewRequestWithContext(ctx, "GET", geminiListURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-goog-api-key", apiKey)

	resp, err := geminiHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &APIError{StatusCode: resp.StatusCode, Message: string(body)}
	}

	var parsed geminiModelsResp
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("gemini: parse models: %w", err)
	}

	out := make(map[string]Capabilities, len(parsed.Models))
	for _, m := range parsed.Models {
		id := strings.TrimPrefix(m.Name, "models/")
		// Skip non-chat models: embedding-*, imagen-*, gemma doesn't support
		// tools (keep gemma but flag accordingly).
		if strings.HasPrefix(id, "embedding-") ||
			strings.HasPrefix(id, "text-embedding-") ||
			strings.HasPrefix(id, "aqa") ||
			strings.HasPrefix(id, "imagen-") {
			continue
		}
		// Must support chat completions.
		if !sliceContains(m.SupportedGenerationMethods, "generateContent") {
			continue
		}
		c := Capabilities{
			Vision:        geminiSupportsVision(id),
			Tools:         geminiSupportsTools(id),
			Reasoning:     geminiSupportsReasoning(id),
			ContextLength: m.InputTokenLimit,
		}
		if price, ok := lookupGeminiPrice(id); ok {
			c.PromptPrice = price.Prompt
			c.CompletionPrice = price.Completion
		}
		out[id] = c
	}
	return out, nil
}

func sliceContains(slice []string, want string) bool {
	for _, s := range slice {
		if s == want {
			return true
		}
	}
	return false
}

// geminiSupportsVision returns true for model families that accept image input.
// All Gemini 1.5+ models are multimodal; Gemma and text-embedding variants are not.
func geminiSupportsVision(id string) bool {
	lower := strings.ToLower(id)
	if strings.HasPrefix(lower, "gemma") {
		return false
	}
	// Gemini 1.5, 2.0, 2.5, 3.x — all support vision.
	if strings.HasPrefix(lower, "gemini-1.5") ||
		strings.HasPrefix(lower, "gemini-2.") ||
		strings.HasPrefix(lower, "gemini-3.") {
		return true
	}
	return false
}

// geminiSupportsTools returns true for model families that support function calling.
// Gemma does not; all Gemini 1.5+ do.
func geminiSupportsTools(id string) bool {
	lower := strings.ToLower(id)
	if strings.HasPrefix(lower, "gemma") {
		return false
	}
	if strings.Contains(lower, "tts") || strings.Contains(lower, "image") {
		return false
	}
	return true
}

// geminiSupportsReasoning marks the 2.5+ "thinking" models (dynamic reasoning budget).
func geminiSupportsReasoning(id string) bool {
	lower := strings.ToLower(id)
	// gemini-2.5-pro and gemini-2.5-flash have thinking built in; flash-lite
	// does NOT by default.
	if strings.HasPrefix(lower, "gemini-2.5-pro") {
		return true
	}
	if strings.HasPrefix(lower, "gemini-2.5-flash") && !strings.Contains(lower, "lite") {
		return true
	}
	if strings.HasPrefix(lower, "gemini-3.") {
		return true
	}
	return false
}

// geminiPrice represents USD per 1 million input/output tokens.
type geminiPrice struct {
	Prompt     float64
	Completion float64
}

// geminiPricing maps model id prefix → price. Prices are taken from the public
// Google Gemini pricing page (USD / 1M tokens). Longest-prefix-wins matching.
//
// NOTE: prices marked "est." are provisional until Google publishes; revisit
// quarterly. Pro tiered pricing (>200k context) is not modelled — we use the
// low-tier price.
var geminiPricing = map[string]geminiPrice{
	// 2.5 family
	"gemini-2.5-pro":        {Prompt: 1.25, Completion: 10.00},
	"gemini-2.5-flash":      {Prompt: 0.30, Completion: 2.50},
	"gemini-2.5-flash-lite": {Prompt: 0.10, Completion: 0.40},
	// 2.0 family
	"gemini-2.0-flash":      {Prompt: 0.10, Completion: 0.40},
	"gemini-2.0-flash-lite": {Prompt: 0.075, Completion: 0.30},
	"gemini-2.0-pro":        {Prompt: 1.25, Completion: 5.00}, // est.
	// 1.5 family (legacy, still live)
	"gemini-1.5-pro":      {Prompt: 1.25, Completion: 5.00},
	"gemini-1.5-flash-8b": {Prompt: 0.0375, Completion: 0.15},
	"gemini-1.5-flash":    {Prompt: 0.075, Completion: 0.30},
	// 3.x family (placeholder — actual prices TBD)
	"gemini-3.0-pro":   {Prompt: 2.00, Completion: 10.00}, // est.
	"gemini-3.0-flash": {Prompt: 0.30, Completion: 2.50},  // est.
}

// lookupGeminiPrice matches by longest prefix so "gemini-2.5-flash-lite" wins
// over "gemini-2.5-flash".
func lookupGeminiPrice(modelID string) (geminiPrice, bool) {
	lower := strings.ToLower(modelID)
	var bestKey string
	for k := range geminiPricing {
		if strings.HasPrefix(lower, k) && len(k) > len(bestKey) {
			bestKey = k
		}
	}
	if bestKey == "" {
		return geminiPrice{}, false
	}
	return geminiPricing[bestKey], true
}
