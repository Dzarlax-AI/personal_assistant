package llm

import "testing"

func TestParseOpenRouterRankingSignalsFlexibleShapes(t *testing.T) {
	body := []byte(`{
		"data": [
			{"model_id": "deepseek/deepseek-v3.2", "rank": 2, "tokenShare": 12.5},
			{"model": {"id": "x-ai/grok-4.3"}, "value": 4200},
			{"id": "not-a-model"},
			{"slug": "google/gemini-2.5-pro", "percentage": "7.25"}
		]
	}`)

	got, err := parseOpenRouterRankingSignals(body, "tools")
	if err != nil {
		t.Fatalf("parse rankings: %v", err)
	}
	if got["deepseek/deepseek-v3.2"].Rank != 2 {
		t.Fatalf("deepseek rank = %d, want 2", got["deepseek/deepseek-v3.2"].Rank)
	}
	if got["deepseek/deepseek-v3.2"].Share != 12.5 {
		t.Fatalf("deepseek share = %v, want 12.5", got["deepseek/deepseek-v3.2"].Share)
	}
	if got["x-ai/grok-4.3"].Score != 4200 {
		t.Fatalf("grok score = %v, want 4200", got["x-ai/grok-4.3"].Score)
	}
	if got["google/gemini-2.5-pro"].Share != 7.25 {
		t.Fatalf("gemini share = %v, want 7.25", got["google/gemini-2.5-pro"].Share)
	}
	if _, ok := got["not-a-model"]; ok {
		t.Fatalf("invalid model id should be ignored: %+v", got["not-a-model"])
	}
}

func TestMergeMarketSignalsKeepsBestRankAndCategories(t *testing.T) {
	dst := map[string]OpenRouterMarketSignal{
		"x-ai/grok-4.3": {ModelID: "x-ai/grok-4.3", Rank: 5, Source: "tools", Categories: []string{"tools"}},
	}
	src := map[string]OpenRouterMarketSignal{
		"x-ai/grok-4.3": {ModelID: "x-ai/grok-4.3", Rank: 2, Share: 3.5, Source: "programming"},
	}

	mergeMarketSignals(dst, src)
	got := dst["x-ai/grok-4.3"]
	if got.Rank != 2 {
		t.Fatalf("rank = %d, want 2", got.Rank)
	}
	if got.Share != 3.5 {
		t.Fatalf("share = %v, want 3.5", got.Share)
	}
	if len(got.Categories) != 2 {
		t.Fatalf("categories = %+v, want two categories", got.Categories)
	}
}
