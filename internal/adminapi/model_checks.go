package adminapi

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"telegram-agent/internal/llm"
)

const settingKeyModelChecks = "recommendation.model_checks"

const (
	SettingKeyModelCheckIntervalHours    = "recommendation.model_check_interval_hours"
	SettingKeyModelCheckInitialDelayMins = "recommendation.model_check_initial_delay_minutes"
	SettingKeyModelCheckStaleHours       = "recommendation.model_check_stale_hours"
	SettingKeyModelCheckBatchLimit       = "recommendation.model_check_batch_limit"
)

const (
	defaultModelCheckIntervalHours    = 12
	defaultModelCheckInitialDelayMins = 5
	defaultModelCheckStaleHours       = 24
	defaultModelCheckBatchLimit       = 5
)

type modelCheckStatus struct {
	Status    string `json:"status"`
	CheckedAt string `json:"checked_at,omitempty"`
	LatencyMS int64  `json:"latency_ms,omitempty"`
	Error     string `json:"error,omitempty"`
}

type modelCheckRequest struct {
	Role     string `json:"role"`
	Provider string `json:"provider"`
	ModelID  string `json:"model_id"`
}

type modelCheckResponse struct {
	OK     bool             `json:"ok"`
	Model  string           `json:"model"`
	Status modelCheckStatus `json:"status"`
}

type modelCheckSummary struct {
	Total       int    `json:"total"`
	Verified    int    `json:"verified"`
	Degraded    int    `json:"degraded"`
	Blocked     int    `json:"blocked"`
	Unverified  int    `json:"unverified"`
	OldestCheck string `json:"oldest_check,omitempty"`
	NewestCheck string `json:"newest_check,omitempty"`
}

type modelCheckSweepResult struct {
	Checked   int               `json:"checked"`
	Remaining int               `json:"remaining"`
	Statuses  map[string]int    `json:"statuses"`
	Models    []modelCheckModel `json:"models,omitempty"`
}

type modelCheckModel struct {
	ModelID   string `json:"model_id"`
	Status    string `json:"status"`
	LatencyMS int64  `json:"latency_ms,omitempty"`
	Error     string `json:"error,omitempty"`
}

type modelProbeFunc func(ctx context.Context, providerType, modelID string, caps llm.Capabilities) modelCheckStatus

func modelCheckKey(provider, modelID string) string {
	return provider + "|" + modelID
}

func (s *Server) loadModelChecks(ctx context.Context) map[string]modelCheckStatus {
	if s.settings == nil {
		return nil
	}
	raw, ok, err := s.settings.GetSetting(ctx, settingKeyModelChecks)
	if err != nil || !ok || strings.TrimSpace(raw) == "" {
		return nil
	}
	out := map[string]modelCheckStatus{}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		s.logger.Warn("model checks cache parse failed", "err", err)
		return nil
	}
	return out
}

func (s *Server) modelCheckStatus(ctx context.Context, provider, modelID string) modelCheckStatus {
	checks := s.loadModelChecks(ctx)
	return checks[modelCheckKey(provider, modelID)]
}

func (s *Server) saveModelCheck(ctx context.Context, provider, modelID string, status modelCheckStatus) error {
	if s.settings == nil {
		return fmt.Errorf("settings store unavailable")
	}
	checks := s.loadModelChecks(ctx)
	if checks == nil {
		checks = map[string]modelCheckStatus{}
	}
	checks[modelCheckKey(provider, modelID)] = status
	data, err := json.Marshal(checks)
	if err != nil {
		return err
	}
	return s.settings.PutSetting(ctx, settingKeyModelChecks, string(data))
}

func (s *Server) modelCheckInterval() time.Duration {
	return time.Duration(s.intSetting(SettingKeyModelCheckIntervalHours, defaultModelCheckIntervalHours, 1, 168)) * time.Hour
}

func (s *Server) modelCheckInitialDelay() time.Duration {
	return time.Duration(s.intSetting(SettingKeyModelCheckInitialDelayMins, defaultModelCheckInitialDelayMins, 0, 1440)) * time.Minute
}

func (s *Server) modelCheckStaleAfter() time.Duration {
	return time.Duration(s.intSetting(SettingKeyModelCheckStaleHours, defaultModelCheckStaleHours, 1, 720)) * time.Hour
}

func (s *Server) modelCheckBatchLimit() int {
	return s.intSetting(SettingKeyModelCheckBatchLimit, defaultModelCheckBatchLimit, 1, 50)
}

