package agent

import (
	"context"
	"fmt"
	"log/slog"

	"telegram-agent/internal/llm"
	"telegram-agent/internal/store"
)

const (
	compactionKeepLast      = 10
	compactionTokenThreshold = 16000 // trigger compaction at ~16K tokens
	compactionCharPrecheck   = 32000 // cheap SQL char pre-check to skip token counting when far below threshold
	imageTokenCost           = 1000  // approximate visual token cost per image
)

const compactionSystemPrompt = `Summarise the conversation history into a concise summary in the same language as the conversation.
Preserve: key facts about the user, decisions made, pending tasks, and important context.
Write only the essential content — no preamble or filler.`

// Compacter summarizes old conversation history.
type Compacter struct {
	provider llm.Provider
}

func NewCompacter(provider llm.Provider) *Compacter {
	return &Compacter{provider: provider}
}

// NeedsCompaction returns true if the conversation history should be compacted.
// Uses a two-step check: cheap SQL char count as a pre-filter, then accurate
// token estimation over the actual messages.
func NeedsCompaction(s store.Store, chatID int64) bool {
	cs, ok := s.(store.CompactableStore)
	if !ok {
		return false
	}
	// Fast path: if we're well below threshold even in chars, skip the full load.
	if cs.ActiveCharCount(chatID) < compactionCharPrecheck {
		return false
	}
	rows, err := cs.GetAllActive(chatID)
	if err != nil {
		return false
	}
	total := 0
	for _, row := range rows {
		total += EstimateTokens(row.Message)
	}
	return total > compactionTokenThreshold
}

// EstimateTokens returns a rough token count for a single message.
// Heuristic: 1 token ≈ 4 bytes of UTF-8 text; each image costs ~1000 tokens.
func EstimateTokens(msg llm.Message) int {
	total := 0
	if msg.Content != "" {
		total += len(msg.Content) / 4
	}
	for _, p := range msg.Parts {
		switch p.Type {
		case "text":
			total += len(p.Text) / 4
		case "image_url":
			total += imageTokenCost
		}
	}
	return total
}

// Compact summarizes old messages and marks them as archived.
func (c *Compacter) Compact(ctx context.Context, chatID int64, s store.Store) error {
	cs, ok := s.(store.CompactableStore)
	if !ok {
		return fmt.Errorf("store does not support compaction")
	}

	rows, err := cs.GetAllActive(chatID)
	if err != nil {
		return fmt.Errorf("get active messages: %w", err)
	}

	slog.Info("compact: active messages", "count", len(rows), "char_count", cs.ActiveCharCount(chatID))

	boundary := findBoundary(rows, compactionKeepLast)
	slog.Info("compact: boundary", "boundary", boundary, "keep_last", compactionKeepLast)
	if boundary == 0 {
		return nil // nothing to compact
	}

	toCompact := rows[:boundary]

	// Build message history for the summary request
	history := make([]llm.Message, 0, len(toCompact))
	for _, row := range toCompact {
		history = append(history, row.Message)
	}

	var resp llm.Response
	for attempt := range 2 {
		resp, err = c.provider.Chat(ctx, history, compactionSystemPrompt, nil)
		if err == nil {
			break
		}
		slog.Warn("compaction attempt failed", "attempt", attempt+1, "err", err)
	}
	if err != nil {
		return fmt.Errorf("summarize: %w", err)
	}

	// Insert summary before marking old messages as compacted
	cs.AddSummary(chatID, "[Summary of previous conversation]\n\n"+resp.Content)

	ids := make([]int64, len(toCompact))
	for i, row := range toCompact {
		ids[i] = row.ID
	}
	return cs.MarkCompacted(ids)
}

// findBoundary finds the split point: messages before this index get compacted.
// Snaps to a user message boundary to avoid splitting tool call sequences.
func findBoundary(rows []store.MessageRow, keepLast int) int {
	if len(rows) <= keepLast {
		return 0
	}
	boundary := len(rows) - keepLast
	// Snap back to the nearest user message so we don't split mid-sequence
	for boundary > 0 && rows[boundary].Message.Role != "user" {
		boundary--
	}
	return boundary
}
