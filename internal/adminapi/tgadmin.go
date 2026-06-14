package adminapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"telegram-agent/internal/config"
	"telegram-agent/internal/llm"
)

const tgInitDataMaxAge = 24 * time.Hour

type tgAdminData struct {
	FullAdminURL string
}

type tgAdminUser struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
}

type tgAdminSummary struct {
	UpdatedAt    string             `json:"updated_at"`
	Status       tgAdminStatus      `json:"status"`
	Routing      []tgAdminRoute     `json:"routing"`
	Usage        tgAdminUsage       `json:"usage"`
	MCP          []tgAdminMCPServer `json:"mcp"`
	FullAdminURL string             `json:"full_admin_url"`
}

type tgAdminStatus struct {
	Bot      string `json:"bot"`
	Model    string `json:"model"`
	Health   string `json:"health"`
	MCPCount int    `json:"mcp_count"`
}

type tgAdminRoute struct {
	Role      string   `json:"role"`
	Slot      string   `json:"slot"`
	Model     string   `json:"model"`
	Provider  string   `json:"provider"`
	Available []string `json:"available"`
}

type tgAdminModel struct {
	ID              string  `json:"id"`
	Provider        string  `json:"provider"`
	PromptPrice     float64 `json:"prompt_price"`
	CompletionPrice float64 `json:"completion_price"`
	ContextLength   int     `json:"context_length"`
	Vision          bool    `json:"vision"`
	Tools           bool    `json:"tools"`
	Reasoning       bool    `json:"reasoning"`
	Free            bool    `json:"free"`
	Score           float64 `json:"score"`
	AgenticIndex    float64 `json:"agentic_index"`
	SpeedTPS        float64 `json:"speed_tps"`
	TTFT            float64 `json:"ttft"`
	ValuePerDollar  float64 `json:"value_per_dollar"`
	Recommended     bool    `json:"recommended"`
	Current         bool    `json:"current"`
}

type tgAdminModelsResponse struct {
	Role        string         `json:"role"`
	Slot        string         `json:"slot"`
	Current     string         `json:"current"`
	Provider    string         `json:"provider"`
	Query       string         `json:"query"`
	Recommended bool           `json:"recommended"`
	Description string         `json:"description,omitempty"`
	Models      []tgAdminModel `json:"models"`
}

type tgAdminUsage struct {
	Calls24h  int     `json:"calls_24h"`
	Cost24h   float64 `json:"cost_24h"`
	Tokens24h int     `json:"tokens_24h"`
	Calls7d   int     `json:"calls_7d"`
	Cost7d    float64 `json:"cost_7d"`
}

type tgAdminMCPServer struct {
	Name   string `json:"name"`
	Type   string `json:"type,omitempty"`
	Status string `json:"status"`
}

func (s *Server) handleTGAdmin(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/tg-admin" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := render(w, viewTGAdmin, tgAdminData{FullAdminURL: s.cfg.BaseURL}); err != nil {
		s.logger.Error("render tg admin", "err", err)
	}
}

