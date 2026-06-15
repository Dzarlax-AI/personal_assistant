package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Capabilities describes what a specific model can do and what it costs.
// All fields are populated from provider /models endpoints; when a field is
// unknown (e.g. legacy provider without a models listing) it stays at zero value.
type Capabilities struct {
	Name            string
	Description     string
	Vision          bool
	Tools           bool
	Reasoning       bool
	PromptPrice     float64 // USD per 1M prompt tokens (0 = free or unknown)
	CompletionPrice float64 // USD per 1M completion tokens
	ContextLength   int     // max context in tokens
	Score           float64 // Artificial Analysis Intelligence Index (0 = unknown)
}

// Free reports whether the model can be used without usage cost.
func (c Capabilities) Free() bool {
	return c.PromptPrice == 0 && c.CompletionPrice == 0
}

// CapabilityStore persists per-(provider, model) capabilities across restarts.
// Implementations live in the store package (SQLite, Postgres).
type CapabilityStore interface {
	GetCapabilities(ctx context.Context, provider, modelID string) (Capabilities, bool, error)
	PutCapabilities(ctx context.Context, provider, modelID string, caps Capabilities) error
	GetAllCapabilities(ctx context.Context, provider string) (map[string]Capabilities, error)
}

// SettingsStore is a generic key-value store used for small persisted state
// (routing overrides, feature flags, etc.). Used by the router in place of
// direct file I/O so all persistent state lives in the same DB.
type SettingsStore interface {
	GetSetting(ctx context.Context, key string) (string, bool, error)
	PutSetting(ctx context.Context, key, value string) error
}

// CapabilityProvider is an optional interface providers may implement to
// expose their current model's capabilities to the router.
type CapabilityProvider interface {
	Capabilities() Capabilities
}

// ConfigurableProvider is an optional interface providers may implement to
// allow runtime model swaps (used by the admin UI).
type ConfigurableProvider interface {
	SetModel(modelID string, caps Capabilities)
	CurrentModel() string
}

// --- OpenRouter /models fetcher ---

var (
	openRouterHTTPClient         = &http.Client{Timeout: 20 * time.Second}
	openRouterPageBaseURL        = "https://openrouter.ai"
	openRouterDescriptionPayload = regexp.MustCompile(`\\"description\\":\\"((?:\\\\.|[^\\"])*)\\"`)
)

// FetchOpenRouterModels pulls the full catalog from OpenRouter and returns a
// map keyed by model id. Prices are normalised to USD per 1M tokens (OpenRouter
// returns USD per token).
func FetchOpenRouterModels(ctx context.Context, apiKey string) (map[string]Capabilities, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://openrouter.ai/api/v1/models", nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := openRouterHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openrouter /models: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("openrouter /models: read: %w", err)
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("openrouter /models: HTTP %d: %s", resp.StatusCode, string(body))
	}
	return parseOpenRouterModels(body)
}

type openRouterModelsResponse struct {
	Data []struct {
		ID            string `json:"id"`
		Name          string `json:"name"`
		Description   string `json:"description"`
		ContextLength int    `json:"context_length"`
		Architecture  struct {
			InputModalities  []string `json:"input_modalities"`
			OutputModalities []string `json:"output_modalities"`
		} `json:"architecture"`
		Pricing struct {
			Prompt     string `json:"prompt"`
			Completion string `json:"completion"`
		} `json:"pricing"`
		SupportedParameters []string `json:"supported_parameters"`
	} `json:"data"`
}

func parseOpenRouterModels(body []byte) (map[string]Capabilities, error) {
	var resp openRouterModelsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("openrouter /models: parse: %w", err)
	}
	out := make(map[string]Capabilities, len(resp.Data))
	for _, m := range resp.Data {
		if m.ID == "" {
			continue
		}
		out[m.ID] = Capabilities{
			Name:            strings.TrimSpace(m.Name),
			Description:     strings.TrimSpace(m.Description),
			Vision:          containsStr(m.Architecture.InputModalities, "image"),
			Tools:           containsStr(m.SupportedParameters, "tools"),
			Reasoning:       containsStr(m.SupportedParameters, "reasoning"),
			PromptPrice:     parsePricePerMillion(m.Pricing.Prompt),
			CompletionPrice: parsePricePerMillion(m.Pricing.Completion),
			ContextLength:   m.ContextLength,
		}
	}
	return out, nil
}

// OpenRouterDescriptionLooksTruncated reports descriptions that already arrive
// from OpenRouter with an ellipsis. Those are upstream-shortened strings, not a
// UI clamp, so callers may optionally enrich them from the model page payload.
func OpenRouterDescriptionLooksTruncated(desc string) bool {
	desc = strings.TrimSpace(desc)
	return strings.HasSuffix(desc, "...") || strings.HasSuffix(desc, "…")
}

// FetchOpenRouterModelDescription fetches a model page and extracts the longest
// matching description from its SSR payload. OpenRouter's public /models API can
// return shortened descriptions ending in "...", while the model page payload
// usually carries the full text.
func FetchOpenRouterModelDescription(ctx context.Context, modelID, currentDescription string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, openRouterModelPageURL(modelID), nil)
	if err != nil {
		return "", err
	}
	resp, err := openRouterHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("openrouter model page: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if err != nil {
		return "", fmt.Errorf("openrouter model page: read: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("openrouter model page: HTTP %d: %s", resp.StatusCode, string(body))
	}
	full := extractOpenRouterPageDescription(body, currentDescription)
	if full == "" {
		return "", fmt.Errorf("openrouter model page: full description not found")
	}
	return full, nil
}

func openRouterModelPageURL(modelID string) string {
	parts := strings.Split(modelID, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return strings.TrimRight(openRouterPageBaseURL, "/") + "/" + strings.Join(parts, "/")
}

func extractOpenRouterPageDescription(body []byte, currentDescription string) string {
	current := normalizeDescriptionText(currentDescription)
	prefix := strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(current, "..."), "…"))
	if len(prefix) > 120 {
		prefix = prefix[:120]
	}
	var best string
	for _, m := range openRouterDescriptionPayload.FindAllSubmatch(body, -1) {
		if len(m) != 2 {
			continue
		}
		raw := string(m[1])
		decoded, err := strconv.Unquote(`"` + raw + `"`)
		if err != nil {
			continue
		}
		decoded = normalizeDescriptionText(html.UnescapeString(decoded))
		if decoded == "" || len(decoded) <= len(current) || OpenRouterDescriptionLooksTruncated(decoded) {
			continue
		}
		if prefix != "" && !strings.HasPrefix(decoded, prefix) {
			continue
		}
		if len(decoded) > len(best) {
			best = decoded
		}
	}
	return best
}

func normalizeDescriptionText(s string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
}

func containsStr(s []string, target string) bool {
	for _, v := range s {
		if strings.EqualFold(v, target) {
			return true
		}
	}
	return false
}

// parsePricePerMillion converts OpenRouter's "USD per token" string to USD per 1M tokens.
// Returns 0 on parse failure or explicit zero; treating unknown as free matches
// the common :free model convention.
func parsePricePerMillion(s string) float64 {
	if s == "" {
		return 0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return f * 1_000_000
}
