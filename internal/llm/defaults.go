package llm

import (
	"os"

	"telegram-agent/internal/config"
)

// Default base URLs for each provider type. The ones that never change
// (OpenRouter, Google) are hardcoded; Docker-host-bound ones (Claude Bridge,
// Ollama) read an env var first so non-Docker deployments can override.
const (
	DefaultOpenRouterBaseURL = "https://openrouter.ai/api/v1"
	// GeminiNativeProvider has its own endpoint constant — this is only used
	// if someone ever wires Gemini through an OpenAI-compat proxy.
	DefaultGeminiOpenAIBaseURL = "https://generativelanguage.googleapis.com/v1beta/openai/"
	// DefaultClaudeBridgeURL assumes bot-in-Docker + bridge-on-host; override
	// with CLAUDE_BRIDGE_URL for other topologies.
	DefaultClaudeBridgeURL = "http://host.docker.internal:9900"
	DefaultOllamaBaseURL   = "http://host.docker.internal:11434"
)

// ApplyProviderDefaults fills in fields that have well-known defaults when
// they're absent from the config entry. Returns a copy — the original is
// never mutated so callers that keep cfg.Models references around don't
// see surprise changes.
//
// Current defaults:
//   - openrouter   → base_url = OPENROUTER_BASE_URL env or DefaultOpenRouterBaseURL
//   - claude-bridge→ base_url = CLAUDE_BRIDGE_URL env or DefaultClaudeBridgeURL
//   - ollama       → base_url = OLLAMA_BASE_URL env or DefaultOllamaBaseURL
//   - gemini       → base_url is unused by the native provider (kept for the
//     openai-compat variant only).
//
// Secrets (api_key) are NOT filled here — callers should already expand
// ${ENV_VAR} before reaching this function.
func ApplyProviderDefaults(cfg config.ModelConfig) config.ModelConfig {
	if cfg.BaseURL != "" {
		return cfg
	}
	switch cfg.Provider {
	case "openrouter":
		cfg.BaseURL = firstNonEmpty(os.Getenv("OPENROUTER_BASE_URL"), DefaultOpenRouterBaseURL)
	case "claude-bridge":
		cfg.BaseURL = firstNonEmpty(os.Getenv("CLAUDE_BRIDGE_URL"), DefaultClaudeBridgeURL)
	case "ollama":
		cfg.BaseURL = firstNonEmpty(os.Getenv("OLLAMA_BASE_URL"), DefaultOllamaBaseURL)
	case "gemini":
		// Native provider ignores BaseURL. Keep empty.
	}
	return cfg
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
