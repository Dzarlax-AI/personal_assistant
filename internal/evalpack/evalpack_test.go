package evalpack

import (
	"context"
	"path/filepath"
	"testing"

	"telegram-agent/internal/llm"
)

type mockProvider struct {
	resp llm.Response
	err  error
}

func (p mockProvider) Chat(context.Context, []llm.Message, string, []llm.Tool) (llm.Response, error) {
	return p.resp, p.err
}

func (p mockProvider) Name() string {
	return "mock/test-model"
}

func TestLoadWorkloadSuite(t *testing.T) {
	suite, err := LoadSuite(filepath.Join("..", "..", "evals", "workload.json"))
	if err != nil {
		t.Fatalf("load workload suite: %v", err)
	}
	if suite.Name != "personal-assistant-core-workloads" {
		t.Fatalf("suite name = %q", suite.Name)
	}
	if len(suite.Cases) < 5 {
		t.Fatalf("expected at least 5 workload cases, got %d", len(suite.Cases))
	}
}

func TestEvaluateContentExpectations(t *testing.T) {
	failures := Evaluate(Expect{
		MustContain:    []string{"логи"},
		MustNotContain: []string{"restart blindly"},
		NoToolCall:     true,
	}, llm.Response{Content: "Сначала проверь логи сервиса."})
	if len(failures) != 0 {
		t.Fatalf("expected pass, got failures: %+v", failures)
	}

	failures = Evaluate(Expect{MustContain: []string{"capabilities"}}, llm.Response{Content: "check config"})
	if len(failures) != 1 {
		t.Fatalf("expected one missing-content failure, got: %+v", failures)
	}
}

func TestEvaluateToolCallExpectation(t *testing.T) {
	failures := Evaluate(Expect{ToolCall: "web_fetch"}, llm.Response{
		ToolCalls: []llm.ToolCall{{Name: "web_fetch"}},
	})
	if len(failures) != 0 {
		t.Fatalf("expected tool call pass, got failures: %+v", failures)
	}

	failures = Evaluate(Expect{ToolCall: "web_fetch"}, llm.Response{})
	if len(failures) != 1 {
		t.Fatalf("expected missing tool call failure, got: %+v", failures)
	}
}

func TestRunSuiteAggregatesResults(t *testing.T) {
	suite := Suite{
		Version: 1,
		Name:    "test",
		Cases: []Case{{
			ID:       "case-1",
			Category: "unit",
			Prompt:   "say ok",
			Expect:   Expect{MustContain: []string{"ok"}},
		}},
	}
	provider := mockProvider{resp: llm.Response{Content: "ok"}}
	report := RunSuite(context.Background(), provider, suite, Options{})
	if report.Passed != 1 || report.Failed != 0 || len(report.Results) != 1 {
		t.Fatalf("unexpected report: %+v", report)
	}
}
