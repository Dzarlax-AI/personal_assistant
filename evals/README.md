# Model evals

Manual-only workload checks for candidate models.

These evals are intentionally not wired into CI, schedulers, or background jobs.
The default runner refuses cloud paid models unless `--allow-paid` is passed.
OpenRouter models are allowed by default only when the model id ends with
`:free`; local and Ollama models are allowed by default.

Examples:

```bash
go run ./cmd/model-eval -provider openrouter -model google/gemma-3-27b-it:free
go run ./cmd/model-eval -provider ollama -model qwen3:8b -base-url http://localhost:11434
go run ./cmd/model-eval -provider openrouter -model x-ai/grok-4.3 --allow-paid -out eval-results/grok-4.3.md
```

Use paid runs sparingly and only for explicit model selection decisions.
