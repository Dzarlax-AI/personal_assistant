package llm

import "telegram-agent/internal/config"

// NewLocal creates an OpenAI-compatible provider for a local model (e.g. llama.cpp server).
// Does not require an API key — only base_url and model are needed.
func NewLocal(cfg config.ModelConfig) (*openAICompatProvider, error) {
	return newOpenAICompat(cfg, "", "local")
}
