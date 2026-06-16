package adminapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"telegram-agent/internal/evalpack"
	"telegram-agent/internal/llm"
)

const (
	settingKeyModelEvals = "recommendation.model_evals"
	modelEvalDatasetPath = "evals/workload.json"
	modelEvalTimeout     = 45 * time.Second
)

type modelEvalStatus struct {
	CheckedAt  string   `json:"checked_at,omitempty"`
	Passed     int      `json:"passed"`
	Failed     int      `json:"failed"`
	DurationMS int64    `json:"duration_ms"`
	Error      string   `json:"error,omitempty"`
	Failures   []string `json:"failures,omitempty"`
}

func (s *Server) handleModelEval(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.settings == nil {
		http.Error(w, "settings store not available", http.StatusServiceUnavailable)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "parse form", http.StatusBadRequest)
		return
	}
	provider := strings.TrimSpace(r.FormValue("provider"))
	if provider == "" {
		provider = "openrouter"
	}
	modelID := strings.TrimSpace(r.FormValue("model_id"))
	if modelID == "" {
		http.Error(w, "model_id required", http.StatusBadRequest)
		return
	}
	paid := isPaidModelEval(provider, modelID)
	if paid && r.FormValue("confirm_paid_eval") != "1" {
		http.Error(w, "paid/cloud eval requires confirmation", http.StatusBadRequest)
		return
	}

	if !s.modelEvalMu.TryLock() {
		http.Error(w, "another model eval is already running", http.StatusTooManyRequests)
		return
	}
	defer s.modelEvalMu.Unlock()

	caps := s.lookupCapsFor(r.Context(), provider, modelID)
	ctx, cancel := context.WithTimeout(r.Context(), modelEvalTimeout)
	defer cancel()
	status := s.runModelEval(ctx, provider, modelID, caps)
	if err := s.saveModelEval(ctx, provider, modelID, status); err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	next := r.Clone(r.Context())
	next.URL = cloneURL(r.URL)
	next.URL.RawQuery = r.Form.Encode()
	data := s.buildIndexData(next)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := render(w, viewModelsContent, data); err != nil {
		s.logger.Error("render models after eval", "err", err)
	}
}

func isPaidModelEval(provider, modelID string) bool {
	provider = strings.ToLower(strings.TrimSpace(provider))
	switch provider {
	case "local", "ollama":
		return false
	case "openrouter":
		return !isFreeVariant(modelID)
	default:
		return true
	}
}

func (s *Server) runModelEval(ctx context.Context, providerType, modelID string, caps llm.Capabilities) modelEvalStatus {
	if s.cfgRef == nil {
		return failedModelEval("missing runtime config")
	}
	suite, err := evalpack.LoadSuite(modelEvalDatasetPath)
	if err != nil {
		return failedModelEval(err.Error())
	}
	factories := llm.BuildBackendFactories(s.cfgRef)
	factory := factories[providerType]
	if factory == nil {
		return failedModelEval("provider is not eval-capable from current config")
	}
	provider, err := factory(modelID, caps)
	if err != nil {
		return failedModelEval(err.Error())
	}
	report := evalpack.RunSuite(ctx, provider, suite, evalpack.Options{Timeout: 12 * time.Second})
	status := modelEvalStatus{
		CheckedAt:  time.Now().Format(time.RFC3339),
		Passed:     report.Passed,
		Failed:     report.Failed,
		DurationMS: report.DurationMS,
	}
	for _, result := range report.Results {
		if result.Passed {
			continue
		}
		if result.Error != "" {
			status.Failures = append(status.Failures, fmt.Sprintf("%s: %s", result.ID, result.Error))
		}
		for _, failure := range result.Failures {
			status.Failures = append(status.Failures, fmt.Sprintf("%s: %s", result.ID, failure))
		}
	}
	if report.Failed > 0 && len(status.Failures) == 0 {
		status.Failures = append(status.Failures, "one or more cases failed")
	}
	if len(status.Failures) > 5 {
		status.Failures = status.Failures[:5]
	}
	return status
}

func failedModelEval(message string) modelEvalStatus {
	return modelEvalStatus{
		CheckedAt: time.Now().Format(time.RFC3339),
		Failed:    1,
		Error:     message,
		Failures:  []string{message},
	}
}

func (s *Server) loadModelEvals(ctx context.Context) map[string]modelEvalStatus {
	if s.settings == nil {
		return nil
	}
	raw, ok, err := s.settings.GetSetting(ctx, settingKeyModelEvals)
	if err != nil || !ok || strings.TrimSpace(raw) == "" {
		return nil
	}
	out := map[string]modelEvalStatus{}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		s.logger.Warn("model eval cache parse failed", "err", err)
		return nil
	}
	return out
}

func (s *Server) saveModelEval(ctx context.Context, provider, modelID string, status modelEvalStatus) error {
	if s.settings == nil {
		return fmt.Errorf("settings store unavailable")
	}
	evals := s.loadModelEvals(ctx)
	if evals == nil {
		evals = map[string]modelEvalStatus{}
	}
	evals[modelCheckKey(provider, modelID)] = status
	data, err := json.Marshal(evals)
	if err != nil {
		return err
	}
	return s.settings.PutSetting(ctx, settingKeyModelEvals, string(data))
}
