# Model evals

Manual-only workload checks for candidate models.

This suite is a local role-fit gate, not a public benchmark or leaderboard. It
is intentionally shaped around production assistant usage patterns: short
Telegram-style commands, calendar/tasks/memory/health/files/web tool routing,
admin/debug advice, model-routing tradeoffs, and compaction.

These evals are intentionally not wired into CI, schedulers, or background jobs.
The default runner refuses cloud paid models unless `--allow-paid` is passed.
OpenRouter models are allowed by default only when the model id ends with
`:free`; local and Ollama models are allowed by default.

The suite uses deterministic expectations:

- literal required/forbidden content
- "one of these phrases" groups for less brittle synonym checks
- expected tool names
- top-level JSON tool argument checks
- max tool calls
- max response length for simple-role checks

Tool cases use synthetic tool declarations. They validate whether a model picks
the right tool and argument shape; they do not execute the real calendar,
Todoist, memory, health, filesystem, or web tools.

Examples:

```bash
go run ./cmd/model-eval -provider openrouter -model google/gemma-3-27b-it:free
go run ./cmd/model-eval -provider ollama -model qwen3:8b -base-url http://localhost:11434
go run ./cmd/model-eval -provider openrouter -model x-ai/grok-4.3 --allow-paid -out eval-results/grok-4.3.md
```

Use paid runs sparingly and only for explicit model selection decisions.
