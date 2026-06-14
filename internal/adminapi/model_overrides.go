package adminapi

import (
	"context"
	"encoding/json"
	"fmt"

	"telegram-agent/internal/llm"
)

const settingKeyModelOverrides = "recommendation.overrides"

type modelOverride struct {
	State string `json:"state"`
	Note  string `json:"note,omitempty"`
}

func loadModelOverrides(ctx context.Context, settings llm.SettingsStore) map[string]modelOverride {
	if settings == nil {
		return nil
	}
	raw, ok, err := settings.GetSetting(ctx, settingKeyModelOverrides)
	if err != nil || !ok || raw == "" {
		return nil
	}
	out := map[string]modelOverride{}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

func saveModelOverride(ctx context.Context, settings llm.SettingsStore, provider, modelID, state, note string) error {
	if settings == nil {
		return fmt.Errorf("settings store unavailable")
	}
	overrides := loadModelOverrides(ctx, settings)
	if overrides == nil {
		overrides = map[string]modelOverride{}
	}
	key := modelCheckKey(provider, modelID)
	if state == "" {
		delete(overrides, key)
	} else {
		overrides[key] = modelOverride{State: state, Note: note}
	}
	data, err := json.Marshal(overrides)
	if err != nil {
		return err
	}
	return settings.PutSetting(ctx, settingKeyModelOverrides, string(data))
}

func applyModelOverride(provider string, m *uiModel, overrides map[string]modelOverride) bool {
	if len(overrides) == 0 {
		return true
	}
	override := overrides[modelCheckKey(provider, m.ID)]
	switch override.State {
	case "deny":
		m.Policy = "manual_deny"
		m.OverrideNote = override.Note
		m.Warnings = append(m.Warnings, "manual deny")
		if override.Note != "" {
			m.Warnings = append(m.Warnings, override.Note)
		}
	case "allow":
		m.Policy = "manual_allow"
		m.OverrideNote = override.Note
		m.Source = "manual"
		m.Reasons = append(m.Reasons, "manual allow")
		if override.Note != "" {
			m.Reasons = append(m.Reasons, override.Note)
		}
	}
	return true
}

func filterModelOverrides(models []uiModel, provider string, overrides map[string]modelOverride, hideDenied bool) []uiModel {
	if len(models) == 0 || len(overrides) == 0 {
		return models
	}
	out := models[:0]
	for _, m := range models {
		applyModelOverride(provider, &m, overrides)
		if hideDenied && m.Policy == "manual_deny" {
			continue
		}
		out = append(out, m)
	}
	return out
}

func appendAllowedOverrideCandidates(models []uiModel, allCaps map[string]llm.Capabilities, aaModels map[string]llm.AAModelInfo, provider, role string, overrides map[string]modelOverride) []uiModel {
	if len(overrides) == 0 {
		return models
	}
	seen := make(map[string]bool, len(models))
	for _, m := range models {
		seen[m.ID] = true
	}
	for id, override := range overrides {
		if override.State != "allow" {
			continue
		}
		prefix := provider + "|"
		if len(id) <= len(prefix) || id[:len(prefix)] != prefix {
			continue
		}
		modelID := id[len(prefix):]
		if seen[modelID] {
			continue
		}
		c, ok := allCaps[modelID]
		if !ok {
			continue
		}
		m := uiModel{
			ID:              modelID,
			PromptPrice:     c.PromptPrice,
			CompletionPrice: c.CompletionPrice,
			ContextLength:   c.ContextLength,
			Vision:          c.Vision,
			Tools:           c.Tools,
			Reasoning:       c.Reasoning,
			Free:            c.Free(),
			Score:           c.Score,
			Recommended:     false,
		}
		if aaModels != nil {
			if info := llm.LookupAAInfo(modelID, aaModels); info != nil {
				enrichFromAA(&m, *info)
			}
		}
		if m.PromptPrice > 0 {
			q := m.AgenticIndex
			if q == 0 {
				q = m.Score
			}
			if q > 0 {
				m.ValuePerDollar = q / m.PromptPrice
			}
		}
		annotateModelForRole(&m, role, "manual")
		applyModelOverride(provider, &m, overrides)
		models = append(models, m)
		seen[modelID] = true
	}
	return models
}
