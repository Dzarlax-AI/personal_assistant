package evalpack

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"telegram-agent/internal/llm"
)

type Suite struct {
	Version int    `json:"version"`
	Name    string `json:"name"`
	Cases   []Case `json:"cases"`
}

type Case struct {
	ID          string   `json:"id"`
	Category    string   `json:"category"`
	Purpose     string   `json:"purpose,omitempty"`
	Limitations []string `json:"limitations,omitempty"`

	SystemPrompt string     `json:"system_prompt,omitempty"`
	Prompt       string     `json:"prompt"`
	Tools        []llm.Tool `json:"tools,omitempty"`
	Expect       Expect     `json:"expect"`
}

type Expect struct {
	MustContain    []string   `json:"must_contain,omitempty"`
	MustContainAny [][]string `json:"must_contain_any,omitempty"`
	MustNotContain []string   `json:"must_not_contain,omitempty"`

	ToolCall     string            `json:"tool_call,omitempty"`
	ToolArgs     map[string]string `json:"tool_args,omitempty"`
	NoToolCall   bool              `json:"no_tool_call,omitempty"`
	MaxToolCalls int               `json:"max_tool_calls,omitempty"`
	MaxChars     int               `json:"max_chars,omitempty"`
}

type Options struct {
	Timeout time.Duration
}

type Result struct {
	ID             string   `json:"id"`
	Category       string   `json:"category"`
	Passed         bool     `json:"passed"`
	LatencyMS      int64    `json:"latency_ms"`
	ContentPreview string   `json:"content_preview,omitempty"`
	ToolCalls      []string `json:"tool_calls,omitempty"`
	Failures       []string `json:"failures,omitempty"`
	Error          string   `json:"error,omitempty"`
}

type Report struct {
	Suite      string    `json:"suite"`
	Model      string    `json:"model"`
	Provider   string    `json:"provider"`
	StartedAt  time.Time `json:"started_at"`
	DurationMS int64     `json:"duration_ms"`
	Passed     int       `json:"passed"`
	Failed     int       `json:"failed"`
	Results    []Result  `json:"results"`
}

func LoadSuite(path string) (Suite, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Suite{}, err
	}
	var suite Suite
	if err := json.Unmarshal(data, &suite); err != nil {
		return Suite{}, fmt.Errorf("parse eval suite: %w", err)
	}
	if err := ValidateSuite(suite); err != nil {
		return Suite{}, err
	}
	return suite, nil
}

func ValidateSuite(suite Suite) error {
	if suite.Version <= 0 {
		return fmt.Errorf("eval suite version is required")
	}
	if strings.TrimSpace(suite.Name) == "" {
		return fmt.Errorf("eval suite name is required")
	}
	if len(suite.Cases) == 0 {
		return fmt.Errorf("eval suite has no cases")
	}
	seen := map[string]bool{}
	for i, c := range suite.Cases {
		if strings.TrimSpace(c.ID) == "" {
			return fmt.Errorf("case %d has empty id", i)
		}
		if seen[c.ID] {
			return fmt.Errorf("duplicate case id %q", c.ID)
		}
		seen[c.ID] = true
		if strings.TrimSpace(c.Category) == "" {
			return fmt.Errorf("case %q has empty category", c.ID)
		}
		if strings.TrimSpace(c.Prompt) == "" {
			return fmt.Errorf("case %q has empty prompt", c.ID)
		}
		if c.Expect.MaxToolCalls < 0 {
			return fmt.Errorf("case %q has negative max_tool_calls", c.ID)
		}
		if c.Expect.MaxChars < 0 {
			return fmt.Errorf("case %q has negative max_chars", c.ID)
		}
		if len(c.Expect.ToolArgs) > 0 && strings.TrimSpace(c.Expect.ToolCall) == "" {
			return fmt.Errorf("case %q has tool_args without tool_call", c.ID)
		}
		for groupIdx, group := range c.Expect.MustContainAny {
			if len(group) == 0 {
				return fmt.Errorf("case %q has empty must_contain_any group %d", c.ID, groupIdx)
			}
		}
		for _, tool := range c.Tools {
			if strings.TrimSpace(tool.Name) == "" {
				return fmt.Errorf("case %q has tool with empty name", c.ID)
			}
			if len(tool.InputSchema) > 0 && !json.Valid(tool.InputSchema) {
				return fmt.Errorf("case %q tool %q has invalid input schema", c.ID, tool.Name)
			}
		}
	}
	return nil
}

