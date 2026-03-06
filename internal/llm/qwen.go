package llm

import "telegram-agent/internal/config"

func NewQwen(cfg config.ModelConfig) (*openAICompatProvider, error) {
	return newOpenAICompat(cfg, "", "qwen")
}