func (s *Server) handleTGAdminRouter(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/tg-admin/api/summary":
		s.requireTGAdmin(http.HandlerFunc(s.handleTGAdminSummary)).ServeHTTP(w, r)
	case "/tg-admin/api/models":
		s.requireTGAdmin(http.HandlerFunc(s.handleTGAdminModels)).ServeHTTP(w, r)
	case "/tg-admin/api/model":
		s.requireTGAdmin(http.HandlerFunc(s.handleTGAdminModelSet)).ServeHTTP(w, r)
	case "/tg-admin/api/routing":
		s.requireTGAdmin(http.HandlerFunc(s.handleTGAdminRoutingSet)).ServeHTTP(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) requireTGAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, err := s.validateTGInitData(r.Header.Get("X-Telegram-Init-Data"), time.Now())
		if err != nil {
			http.Error(w, "telegram auth failed", http.StatusUnauthorized)
			return
		}
		if s.cfgRef == nil || s.cfgRef.Telegram.OwnerChatID == 0 || user.ID != s.cfgRef.Telegram.OwnerChatID {
			http.Error(w, "telegram admin forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) validateTGInitData(raw string, now time.Time) (tgAdminUser, error) {
	if s.cfgRef == nil || s.cfgRef.Telegram.BotToken == "" {
		return tgAdminUser{}, fmt.Errorf("bot token not configured")
	}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return tgAdminUser{}, err
	}
	gotHash := values.Get("hash")
	if gotHash == "" {
		return tgAdminUser{}, fmt.Errorf("missing hash")
	}

	keys := make([]string, 0, len(values))
	for k, v := range values {
		if k == "hash" {
			continue
		}
		if len(v) != 1 {
			return tgAdminUser{}, fmt.Errorf("duplicate init data key: %s", k)
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	checkParts := make([]string, 0, len(keys))
	for _, k := range keys {
		checkParts = append(checkParts, k+"="+values.Get(k))
	}
	dataCheckString := strings.Join(checkParts, "\n")

	secretMAC := hmac.New(sha256.New, []byte("WebAppData"))
	_, _ = secretMAC.Write([]byte(s.cfgRef.Telegram.BotToken))
	secret := secretMAC.Sum(nil)

	hashMAC := hmac.New(sha256.New, secret)
	_, _ = hashMAC.Write([]byte(dataCheckString))
	wantHash := hex.EncodeToString(hashMAC.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(strings.ToLower(gotHash)), []byte(wantHash)) != 1 {
		return tgAdminUser{}, fmt.Errorf("invalid hash")
	}

	authUnix, err := strconv.ParseInt(values.Get("auth_date"), 10, 64)
	if err != nil {
		return tgAdminUser{}, fmt.Errorf("invalid auth_date")
	}
	authTime := time.Unix(authUnix, 0)
	if now.Sub(authTime) > tgInitDataMaxAge || authTime.After(now.Add(5*time.Minute)) {
		return tgAdminUser{}, fmt.Errorf("stale auth_date")
	}

	var user tgAdminUser
	if err := json.Unmarshal([]byte(values.Get("user")), &user); err != nil {
		return tgAdminUser{}, fmt.Errorf("invalid user: %w", err)
	}
	if user.ID == 0 {
		return tgAdminUser{}, fmt.Errorf("missing user id")
	}
	return user, nil
}

func (s *Server) handleTGAdminSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	writeJSON(w, s.buildTGAdminSummary(ctx))
}

func (s *Server) buildTGAdminSummary(ctx context.Context) tgAdminSummary {
	servers := s.loadMCPForUI(ctx)
	return tgAdminSummary{
		UpdatedAt:    time.Now().Format(time.RFC3339),
		Status:       s.buildTGAdminStatus(servers),
		Routing:      s.buildTGAdminRouting(),
		Usage:        s.buildTGAdminUsage(ctx),
		MCP:          buildTGAdminMCP(servers),
		FullAdminURL: s.cfg.BaseURL,
	}
}

func (s *Server) buildTGAdminStatus(servers map[string]config.MCPServerConfig) tgAdminStatus {
	model := ""
	if s.router != nil {
		model = s.router.LastRouted()
		if model == "" {
			model = s.router.GetConfig().Default
		}
	}
	return tgAdminStatus{
		Bot:      "running",
		Model:    model,
		Health:   "ok",
		MCPCount: len(servers),
	}
}

func (s *Server) buildTGAdminRouting() []tgAdminRoute {
	if s.router == nil {
		return nil
	}
	cfg := s.router.GetConfig()
	available := s.router.ProviderNames()
	roles := []struct {
		name string
		slot string
	}{
		{"simple", cfg.Simple},
		{"default", cfg.Default},
		{"complex", cfg.Complex},
		{"multimodal", cfg.Multimodal},
	}
	out := make([]tgAdminRoute, 0, len(roles))
	for _, role := range roles {
		model, provider := role.slot, s.router.SlotProvider(role.slot)
		if p, ok := s.router.Provider(role.slot); ok {
			if cp, ok := p.(llm.ConfigurableProvider); ok && cp.CurrentModel() != "" {
				model = cp.CurrentModel()
			}
		}
		out = append(out, tgAdminRoute{
			Role:      role.name,
			Slot:      role.slot,
			Model:     model,
			Provider:  provider,
			Available: available,
		})
	}
	return out
}

func (s *Server) tgRoleSlot(role string) (string, bool) {
	if s.router == nil {
		return "", false
	}
	cfg := s.router.GetConfig()
	switch role {
	case "simple":
		return cfg.Simple, cfg.Simple != ""
	case "default":
		return cfg.Default, cfg.Default != ""
	case "complex":
		return cfg.Complex, cfg.Complex != ""
	case "multimodal":
		return cfg.Multimodal, cfg.Multimodal != ""
	case "classifier":
		return cfg.Classifier, cfg.Classifier != ""
	case "compaction":
		return cfg.Compaction, cfg.Compaction != ""
	default:
		return "", false
	}
}

func (s *Server) tgSlotModel(slot string) (modelID, providerType string) {
	modelID, providerType = slot, s.router.SlotProvider(slot)
	if p, ok := s.router.Provider(slot); ok {
		if cp, ok := p.(llm.ConfigurableProvider); ok && cp.CurrentModel() != "" {
			modelID = cp.CurrentModel()
		}
	}
	if providerType == "" && s.cfgRef != nil {
		if mc, ok := s.cfgRef.Models[slot]; ok {
			providerType = mc.Provider
		}
	}
	return modelID, providerType
}

func (s *Server) handleTGAdminModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	role := strings.TrimSpace(r.URL.Query().Get("role"))
	provider := strings.TrimSpace(r.URL.Query().Get("provider"))
	if provider == "" {
		provider = "openrouter"
	}
	query := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	limit := 24
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}
	resp, err := s.buildTGAdminModels(r.Context(), role, provider, query, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, resp)
}

