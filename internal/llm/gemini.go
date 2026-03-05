package llm

import "telegram-agent/internal/config"

func NewGemini(cfg config.ModelConfig) (*openAICompatProvider, error) {
	return newOpenAICompat(cfg, "", "gemini")
}

// NewGeminiMultimodal creates a Gemini provider with vision support enabled.
func NewGeminiMultimodal(cfg config.ModelConfig) (*openAICompatProvider, error) {
	return newOpenAICompat(cfg, "", "gemini", true)
}