func RunSuite(ctx context.Context, provider llm.Provider, suite Suite, opts Options) Report {
	start := time.Now()
	report := Report{
		Suite:     suite.Name,
		Model:     provider.Name(),
		Provider:  provider.Name(),
		StartedAt: start,
		Results:   make([]Result, 0, len(suite.Cases)),
	}
	for _, c := range suite.Cases {
		result := RunCase(ctx, provider, c, opts)
		if result.Passed {
			report.Passed++
		} else {
			report.Failed++
		}
		report.Results = append(report.Results, result)
	}
	report.DurationMS = time.Since(start).Milliseconds()
	return report
}

func RunCase(ctx context.Context, provider llm.Provider, c Case, opts Options) Result {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	caseCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	resp, err := provider.Chat(caseCtx, []llm.Message{{Role: "user", Content: c.Prompt}}, c.SystemPrompt, c.Tools)
	result := Result{
		ID:        c.ID,
		Category:  c.Category,
		LatencyMS: time.Since(start).Milliseconds(),
	}
	if err != nil {
		result.Error = err.Error()
		result.Failures = append(result.Failures, "provider error")
		return result
	}
	result.ContentPreview = preview(resp.Content, 500)
	for _, call := range resp.ToolCalls {
		result.ToolCalls = append(result.ToolCalls, call.Name)
	}
	result.Failures = Evaluate(c.Expect, resp)
	result.Passed = len(result.Failures) == 0
	return result
}

func Evaluate(expect Expect, resp llm.Response) []string {
	var failures []string
	content := strings.ToLower(resp.Content)
	for _, want := range expect.MustContain {
		if !strings.Contains(content, strings.ToLower(want)) {
			failures = append(failures, fmt.Sprintf("missing content %q", want))
		}
	}
	for _, group := range expect.MustContainAny {
		if !containsAny(content, group) {
			failures = append(failures, fmt.Sprintf("missing one of %q", group))
		}
	}
	for _, deny := range expect.MustNotContain {
		if strings.Contains(content, strings.ToLower(deny)) {
			failures = append(failures, fmt.Sprintf("forbidden content %q", deny))
		}
	}
	var matchedCall *llm.ToolCall
	if expect.ToolCall != "" {
		matchedCall = findToolCall(resp.ToolCalls, expect.ToolCall)
		if matchedCall == nil {
			failures = append(failures, fmt.Sprintf("missing tool call %q", expect.ToolCall))
		}
	}
	if matchedCall != nil && len(expect.ToolArgs) > 0 {
		failures = append(failures, evaluateToolArgs(expect.ToolCall, expect.ToolArgs, matchedCall.Arguments)...)
	}
	if expect.NoToolCall && len(resp.ToolCalls) > 0 {
		failures = append(failures, "unexpected tool call")
	}
	if expect.MaxToolCalls > 0 && len(resp.ToolCalls) > expect.MaxToolCalls {
		failures = append(failures, fmt.Sprintf("too many tool calls: got %d, max %d", len(resp.ToolCalls), expect.MaxToolCalls))
	}
	if expect.MaxChars > 0 && len([]rune(strings.TrimSpace(resp.Content))) > expect.MaxChars {
		failures = append(failures, fmt.Sprintf("response too long: got %d chars, max %d", len([]rune(strings.TrimSpace(resp.Content))), expect.MaxChars))
	}
	return failures
}

func containsAny(content string, group []string) bool {
	for _, want := range group {
		if strings.Contains(content, strings.ToLower(want)) {
			return true
		}
	}
	return false
}

func findToolCall(calls []llm.ToolCall, name string) *llm.ToolCall {
	for i := range calls {
		call := &calls[i]
		if call.Name == name {
			return call
		}
	}
	return nil
}

func evaluateToolArgs(toolName string, expected map[string]string, rawArgs string) []string {
	var failures []string
	var actual map[string]any
	if strings.TrimSpace(rawArgs) == "" {
		return []string{fmt.Sprintf("tool call %q has empty arguments", toolName)}
	}
	if err := json.Unmarshal([]byte(rawArgs), &actual); err != nil {
		return []string{fmt.Sprintf("tool call %q has invalid JSON arguments", toolName)}
	}
	for key, want := range expected {
		got, ok := actual[key]
		if !ok {
			failures = append(failures, fmt.Sprintf("tool call %q missing argument %q", toolName, key))
			continue
		}
		if actualArgString(got) != want {
			failures = append(failures, fmt.Sprintf("tool call %q argument %q = %q, want %q", toolName, key, actualArgString(got), want))
		}
	}
	return failures
}

func actualArgString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return ""
	default:
		return fmt.Sprint(t)
	}
}

func preview(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