func (s *Server) buildTGAdminModels(ctx context.Context, role, provider, query string, limit int) (tgAdminModelsResponse, error) {
	slot, ok := s.tgRoleSlot(role)
	if !ok {
		return tgAdminModelsResponse{}, fmt.Errorf("unknown role")
	}
	current, currentProvider := s.tgSlotModel(slot)
	resp := tgAdminModelsResponse{
		Role:     role,
		Slot:     slot,
		Current:  current,
		Provider: provider,
		Query:    query,
	}
	if provider == "" {
		provider = "openrouter"
		resp.Provider = provider
	}
	if s.capStore == nil {
		return resp, nil
	}

	ctx5, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	allCaps, _ := s.capStore.GetAllCapabilities(ctx5, provider)
	var aaModels map[string]llm.AAModelInfo
	if s.settings != nil {
		if cache, _ := llm.LoadAACache(ctx5, s.settings); cache != nil {
			aaModels = cache.Models
		}
	}

	var models []uiModel
	recommendedMode := false
	if query == "" && provider == "openrouter" {
		var visionFallbackPrompt float64
		cfg := s.router.GetConfig()
		for _, sl := range s.openRouterSlots() {
			if sl.Name == cfg.Multimodal {
				if c, ok := allCaps[sl.ModelID]; ok {
					visionFallbackPrompt = c.PromptPrice
				}
				break
			}
		}
		models = applyPreset(allCaps, aaModels, role, visionFallbackPrompt)
		if p, ok := rolePresets[role]; ok {
			resp.Description = p.Description
		}
		recommendedMode = len(models) > 0
	}
	if len(models) == 0 {
		models = tgAdminBrowseModels(allCaps, aaModels, query, role)
	}
	resp.Recommended = recommendedMode
	if len(models) > limit {
		models = models[:limit]
	}
	resp.Models = make([]tgAdminModel, 0, len(models))
	for _, m := range models {
		resp.Models = append(resp.Models, tgModelFromUI(provider, m, current, recommendedMode))
	}
	if currentProvider != "" && currentProvider != provider && query == "" {
		resp.Description = strings.TrimSpace(resp.Description + " Current role uses " + currentProvider + "; choosing a model here will retarget this role's slot.")
	}
	return resp, nil
}

