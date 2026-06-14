package llm

import "testing"

func TestLookupAAInfoMatchesXAIProviderAlias(t *testing.T) {
	models := map[string]AAModelInfo{
		"xai/grok-4-3": {Score: 53.2, TTFT: 24.49},
	}

	got := LookupAAInfo("x-ai/grok-4.3", models)
	if got == nil {
		t.Fatal("expected x-ai OpenRouter model to match xai AA entry")
	}
	if got.Score != 53.2 || got.TTFT != 24.49 {
		t.Fatalf("unexpected AA match: %+v", *got)
	}
}
