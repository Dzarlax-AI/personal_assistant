package llm

import (
	"fmt"

	"telegram-agent/internal/config"
)

// BackendFactory creates a Provider instance bound to a specific model id,
// reusing shared auth/base URL config captured at registration time. Used by
// the router to rebuild a slot's provider when the admin UI swaps its backend
// type (e.g. from OpenRouter to Gemini).
//
// Each factory captures one set of credentials — typically from the first
// config entry of that provider type. Multiple API keys per type are not
// supported at the factory level; users that need distinct keys should define
// multiple slots in config and pin them in the UI.
type BackendFactory func(modelID string, caps Capabilities) (Provider, error)

// BuildBackendFactories walks cfg.Models once and returns a factory for every
// provider type it finds. The first entry per type wins for shared fields
// (api_key, base_url, max_tokens). Factories are keyed by the literal
// provider string used in config ("openrouter", "gemini", etc.).
//
// hf-tei and local embedding providers are skipped — they are not LLM slots.
func BuildBackendFactories(cfg *config.Config) map[string]BackendFactory {
	out := map[string]BackendFactory{}
	seen := map[string]bool{}
	for _, mc := range cfg.Models {
		if seen[mc.Provider] {
			continue
		}
		mc = ApplyProviderDefaults(mc)
		switch mc.Provider {
		case "openrouter":
			base := mc
			out["openrouter"] = func(modelID string, caps Capabilities) (Provider, error) {
				cp := base
				cp.Model = modelID
				p, err := NewOpenRouter(cp)
				if err != nil {
					return nil, err
				}
				p.SetModel(modelID, caps)
				return p, nil
			}
			seen[mc.Provider] = true
		case "gemini":
			base := mc
			out["gemini"] = func(modelID string, caps Capabilities) (Provider, error) {
				cp := base
				cp.Model = modelID
				p, err := NewGeminiNative(cp)
				if err != nil {
					return nil, err
				}
				p.SetModel(modelID, caps)
				return p, nil
			}
			seen[mc.Provider] = true
		case "claude-bridge":
			base := mc
			out["claude-bridge"] = func(modelID string, _ Capabilities) (Provider, error) {
				cp := base
				if modelID != "" {
					cp.Model = modelID
				}
				return NewClaudeBridge(cp)
			}
			seen[mc.Provider] = true
		case "ollama":
			base := mc
			out["ollama"] = func(modelID string, _ Capabilities) (Provider, error) {
				cp := base
				cp.Model = modelID
				return NewOllama(cp)
			}
			seen[mc.Provider] = true
		case "local":
			base := mc
			out["local"] = func(modelID string, _ Capabilities) (Provider, error) {
				cp := base
				cp.Model = modelID
				return NewLocal(cp)
			}
			seen[mc.Provider] = true
			// hf-tei / openai embedding providers are intentionally skipped.
		}
	}
	return out
}

// SwitchableSlot is an optional interface routingSlot-style providers can
// implement so the router knows their current provider type. Used when
// persisting slot state and when deciding whether SetProviderModel needs
// to call the factory vs. a simple SetModel.
type SwitchableSlot interface {
	ProviderType() string
}

// factoryForType returns a helpful error when the requested provider type
// has no registered factory (typical cause: the config doesn't define any
// slot of that type, so credentials were never wired up).
func factoryForType(factories map[string]BackendFactory, providerType string) (BackendFactory, error) {
	if factories == nil {
		return nil, fmt.Errorf("no backend factories registered")
	}
	f, ok := factories[providerType]
	if !ok {
		return nil, fmt.Errorf("no factory for provider type %q — add at least one slot of that type to config", providerType)
	}
	return f, nil
}