func (s *Server) intSetting(key string, def, min, max int) int {
	if s.settings == nil {
		return def
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	v := llm.GetIntSetting(ctx, s.settings, key, def)
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

func (s *Server) probeModel(ctx context.Context, providerType, modelID string, caps llm.Capabilities) modelCheckStatus {
	if s.modelProbe != nil {
		return s.modelProbe(ctx, providerType, modelID, caps)
	}
	if s.cfgRef == nil {
		return failedModelCheck("missing runtime config", 0)
	}
	factories := llm.BuildBackendFactories(s.cfgRef)
	factory := factories[providerType]
	if factory == nil {
		return failedModelCheck("provider is not probeable from current config", 0)
	}
	provider, err := factory(modelID, caps)
	if err != nil {
		return failedModelCheck(err.Error(), 0)
	}

	start := time.Now()
	resp, err := provider.Chat(ctx, []llm.Message{{
		Role:    "user",
		Content: "Ответь ровно одним словом: ГОТОВО",
	}}, "You are a health-check probe. Follow the user instruction exactly. Do not call tools.", nil)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		return failedModelCheck(err.Error(), latency)
	}
	if !strings.Contains(strings.ToUpper(resp.Content), "ГОТОВО") {
		return modelCheckStatus{
			Status:    "free_degraded",
			CheckedAt: time.Now().Format(time.RFC3339),
			LatencyMS: latency,
			Error:     "unexpected probe response",
		}
	}
	return modelCheckStatus{
		Status:    "free_verified",
		CheckedAt: time.Now().Format(time.RFC3339),
		LatencyMS: latency,
	}
}

func failedModelCheck(message string, latencyMS int64) modelCheckStatus {
	status := "free_degraded"
	if isBlockedModelCheckError(message) {
		status = "free_blocked"
	}
	return modelCheckStatus{
		Status:    status,
		CheckedAt: time.Now().Format(time.RFC3339),
		LatencyMS: latencyMS,
		Error:     message,
	}
}

func isBlockedModelCheckError(message string) bool {
	lower := strings.ToLower(message)
	for _, needle := range []string{
		"429",
		"rate",
		"quota",
		"no longer available as a free model",
		"unavailable for free",
		"paid version is available",
		"transitioned to a paid model",
		"has become paid",
	} {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}

func (s *Server) startModelCheckScheduler() {
	if s.settings == nil || s.capStore == nil || s.cfgRef == nil {
		return
	}
	s.modelChecksOnce.Do(func() {
		ctx, cancel := context.WithCancel(context.Background())
		s.modelChecksCancel = cancel
		go s.modelCheckScheduler(ctx)
	})
}

func (s *Server) modelCheckScheduler(ctx context.Context) {
	timer := time.NewTimer(s.modelCheckInitialDelay())
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			s.runModelCheckSweep(ctx)
			timer.Reset(s.modelCheckInterval())
		}
	}
}

func (s *Server) runModelCheckSweep(ctx context.Context) modelCheckSweepResult {
	return s.runModelCheckSweepProvider(ctx, "openrouter")
}

func (s *Server) runModelCheckSweepProvider(ctx context.Context, provider string) modelCheckSweepResult {
	sweepCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	candidates := s.freeModelCheckCandidates(sweepCtx, provider)
	result := modelCheckSweepResult{Statuses: map[string]int{}}
	if len(candidates) == 0 {
		return result
	}
	limit := s.modelCheckBatchLimit()
	if len(candidates) > limit {
		result.Remaining = len(candidates) - limit
		candidates = candidates[:limit]
	}
	s.logger.Info("free model check sweep started", "count", len(candidates))
	for _, candidate := range candidates {
		select {
		case <-sweepCtx.Done():
			return result
		default:
		}
		checkCtx, checkCancel := context.WithTimeout(sweepCtx, 30*time.Second)
		status := s.probeModel(checkCtx, candidate.provider, candidate.modelID, candidate.caps)
		checkCancel()
		if err := s.saveModelCheck(sweepCtx, candidate.provider, candidate.modelID, status); err != nil {
			s.logger.Warn("free model check save failed", "model", candidate.modelID, "err", err)
			continue
		}
		result.Checked++
		result.Statuses[status.Status]++
		result.Models = append(result.Models, modelCheckModel{
			ModelID:   candidate.modelID,
			Status:    status.Status,
			LatencyMS: status.LatencyMS,
			Error:     status.Error,
		})
		s.logger.Info("free model checked", "model", candidate.modelID, "status", status.Status, "latency_ms", status.LatencyMS)
	}
	return result
}

type freeModelCandidate struct {
	provider string
	modelID  string
	caps     llm.Capabilities
	checked  time.Time
}

func (s *Server) freeModelCheckCandidates(ctx context.Context, provider string) []freeModelCandidate {
	allCaps, err := s.capStore.GetAllCapabilities(ctx, provider)
	if err != nil {
		s.logger.Warn("free model check catalog load failed", "provider", provider, "err", err)
		return nil
	}
	checks := s.loadModelChecks(ctx)
	now := time.Now()
	out := make([]freeModelCandidate, 0)
	for id, caps := range allCaps {
		if !isFreeVariant(id) {
			continue
		}
		check := checks[modelCheckKey(provider, id)]
		checkedAt := parseCheckTime(check.CheckedAt)
		if !checkedAt.IsZero() && now.Sub(checkedAt) < s.modelCheckStaleAfter() {
			continue
		}
		out = append(out, freeModelCandidate{
			provider: provider,
			modelID:  id,
			caps:     caps,
			checked:  checkedAt,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].checked.IsZero() != out[j].checked.IsZero() {
			return out[i].checked.IsZero()
		}
		if !out[i].checked.Equal(out[j].checked) {
			return out[i].checked.Before(out[j].checked)
		}
		return out[i].modelID < out[j].modelID
	})
	return out
}

func (s *Server) buildModelCheckSummary(ctx context.Context) modelCheckSummary {
	checks := s.loadModelChecks(ctx)
	out := modelCheckSummary{Total: len(checks)}
	var oldest, newest time.Time
	for _, check := range checks {
		switch check.Status {
		case "free_verified":
			out.Verified++
		case "free_degraded":
			out.Degraded++
		case "free_blocked":
			out.Blocked++
		default:
			out.Unverified++
		}
		checkedAt := parseCheckTime(check.CheckedAt)
		if checkedAt.IsZero() {
			continue
		}
		if oldest.IsZero() || checkedAt.Before(oldest) {
			oldest = checkedAt
		}
		if newest.IsZero() || checkedAt.After(newest) {
			newest = checkedAt
		}
	}
	if !oldest.IsZero() {
		out.OldestCheck = oldest.Format(time.RFC3339)
	}
	if !newest.IsZero() {
		out.NewestCheck = newest.Format(time.RFC3339)
	}
	return out
}

func parseCheckTime(raw string) time.Time {
	if raw == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}
	}
	return t
}
