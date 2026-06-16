package adminapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"telegram-agent/internal/evalpack"
	"telegram-agent/internal/llm"
)

const (
	settingKeyModelEvals = "recommendation.model_evals"
	modelEvalDatasetPath = "evals/workload.json"
	modelEvalTimeout     = 45 * time.Second
	modelEvalStaleAfter  = modelEvalTimeout + 15*time.Second
)

type modelEvalStatus struct {
	Status     string            `json:"status,omitempty"`
	Provider   string            `json:"provider,omitempty"`
	ModelID    string            `json:"model_id,omitempty"`
	Suite      string            `json:"suite,omitempty"`
	StartedAt  string            `json:"started_at,omitempty"`
	FinishedAt string            `json:"finished_at,omitempty"`
	UpdatedAt  string            `json:"updated_at,omitempty"`
	CheckedAt  string            `json:"checked_at,omitempty"`
	Passed     int               `json:"passed"`
	Failed     int               `json:"failed"`
	DurationMS int64             `json:"duration_ms"`
	Error      string            `json:"error,omitempty"`
	Failures   []string          `json:"failures,omitempty"`
	Results    []evalpack.Result `json:"results,omitempty"`
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
		s.renderModelOperationError(w, r, provider, modelID, "eval", "paid/cloud eval requires confirmation")
		return
	}

	if !s.modelEvalMu.TryLock() {
		s.renderModelOperationError(w, r, provider, modelID, "eval", "another model operation is already running")
		return
	}
	defer s.modelEvalMu.Unlock()
	saveCtx, saveCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer saveCancel()
	if err := s.saveModelEval(saveCtx, provider, modelID, runningModelEval(provider, modelID)); err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	caps := s.lookupCapsFor(r.Context(), provider, modelID)
	ctx, cancel := context.WithTimeout(context.Background(), modelEvalTimeout)
	defer cancel()
	status := s.runModelEval(ctx, provider, modelID, caps)
	saveCtx, saveCancel = context.WithTimeout(context.Background(), 5*time.Second)
	defer saveCancel()
	if err := s.saveModelEval(saveCtx, provider, modelID, status); err != nil {
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
		return failedModelEval(providerType, modelID, "missing runtime config")
	}
	suite, err := evalpack.LoadSuite(modelEvalDatasetPath)
	if err != nil {
		return failedModelEval(providerType, modelID, err.Error())
	}
	factories := llm.BuildBackendFactories(s.cfgRef)
	factory := factories[providerType]
	if factory == nil {
		return failedModelEval(providerType, modelID, "provider is not eval-capable from current config")
	}
	provider, err := factory(modelID, caps)
	if err != nil {
		return failedModelEval(providerType, modelID, err.Error())
	}
	report := evalpack.RunSuite(ctx, provider, suite, evalpack.Options{Timeout: 12 * time.Second})
	now := time.Now().Format(time.RFC3339)
	statusText := "passed"
	if report.Failed > 0 {
		statusText = "failed"
	}
	status := modelEvalStatus{
		Status:     statusText,
		Provider:   providerType,
		ModelID:    modelID,
		Suite:      report.Suite,
		StartedAt:  report.StartedAt.Format(time.RFC3339),
		FinishedAt: now,
		UpdatedAt:  now,
		CheckedAt:  now,
		Passed:     report.Passed,
		Failed:     report.Failed,
		DurationMS: report.DurationMS,
		Results:    report.Results,
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

func runningModelEval(provider, modelID string) modelEvalStatus {
	now := time.Now().Format(time.RFC3339)
	return modelEvalStatus{
		Status:    "running",
		Provider:  provider,
		ModelID:   modelID,
		StartedAt: now,
		UpdatedAt: now,
	}
}

func failedModelEval(provider, modelID, message string) modelEvalStatus {
	now := time.Now().Format(time.RFC3339)
	return modelEvalStatus{
		Status:     "error",
		Provider:   provider,
		ModelID:    modelID,
		StartedAt:  now,
		FinishedAt: now,
		UpdatedAt:  now,
		CheckedAt:  now,
		Failed:     1,
		Error:      message,
		Failures:   []string{message},
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
	normalizeModelEvals(out)
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
	evals[modelCheckKey(provider, modelID)] = normalizeSingleModelEval(provider, modelID, status)
	data, err := json.Marshal(evals)
	if err != nil {
		return err
	}
	return s.settings.PutSetting(ctx, settingKeyModelEvals, string(data))
}

func normalizeModelEvals(evals map[string]modelEvalStatus) {
	for key, status := range evals {
		provider, modelID, _ := strings.Cut(key, "|")
		evals[key] = normalizeSingleModelEval(provider, modelID, status)
	}
}

func normalizeSingleModelEval(provider, modelID string, status modelEvalStatus) modelEvalStatus {
	if status.Provider == "" {
		status.Provider = provider
	}
	if status.ModelID == "" {
		status.ModelID = modelID
	}
	if status.Status == "" {
		switch {
		case status.CheckedAt == "":
			status.Status = "unknown"
		case status.Error != "":
			status.Status = "error"
		case status.Failed > 0:
			status.Status = "failed"
		default:
			status.Status = "passed"
		}
	}
	if status.UpdatedAt == "" {
		if status.FinishedAt != "" {
			status.UpdatedAt = status.FinishedAt
		} else {
			status.UpdatedAt = status.CheckedAt
		}
	}
	if status.CheckedAt == "" && status.FinishedAt != "" {
		status.CheckedAt = status.FinishedAt
	}
	if status.Status == "running" && isStaleModelEval(status, time.Now()) {
		now := time.Now().Format(time.RFC3339)
		status.Status = "error"
		status.FinishedAt = now
		status.UpdatedAt = now
		status.CheckedAt = now
		status.Failed = 1
		status.Error = "model eval timed out before completion"
		status.Failures = []string{status.Error}
	}
	return status
}

func isStaleModelEval(status modelEvalStatus, now time.Time) bool {
	if status.StartedAt == "" {
		return false
	}
	started, err := time.Parse(time.RFC3339, status.StartedAt)
	if err != nil {
		return false
	}
	return now.Sub(started) > modelEvalStaleAfter
}

type modelOpsData struct {
	ActiveTab string
	Checks    []modelCheckStatus
	Evals     []modelEvalStatus
	Suite     evalpack.Suite
	SuiteErr  string
}

func (s *Server) buildModelOpsData(ctx context.Context) modelOpsData {
	data := modelOpsData{ActiveTab: "evals"}
	for _, check := range s.loadModelChecks(ctx) {
		data.Checks = append(data.Checks, check)
	}
	sort.Slice(data.Checks, func(i, j int) bool {
		return modelOpSortTime(data.Checks[i].UpdatedAt, data.Checks[i].CheckedAt, data.Checks[i].StartedAt).After(
			modelOpSortTime(data.Checks[j].UpdatedAt, data.Checks[j].CheckedAt, data.Checks[j].StartedAt))
	})
	for _, eval := range s.loadModelEvals(ctx) {
		data.Evals = append(data.Evals, eval)
	}
	sort.Slice(data.Evals, func(i, j int) bool {
		return modelOpSortTime(data.Evals[i].UpdatedAt, data.Evals[i].CheckedAt, data.Evals[i].StartedAt).After(
			modelOpSortTime(data.Evals[j].UpdatedAt, data.Evals[j].CheckedAt, data.Evals[j].StartedAt))
	})
	suite, err := evalpack.LoadSuite(modelEvalDatasetPath)
	if err != nil {
		data.SuiteErr = err.Error()
	} else {
		data.Suite = suite
	}
	return data
}

func modelOpSortTime(values ...string) time.Time {
	for _, v := range values {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			return t
		}
	}
	return time.Time{}
}
