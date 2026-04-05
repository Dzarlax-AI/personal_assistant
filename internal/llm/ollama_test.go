package llm

import (
	"encoding/json"
	"testing"

	"telegram-agent/internal/config"
)

func TestOllamaBuildMessages_ToolNamePopulated(t *testing.T) {
	p, _ := NewOllama(config.ModelConfig{Model: "test"})

	messages := []Message{
		{Role: "user", Content: "what's the weather?"},
		{
			Role: "assistant",
			ToolCalls: []ToolCall{
				{ID: "call_0", Name: "get_weather", Arguments: `{"city":"Belgrade"}`},
			},
		},
		{Role: "tool", Content: `{"temp":22}`, ToolCallID: "call_0"},
	}

	ollamaMsgs := p.buildMessages(messages, "")

	// Find the tool result message.
	var toolMsg *ollamaMessage
	for i := range ollamaMsgs {
		if ollamaMsgs[i].Role == "tool" {
			toolMsg = &ollamaMsgs[i]
			break
		}
	}

	if toolMsg == nil {
		t.Fatal("no tool message found")
	}
	if toolMsg.ToolName != "get_weather" {
		t.Errorf("expected tool_name='get_weather', got '%s'", toolMsg.ToolName)
	}

	// Verify it serializes correctly.
	b, _ := json.Marshal(toolMsg)
	var raw map[string]any
	json.Unmarshal(b, &raw)
	if raw["tool_name"] != "get_weather" {
		t.Errorf("JSON tool_name missing or wrong: %s", string(b))
	}
}

func TestOllamaBuildMessages_MultipleToolCalls(t *testing.T) {
	p, _ := NewOllama(config.ModelConfig{Model: "test"})

	messages := []Message{
		{Role: "user", Content: "weather and time?"},
		{
			Role: "assistant",
			ToolCalls: []ToolCall{
				{ID: "call_0", Name: "get_weather", Arguments: `{"city":"Belgrade"}`},
				{ID: "call_1", Name: "get_time", Arguments: `{"tz":"CET"}`},
			},
		},
		{Role: "tool", Content: `{"temp":22}`, ToolCallID: "call_0"},
		{Role: "tool", Content: `{"time":"14:00"}`, ToolCallID: "call_1"},
	}

	ollamaMsgs := p.buildMessages(messages, "")

	expected := map[string]string{
		`{"temp":22}`:     "get_weather",
		`{"time":"14:00"}`: "get_time",
	}

	for _, msg := range ollamaMsgs {
		if msg.Role == "tool" {
			want, ok := expected[msg.Content]
			if !ok {
				t.Errorf("unexpected tool content: %s", msg.Content)
				continue
			}
			if msg.ToolName != want {
				t.Errorf("content=%s: expected tool_name='%s', got '%s'", msg.Content, want, msg.ToolName)
			}
		}
	}
}
