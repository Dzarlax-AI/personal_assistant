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
	if suite.Name != "personal-assistant-role-workloads" {
		t.Fatalf("suite name = %q", suite.Name)
	}
	if suite.Version != 2 {
		t.Fatalf("suite version = %d", suite.Version)
	}
	if len(suite.Cases) < 12 {
		t.Fatalf("expected at least 12 workload cases, got %d", len(suite.Cases))
	}
	if suite.Cases[0].Purpose == "" {
		t.Fatal("workload cases should describe their purpose")
	}
}

func TestEvaluateContentExpectations(t *testing.T) {
	failures := Evaluate(Expect{
		MustContain:    []string{"логи"},
		MustContainAny: [][]string{{"сервис", "контейнер"}},
		MustNotContain: []string{"restart blindly"},
		NoToolCall:     true,
		MaxChars:       80,
	}, llm.Response{Content: "Сначала проверь логи сервиса."})
	if len(failures) != 0 {
		t.Fatalf("expected pass, got failures: %+v", failures)
	}

	failures = Evaluate(Expect{MustContain: []string{"capabilities"}}, llm.Response{Content: "check config"})
	if len(failures) != 1 {
		t.Fatalf("expected one missing-content failure, got: %+v", failures)
	}

	failures = Evaluate(Expect{MustContainAny: [][]string{{"model_capabilities", "кэш моделей"}}}, llm.Response{Content: "Проверь кэш моделей в базе."})
	if len(failures) != 0 {
		t.Fatalf("expected synonym group pass, got failures: %+v", failures)
	}

	failures = Evaluate(Expect{MaxChars: 5}, llm.Response{Content: "слишком длинно"})
	if len(failures) != 1 {
		t.Fatalf("expected max chars failure, got: %+v", failures)
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

func TestEvaluateToolArgsExpectation(t *testing.T) {
	failures := Evaluate(Expect{
		ToolCall: "calendar__get_events",
		ToolArgs: map[string]string{
			"date_from": "2026-06-17T00:00:00+02:00",
			"date_to":   "2026-06-17T12:00:00+02:00",
		},
		MaxToolCalls: 1,
	}, llm.Response{
		ToolCalls: []llm.ToolCall{{
			Name:      "calendar__get_events",
			Arguments: `{"date_from":"2026-06-17T00:00:00+02:00","date_to":"2026-06-17T12:00:00+02:00"}`,
		}},
	})
	if len(failures) != 0 {
		t.Fatalf("expected tool args pass, got failures: %+v", failures)
	}

	failures = Evaluate(Expect{
		ToolCall: "calendar__get_events",
		ToolArgs: map[string]string{"date_from": "2026-06-17T00:00:00+02:00"},
	}, llm.Response{
		ToolCalls: []llm.ToolCall{{
			Name:      "calendar__get_events",
			Arguments: `{"date_from":"2026-06-18T00:00:00+02:00"}`,
		}},
	})
	if len(failures) != 1 {
		t.Fatalf("expected tool arg mismatch failure, got: %+v", failures)
	}

	failures = Evaluate(Expect{MaxToolCalls: 1}, llm.Response{
		ToolCalls: []llm.ToolCall{{Name: "a"}, {Name: "b"}},
	})
	if len(failures) != 1 {
		t.Fatalf("expected max tool calls failure, got: %+v", failures)
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