func tgAdminBrowseModels(allCaps map[string]llm.Capabilities, aaModels map[string]llm.AAModelInfo, query, role string) []uiModel {
	models := make([]uiModel, 0, len(allCaps))
	for id, c := range allCaps {
		if query != "" && !strings.Contains(strings.ToLower(id), query) {
			continue
		}
		if query == "" {
			if role == "multimodal" && !c.Vision {
				continue
			}
			if role != "compaction" && !c.Tools {
				continue
			}
		}
		m := uiModel{
			ID:              id,
			PromptPrice:     c.PromptPrice,
			CompletionPrice: c.CompletionPrice,
			ContextLength:   c.ContextLength,
			Vision:          c.Vision,
			Tools:           c.Tools,
			Reasoning:       c.Reasoning,
			Free:            c.Free(),
			Score:           c.Score,
		}
		if aaModels != nil {
			if info := llm.LookupAAInfo(id, aaModels); info != nil {
				enrichFromAA(&m, *info)
			}
		}
		if m.PromptPrice > 0 {
			q := m.AgenticIndex
			if q == 0 {
				q = m.Score
			}
			if q > 0 {
				m.ValuePerDollar = q / m.PromptPrice
			}
		}
		models = append(models, m)
	}
	sort.Slice(models, func(i, j int) bool {
		if query != "" {
			iExact := strings.EqualFold(models[i].ID, query)
			jExact := strings.EqualFold(models[j].ID, query)
			if iExact != jExact {
				return iExact
			}
		}
		iQ := models[i].AgenticIndex
		if iQ == 0 {
			iQ = models[i].Score
		}
		jQ := models[j].AgenticIndex
		if jQ == 0 {
			jQ = models[j].Score
		}
		if iQ != jQ {
			return iQ > jQ
		}
		if models[i].PromptPrice != models[j].PromptPrice {
			return models[i].PromptPrice < models[j].PromptPrice
		}
		return models[i].ID < models[j].ID
	})
	return models
}

func tgModelFromUI(provider string, m uiModel, current string, recommended bool) tgAdminModel {
	return tgAdminModel{
		ID:              m.ID,
		Provider:        provider,
		PromptPrice:     m.PromptPrice,
		CompletionPrice: m.CompletionPrice,
		ContextLength:   m.ContextLength,
		Vision:          m.Vision,
		Tools:           m.Tools,
		Reasoning:       m.Reasoning,
		Free:            m.Free,
		Score:           m.Score,
		AgenticIndex:    m.AgenticIndex,
		SpeedTPS:        m.SpeedTPS,
		TTFT:            m.TTFT,
		ValuePerDollar:  m.ValuePerDollar,
		Recommended:     recommended,
		Current:         m.ID == current,
	}
}

func (s *Server) handleTGAdminModelSet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Role     string `json:"role"`
		Provider string `json:"provider"`
		ModelID  string `json:"model_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	req.Role = strings.TrimSpace(req.Role)
	req.Provider = strings.TrimSpace(req.Provider)
	req.ModelID = strings.TrimSpace(req.ModelID)
	if req.Provider == "" {
		req.Provider = "openrouter"
	}
	if req.ModelID == "" {
		http.Error(w, "model_id required", http.StatusBadRequest)
		return
	}
	slot, ok := s.tgRoleSlot(req.Role)
	if !ok {
		http.Error(w, "unknown role", http.StatusBadRequest)
		return
	}
	caps := s.lookupCapsFor(r.Context(), req.Provider, req.ModelID)
	if err := s.router.SetProviderModel(slot, req.Provider, req.ModelID, caps); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	writeJSON(w, s.buildTGAdminSummary(ctx))
}

func (s *Server) buildTGAdminUsage(ctx context.Context) tgAdminUsage {
	if s.usageStore == nil {
		return tgAdminUsage{}
	}
	now := time.Now()
	t24, err24 := s.usageStore.UsageTotals(ctx, now.Add(-24*time.Hour))
	t7, err7 := s.usageStore.UsageTotals(ctx, now.Add(-7*24*time.Hour))
	var out tgAdminUsage
	if err24 == nil {
		out.Calls24h = t24.Calls
		out.Cost24h = t24.CostUSD
		out.Tokens24h = t24.PromptTokens + t24.CompletionTokens
	}
	if err7 == nil {
		out.Calls7d = t7.Calls
		out.Cost7d = t7.CostUSD
	}
	return out
}

func buildTGAdminMCP(servers map[string]config.MCPServerConfig) []tgAdminMCPServer {
	names := sortedMCPServerNames(servers)
	out := make([]tgAdminMCPServer, 0, len(names))
	for _, name := range names {
		cfg := servers[name]
		status := "configured"
		if strings.TrimSpace(cfg.URL) == "" {
			status = "missing url"
		}
		out = append(out, tgAdminMCPServer{Name: name, Type: cfg.Type, Status: status})
	}
	return out
}

func (s *Server) handleTGAdminRoutingSet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Role string `json:"role"`
		Slot string `json:"slot"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if s.router == nil {
		http.Error(w, "router unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := s.router.SetRole(req.Role, req.Slot); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	writeJSON(w, s.buildTGAdminSummary(ctx))
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
