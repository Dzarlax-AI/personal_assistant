package adminapi

import (
	"testing"

	"telegram-agent/internal/llm"
)

func TestDefaultPresetExcludesLightweightSimpleModels(t *testing.T) {
	caps := map[string]llm.Capabilities{
		"qwen/qwen3.5-flash": {
			Tools:           true,
			PromptPrice:     0.10,
			CompletionPrice: 0.40,
			ContextLength:   64000,
			Score:           82,
		},
		"qwen/qwen3.5-plus": {
			Tools:           true,
			PromptPrice:     1.00,
			CompletionPrice: 3.00,
			ContextLength:   128000,
			Score:           88,
		},
	}
	aa := map[string]llm.AAModelInfo{
		"qwen/qwen3-5-flash": {AgenticIndex: 42, TTFT: 0.35},
		"qwen/qwen3-5-plus":  {AgenticIndex: 58, TTFT: 0.95},
	}

	simple := applyPreset(caps, aa, "simple", 0)
	if !containsModel(simple, "qwen/qwen3.5-flash") {
		t.Fatalf("simple preset should include lightweight flash model: %+v", simple)
	}

	def := applyPreset(caps, aa, "default", 0)
	if containsModel(def, "qwen/qwen3.5-flash") {
		t.Fatalf("default preset should not duplicate lightweight simple model: %+v", def)
	}
	if !containsModel(def, "qwen/qwen3.5-plus") {
		t.Fatalf("default preset should include workhorse model: %+v", def)
	}
}

func TestSimplePresetUsesTTFTWhenAvailable(t *testing.T) {
	caps := map[string]llm.Capabilities{
		"qwen/qwen3.5-flash": {
			Tools:           true,
			PromptPrice:     0.10,
			CompletionPrice: 0.40,
			ContextLength:   64000,
			Score:           82,
		},
		"qwen/qwen3.5-turbo": {
			Tools:           true,
			PromptPrice:     0.10,
			CompletionPrice: 0.40,
			ContextLength:   64000,
			Score:           84,
		},
	}
	aa := map[string]llm.AAModelInfo{
		"qwen/qwen3-5-flash": {AgenticIndex: 40, TTFT: 0.30},
		"qwen/qwen3-5-turbo": {AgenticIndex: 45, TTFT: 0.80},
	}

	got := applyPreset(caps, aa, "simple", 0)
	if len(got) == 0 {
		t.Fatal("simple preset returned no models")
	}
	if got[0].ID != "qwen/qwen3.5-flash" {
		t.Fatalf("simple preset should sort by TTFT before agentic quality, got: %+v", got)
	}
}

func TestPresetKeepsUntestedCandidatesBelowRecommendations(t *testing.T) {
	caps := map[string]llm.Capabilities{
		"deepseek/deepseek-v3.2": {
			Tools:           true,
			PromptPrice:     0.25,
			CompletionPrice: 1.00,
			ContextLength:   128000,
			Score:           32,
		},
		"x-ai/grok-4-fast": {
			Tools:           true,
			Vision:          true,
			Reasoning:       true,
			PromptPrice:     1.00,
			CompletionPrice: 2.00,
			ContextLength:   256000,
			Score:           0,
		},
	}

	got := applyPreset(caps, nil, "default", 0)
	if !containsModel(got, "x-ai/grok-4-fast") {
		t.Fatalf("untested capability-compatible model should remain visible: %+v", got)
	}
	for _, model := range got {
		if model.ID != "x-ai/grok-4-fast" {
			continue
		}
		if model.Recommended || model.Source != "untested" || model.Policy != "candidate" {
			t.Fatalf("untested model should be a low-priority candidate, got: %+v", model)
		}
		return
	}
}

func TestPresetUsesAAScoreWhenCapabilityScoreIsEmpty(t *testing.T) {
	caps := map[string]llm.Capabilities{
		"x-ai/grok-4.3": {
			Tools:           true,
			Vision:          true,
			Reasoning:       true,
			PromptPrice:     1.25,
			CompletionPrice: 2.50,
			ContextLength:   1000000,
			Score:           0,
		},
	}
	aa := map[string]llm.AAModelInfo{
		"xai/grok-4-3": {Score: 53.2, TTFT: 24.49},
	}

	got := applyPreset(caps, aa, "default", 0)
	if len(got) != 1 {
		t.Fatalf("models len = %d, want 1: %+v", len(got), got)
	}
	if got[0].Score != 53.2 || !got[0].Recommended {
		t.Fatalf("AA score should drive recommendation when cap score is empty: %+v", got[0])
	}
}

func TestStablePresetsExcludePreviewVariants(t *testing.T) {
	caps := map[string]llm.Capabilities{
		"google/gemini-3.1-pro-preview": {
			Tools:           true,
			Vision:          true,
			PromptPrice:     2.00,
			CompletionPrice: 12.00,
			ContextLength:   1000000,
			Score:           58,
		},
		"google/gemini-3-flash": {
			Tools:           true,
			Vision:          true,
			PromptPrice:     0.50,
			CompletionPrice: 3.00,
			ContextLength:   1000000,
			Score:           35,
		},
		"x-ai/grok-4.3": {
			Tools:           true,
			Vision:          true,
			Reasoning:       true,
			PromptPrice:     1.25,
			CompletionPrice: 2.50,
			ContextLength:   1000000,
			Score:           53,
		},
	}

	for _, role := range []string{"default", "complex", "multimodal", "compaction"} {
		got := applyPreset(caps, nil, role, 0)
		if containsModel(got, "google/gemini-3.1-pro-preview") {
			t.Fatalf("%s preset should exclude preview variants: %+v", role, got)
		}
	}
	if !containsModel(applyPreset(caps, nil, "default", 0), "x-ai/grok-4.3") {
		t.Fatalf("stable Grok should still pass default preset")
	}
}

func TestDefaultPresetRanksByBlendedCost(t *testing.T) {
	caps := map[string]llm.Capabilities{
		"deepseek/deepseek-v3.2": {
			Tools:           true,
			PromptPrice:     0.50,
			CompletionPrice: 1.00,
			ContextLength:   128000,
			Score:           40,
		},
		"x-ai/grok-4.3": {
			Tools:           true,
			Vision:          true,
			Reasoning:       true,
			PromptPrice:     0.50,
			CompletionPrice: 8.00,
			ContextLength:   1000000,
			Score:           40,
		},
	}

	got := applyPreset(caps, nil, "default", 0)
	if !containsModel(got, "deepseek/deepseek-v3.2") {
		t.Fatalf("cheaper blended-cost model should remain: %+v", got)
	}
	for _, model := range got {
		if model.ID == "x-ai/grok-4.3" && model.Recommended {
			t.Fatalf("higher completion-cost model should not be recommended at equal quality: %+v", got)
		}
	}
}

func containsModel(models []uiModel, id string) bool {
	for _, m := range models {
		if m.ID == id {
			return true
		}
	}
	return false
}
