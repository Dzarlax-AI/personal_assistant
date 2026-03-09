package llm

import (
	"io"
	"log/slog"
)

// noopLogger возвращает logger, который выбрасывает все сообщения — чтобы тесты не шумели.
func noopLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
