package main

import "testing"

func TestEnforceCostGuardAllowsFreeAndLocalByDefault(t *testing.T) {
	if err := enforceCostGuard("openrouter", "google/gemma-3-27b-it:free", false); err != nil {
		t.Fatalf("free OpenRouter model should pass: %v", err)
	}
	if err := enforceCostGuard("ollama", "qwen3:8b", false); err != nil {
		t.Fatalf("ollama model should pass: %v", err)
	}
	if err := enforceCostGuard("local", "local-model", false); err != nil {
		t.Fatalf("local model should pass: %v", err)
	}
}

func TestEnforceCostGuardBlocksPaidCloudByDefault(t *testing.T) {
	if err := enforceCostGuard("openrouter", "x-ai/grok-4.3", false); err == nil {
		t.Fatal("paid OpenRouter model should require --allow-paid")
	}
	if err := enforceCostGuard("gemini", "gemini-2.5-flash", false); err == nil {
		t.Fatal("cloud provider should require --allow-paid")
	}
}

func TestEnforceCostGuardAllowsPaidWhenExplicit(t *testing.T) {
	if err := enforceCostGuard("openrouter", "x-ai/grok-4.3", true); err != nil {
		t.Fatalf("explicit paid run should pass: %v", err)
	}
}
