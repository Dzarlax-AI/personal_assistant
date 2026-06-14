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

func TestModelSectionsSplitRecommendationBuckets(t *testing.T) {
	models := []uiModel{
		{ID: "deepseek/deepseek-v3.2", Recommended: true, Section: "recommended", Policy: "recommended"},
		{ID: "x-ai/grok-4.3", Source: "near_frontier", Section: "interesting", Policy: "candidate"},
		{ID: "qwen/qwen-plus", Source: "untested", Section: "untested", Policy: "candidate"},
	}

	sections := buildModelSections(models, true)
	if len(sections) != 3 {
		t.Fatalf("sections len = %d, want 3: %+v", len(sections), sections)
	}
	if sections[0].Key != "recommended" || sections[0].Models[0].ID != "deepseek/deepseek-v3.2" {
		t.Fatalf("unexpected recommended section: %+v", sections)
	}
	if sections[1].Key != "interesting" || sections[1].Models[0].ID != "x-ai/grok-4.3" {
		t.Fatalf("unexpected interesting section: %+v", sections)
	}
	if sections[2].Key != "untested" || sections[2].Models[0].ID != "qwen/qwen-plus" {
		t.Fatalf("unexpected untested section: %+v", sections)
	}
}

func TestSectionDiversityCapsModelFamilies(t *testing.T) {
	models := []uiModel{
		{ID: "google/gemini-2.5-pro"},
		{ID: "google/gemini-2.5-flash"},
		{ID: "google/gemini-3-flash"},
		{ID: "google/gemini-3.1-flash"},
		{ID: "x-ai/grok-4.3"},
	}

	got := applySectionDiversity(models, 3)
	if len(got) != 4 {
		t.Fatalf("diversity len = %d, want 4: %+v", len(got), got)
	}
	if containsModel(got, "google/gemini-3.1-flash") {
		t.Fatalf("fourth Gemini family model should be hidden by visible-section diversity cap: %+v", got)
	}
	if !containsModel(got, "x-ai/grok-4.3") {
		t.Fatalf("non-Gemini alternative should remain visible: %+v", got)
	}
}

func TestDefaultPresetUsesCodingWhenAgenticIsMissing(t *testing.T) {
	caps := map[string]llm.Capabilities{
		"x-ai/grok-4.3": {
			Tools:           true,
			Vision:          true,
			Reasoning:       true,
			PromptPrice:     1.00,
			CompletionPrice: 3.00,
			ContextLength:   1000000,
			Score:           45,
		},
		"google/gemini-2.5-pro": {
			Tools:           true,
			Vision:          true,
			PromptPrice:     1.00,
			CompletionPrice: 3.00,
			ContextLength:   1000000,
			Score:           60,
		},
	}
	aa := map[string]llm.AAModelInfo{
		"xai/grok-4-3":         {Score: 45, CodingIndex: 72},
		"google/gemini-25-pro": {Score: 60},
	}

	got := applyPreset(caps, aa, "default", 0)
	if len(got) == 0 || got[0].ID != "x-ai/grok-4.3" || !got[0].Recommended {
		t.Fatalf("default preset should use coding as a role quality signal: %+v", got)
	}
	if label := roleQualityLabel(got[0], "default"); label != "AA coding 72" {
		t.Fatalf("quality label = %q, want AA coding 72", label)
	}
}

func TestSimplePresetUsesThroughputWhenTTFTIsMissing(t *testing.T) {
	caps := map[string]llm.Capabilities{
		"x-ai/grok-4-fast": {
			Tools:           true,
			Vision:          true,
			Reasoning:       true,
			PromptPrice:     0.20,
			CompletionPrice: 0.50,
			ContextLength:   256000,
			Score:           20,
		},
	}
	aa := map[string]llm.AAModelInfo{
		"xai/grok-4-fast": {Score: 20, SpeedTPS: 180},
	}

	got := applyPreset(caps, aa, "simple", 0)
	if len(got) != 1 || got[0].ID != "x-ai/grok-4-fast" || !got[0].Recommended {
		t.Fatalf("simple preset should recommend throughput-ranked candidates without TTFT: %+v", got)
	}
	if label := roleQualityLabel(got[0], "simple"); label != "AA speed 180 t/s" {
		t.Fatalf("quality label = %q, want AA speed 180 t/s", label)
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
