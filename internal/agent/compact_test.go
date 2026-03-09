package agent

import (
	"testing"

	"telegram-agent/internal/llm"
	"telegram-agent/internal/store"
)

func TestEstimateTokens_TextContent(t *testing.T) {
	msg := llm.Message{Role: "user", Content: "hello world"} // 11 bytes → 2 tokens
	got := EstimateTokens(msg)
	if got != 2 {
		t.Errorf("expected 2 tokens for %q, got %d", msg.Content, got)
	}
}

func TestEstimateTokens_EmptyMessage(t *testing.T) {
	if got := EstimateTokens(llm.Message{}); got != 0 {
		t.Errorf("expected 0 tokens for empty message, got %d", got)
	}
}

func TestEstimateTokens_ImageCostsFixed(t *testing.T) {
	msg := llm.Message{
		Role: "user",
		Parts: []llm.ContentPart{
			{Type: "image_url", ImageURL: &llm.ImageURL{URL: "data:image/jpeg;base64," + string(make([]byte, 100000))}},
		},
	}
	got := EstimateTokens(msg)
	if got != imageTokenCost {
		t.Errorf("expected image to cost %d tokens regardless of size, got %d", imageTokenCost, got)
	}
}

func TestEstimateTokens_ImagePlusText(t *testing.T) {
	msg := llm.Message{
		Role: "user",
		Parts: []llm.ContentPart{
			{Type: "text", Text: "what is this?"}, // 13 bytes → 3 tokens
			{Type: "image_url", ImageURL: &llm.ImageURL{URL: "data:image/jpeg;base64,abc"}},
		},
	}
	got := EstimateTokens(msg)
	want := 3 + imageTokenCost
	if got != want {
		t.Errorf("expected %d tokens, got %d", want, got)
	}
}

func TestNeedsCompaction_BelowPrecheck(t *testing.T) {
	s := newMockCompactableStore(compactionCharPrecheck - 1)
	if NeedsCompaction(s, 1) {
		t.Error("should not compact when below char pre-check threshold")
	}
}

func TestNeedsCompaction_AbovePrecheckBelowTokens(t *testing.T) {
	// Above char pre-check but messages have low token count
	s := newMockCompactableStore(compactionCharPrecheck + 1)
	s.rows = []mockRow{{content: "hi"}} // 0 tokens
	if NeedsCompaction(s, 1) {
		t.Error("should not compact when token count is low")
	}
}

func TestNeedsCompaction_TriggersOnHighTokenCount(t *testing.T) {
	s := newMockCompactableStore(compactionCharPrecheck + 1)
	// Fill with enough content to exceed token threshold
	bigContent := string(make([]byte, compactionTokenThreshold*4+100))
	s.rows = []mockRow{{content: bigContent}}
	if !NeedsCompaction(s, 1) {
		t.Error("should compact when token count exceeds threshold")
	}
}

// --- mock store ---

type mockRow struct {
	content string
}

type mockCompactableStore struct {
	charCount int
	rows      []mockRow
}

func newMockCompactableStore(charCount int) *mockCompactableStore {
	return &mockCompactableStore{charCount: charCount}
}

func (m *mockCompactableStore) GetHistory(_ int64) []llm.Message        { return nil }
func (m *mockCompactableStore) AddMessage(_ int64, _ llm.Message)       {}
func (m *mockCompactableStore) ClearHistory(_ int64)                    {}
func (m *mockCompactableStore) AddSummary(_ int64, _ string)            {}
func (m *mockCompactableStore) MarkCompacted(_ []int64) error           { return nil }
func (m *mockCompactableStore) ActiveCharCount(_ int64) int             { return m.charCount }

func (m *mockCompactableStore) GetAllActive(_ int64) ([]store.MessageRow, error) {
	rows := make([]store.MessageRow, len(m.rows))
	for i, r := range m.rows {
		rows[i] = store.MessageRow{Message: llm.Message{Role: "user", Content: r.content}}
	}
	return rows, nil
}

func (m *mockCompactableStore) GetStats(_ int64) store.ChatStats { return store.ChatStats{} }
