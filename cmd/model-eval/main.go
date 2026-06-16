package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"telegram-agent/internal/config"
	"telegram-agent/internal/evalpack"
	"telegram-agent/internal/llm"
)

func main() {
	var (
		dataset   = flag.String("dataset", "evals/workload.json", "path to eval suite JSON")
		provider  = flag.String("provider", "openrouter", "provider type: openrouter, gemini, ollama, local")
		model     = flag.String("model", "", "model id to evaluate")
		apiKey    = flag.String("api-key", "", "provider API key; defaults to provider env var")
		baseURL   = flag.String("base-url", "", "provider base URL override")
		maxTokens = flag.Int("max-tokens", 2048, "max output tokens per case")
		timeout   = flag.Duration("timeout", 60*time.Second, "timeout per case")
		format    = flag.String("format", "markdown", "output format: markdown or json")
		outPath   = flag.String("out", "", "optional output file")
		allowPaid = flag.Bool("allow-paid", false, "allow evaluating a paid/cloud model; default only allows local/ollama and OpenRouter :free models")
	)
	flag.Parse()

	if strings.TrimSpace(*model) == "" {
		fatalf("-model is required")
	}
	if err := enforceCostGuard(*provider, *model, *allowPaid); err != nil {
		fatalf("%v", err)
	}
	suite, err := evalpack.LoadSuite(*dataset)
	if err != nil {
		fatalf("load dataset: %v", err)
	}
	p, err := buildProvider(*provider, *model, *apiKey, *baseURL, *maxTokens)
	if err != nil {
		fatalf("provider: %v", err)
	}

	report := evalpack.RunSuite(context.Background(), p, suite, evalpack.Options{Timeout: *timeout})
	output, err := renderReport(report, *format)
	if err != nil {
		fatalf("render report: %v", err)
	}
	if *outPath == "" {
		fmt.Print(output)
	} else {
		if err := os.MkdirAll(filepath.Dir(*outPath), 0755); err != nil && filepath.Dir(*outPath) != "." {
			fatalf("create output dir: %v", err)
		}
		if err := os.WriteFile(*outPath, []byte(output), 0644); err != nil {
			fatalf("write output: %v", err)
		}
		fmt.Printf("wrote %s\n", *outPath)
	}
	if report.Failed > 0 {
		os.Exit(2)
	}
}

func enforceCostGuard(provider, model string, allowPaid bool) error {
	if allowPaid {
		return nil
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	switch provider {
	case "ollama", "local":
		return nil
	case "openrouter":
		if strings.HasSuffix(model, ":free") {
			return nil
		}
		return fmt.Errorf("refusing to evaluate paid OpenRouter model %q without --allow-paid", model)
	default:
		return fmt.Errorf("refusing to evaluate cloud provider %q without --allow-paid", provider)
	}
}

func buildProvider(provider, model, apiKey, baseURL string, maxTokens int) (llm.Provider, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if apiKey == "" {
		apiKey = defaultAPIKey(provider)
	}
	cfg := llm.ApplyProviderDefaults(config.ModelConfig{
		Provider:  provider,
		Model:     model,
		APIKey:    apiKey,
		BaseURL:   baseURL,
		MaxTokens: maxTokens,
	})
	switch provider {
	case "openrouter":
		return llm.NewOpenRouter(cfg)
	case "gemini":
		return llm.NewGeminiNative(cfg)
	case "ollama":
		return llm.NewOllama(cfg)
	case "local":
		return llm.NewLocal(cfg)
	default:
		return nil, fmt.Errorf("unsupported provider %q", provider)
	}
}

func defaultAPIKey(provider string) string {
	switch provider {
	case "openrouter":
		return os.Getenv("OPENROUTER_API_KEY")
	case "gemini":
		return os.Getenv("GEMINI_API_KEY")
	case "ollama":
		return os.Getenv("OLLAMA_API_KEY")
	default:
		return ""
	}
}

func renderReport(report evalpack.Report, format string) (string, error) {
	switch strings.ToLower(format) {
	case "json":
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return "", err
		}
		return string(data) + "\n", nil
	case "markdown", "md":
		return renderMarkdown(report), nil
	default:
		return "", fmt.Errorf("unsupported format %q", format)
	}
}

func renderMarkdown(report evalpack.Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Model Eval: %s\n\n", report.Model)
	fmt.Fprintf(&b, "- Suite: `%s`\n", report.Suite)
	fmt.Fprintf(&b, "- Started: `%s`\n", report.StartedAt.Format(time.RFC3339))
	fmt.Fprintf(&b, "- Duration: `%d ms`\n", report.DurationMS)
	fmt.Fprintf(&b, "- Passed: `%d`\n", report.Passed)
	fmt.Fprintf(&b, "- Failed: `%d`\n\n", report.Failed)
	for _, r := range report.Results {
		status := "PASS"
		if !r.Passed {
			status = "FAIL"
		}
		fmt.Fprintf(&b, "## %s `%s`\n\n", status, r.ID)
		fmt.Fprintf(&b, "- Category: `%s`\n", r.Category)
		fmt.Fprintf(&b, "- Latency: `%d ms`\n", r.LatencyMS)
		if len(r.ToolCalls) > 0 {
			fmt.Fprintf(&b, "- Tool calls: `%s`\n", strings.Join(r.ToolCalls, "`, `"))
		}
		if r.Error != "" {
			fmt.Fprintf(&b, "- Error: `%s`\n", r.Error)
		}
		if len(r.Failures) > 0 {
			fmt.Fprintf(&b, "- Failures: `%s`\n", strings.Join(r.Failures, "`, `"))
		}
		if r.ContentPreview != "" {
			fmt.Fprintf(&b, "\n%s\n", fenced(r.ContentPreview))
		}
		b.WriteString("\n")
	}
	return b.String()
}

func fenced(s string) string {
	return "```text\n" + strings.TrimSpace(s) + "\n```"
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
