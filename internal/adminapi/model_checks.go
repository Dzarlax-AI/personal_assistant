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
	modelCheckInterval     = 12 * time.Hour
	modelCheckInitialDelay = 5 * time.Minute
	modelCheckStaleAfter   = 24 * time.Hour
	modelCheckBatchLimit   = 5
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
	lower := strings.ToLower(message)
	if strings.Contains(lower, "429") || strings.Contains(lower, "rate") || strings.Contains(lower, "quota") {
		status = "free_blocked"
	}
	return modelCheckStatus{
		Status:    status,
		CheckedAt: time.Now().Format(time.RFC3339),
		LatencyMS: latencyMS,
		Error:     message,
	}
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
	timer := time.NewTimer(modelCheckInitialDelay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			s.runModelCheckSweep(ctx)
			timer.Reset(modelCheckInterval)
		}
	}
}

func (s *Server) runModelCheckSweep(ctx context.Context) {
	sweepCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	candidates := s.freeModelCheckCandidates(sweepCtx, "openrouter")
	if len(candidates) == 0 {
		return
	}
	if len(candidates) > modelCheckBatchLimit {
		candidates = candidates[:modelCheckBatchLimit]
	}
	s.logger.Info("free model check sweep started", "count", len(candidates))
	for _, candidate := range candidates {
		select {
		case <-sweepCtx.Done():
			return
		default:
		}
		checkCtx, checkCancel := context.WithTimeout(sweepCtx, 30*time.Second)
		status := s.probeModel(checkCtx, candidate.provider, candidate.modelID, candidate.caps)
		checkCancel()
		if err := s.saveModelCheck(sweepCtx, candidate.provider, candidate.modelID, status); err != nil {
			s.logger.Warn("free model check save failed", "model", candidate.modelID, "err", err)
			continue
		}
		s.logger.Info("free model checked", "model", candidate.modelID, "status", status.Status, "latency_ms", status.LatencyMS)
	}
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
		if !checkedAt.IsZero() && now.Sub(checkedAt) < modelCheckStaleAfter {
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
