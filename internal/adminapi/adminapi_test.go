package adminapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"telegram-agent/internal/agent"
	"telegram-agent/internal/config"
	"telegram-agent/internal/evalpack"
	"telegram-agent/internal/llm"
	"telegram-agent/internal/store"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	providers := map[string]llm.Provider{}
	router := llm.NewRouter(providers, llm.RouterConfig{})
	cfg := &config.Config{
		Models: config.ModelsConfig{},
	}
	return New(config.AdminAPIConfig{
		Enabled:          true,
		Listen:           ":0",
		Token:            "secret-token",
		TrustForwardAuth: false,
	}, router, nil, nil, nil, cfg, slog.Default())
}

type fakeCapabilityStore struct {
	byProvider map[string]map[string]llm.Capabilities
}

func (f fakeCapabilityStore) GetCapabilities(_ context.Context, provider, modelID string) (llm.Capabilities, bool, error) {
	if byID := f.byProvider[provider]; byID != nil {
		c, ok := byID[modelID]
		return c, ok, nil
	}
	return llm.Capabilities{}, false, nil
}

func (f fakeCapabilityStore) PutCapabilities(context.Context, string, string, llm.Capabilities) error {
	return nil
}

func (f fakeCapabilityStore) GetAllCapabilities(_ context.Context, provider string) (map[string]llm.Capabilities, error) {
	out := map[string]llm.Capabilities{}
	for id, c := range f.byProvider[provider] {
		out[id] = c
	}
	return out, nil
}

type fakeConfigurableProvider struct {
	model string
	caps  llm.Capabilities
}

type fakeUsageStore struct {
	byModel []llm.UsageModelRow
}

func (f fakeUsageStore) PutUsage(context.Context, llm.UsageLog) (int64, error) {
	return 0, nil
}

func (f fakeUsageStore) UpdateAssistantMessageID(context.Context, int64, int64) error {
	return nil
}

func (f fakeUsageStore) UpdateTurnLatencyMs(context.Context, int64, int) error {
	return nil
}

func (f fakeUsageStore) UsageTotals(context.Context, time.Time) (llm.UsageTotals, error) {
	return llm.UsageTotals{}, nil
}

func (f fakeUsageStore) UsageByDay(context.Context, time.Time) ([]llm.UsageDayBucket, error) {
	return nil, nil
}

func (f fakeUsageStore) UsageByModel(context.Context, time.Time, int) ([]llm.UsageModelRow, error) {
	return f.byModel, nil
}

func (f fakeUsageStore) UsageByRole(context.Context, time.Time) ([]llm.UsageRoleRow, error) {
	return nil, nil
}

func (f fakeUsageStore) ExpensiveTurns(context.Context, time.Time, int) ([]llm.ExpensiveTurn, error) {
	return nil, nil
}

func (f *fakeConfigurableProvider) Chat(context.Context, []llm.Message, string, []llm.Tool) (llm.Response, error) {
	return llm.Response{}, nil
}

func (f *fakeConfigurableProvider) Name() string { return "fake/" + f.model }

func (f *fakeConfigurableProvider) SetModel(modelID string, caps llm.Capabilities) {
	f.model = modelID
	f.caps = caps
}

func (f *fakeConfigurableProvider) CurrentModel() string { return f.model }

func newTGModelTestServer(t *testing.T) (*Server, *fakeConfigurableProvider) {
	t.Helper()
	provider := &fakeConfigurableProvider{model: "qwen/qwen3.5-flash"}
	router := llm.NewRouter(map[string]llm.Provider{"default-or": provider}, llm.RouterConfig{Simple: "default-or", Default: "default-or", Classifier: "default-or"})
	cfg := &config.Config{
		Models: config.ModelsConfig{
			"default-or": config.ModelConfig{Provider: "openrouter", Model: "qwen/qwen3.5-flash"},
		},
	}
	capStore := fakeCapabilityStore{byProvider: map[string]map[string]llm.Capabilities{
		"openrouter": {
			"qwen/qwen3.5-flash":      {Tools: true, Vision: true, PromptPrice: 0.1, CompletionPrice: 0.4, ContextLength: 64000, Score: 80},
			"qwen/qwen3.5-flash:free": {Tools: true, Vision: true, PromptPrice: 0, CompletionPrice: 0, ContextLength: 64000, Score: 70},
			"qwen/qwen3.5-plus":       {Tools: true, Vision: true, PromptPrice: 1.0, CompletionPrice: 3.0, ContextLength: 128000, Score: 90},
		},
	}}
	return New(config.AdminAPIConfig{
		Enabled: true,
		Listen:  ":0",
		Token:   "secret-token",
	}, router, capStore, nil, nil, cfg, slog.Default()), provider
}

// TestTemplatesParse verifies every registered view renders without panicking
// against minimal data. Catches template syntax errors at test time.
func TestTemplatesParse(t *testing.T) {
	slotInfos := []uiSlotInfo{{Name: "workhorse", Provider: "openrouter"}, {Name: "gemini-flash-lite", Provider: "gemini"}}
	data := indexData{
		Routing: uiRouting{
			Roles: []uiRole{{
				Name: "default", Current: "workhorse",
				Provider: "openrouter", ModelID: "deepseek/v3.1",
				AvailableSlots: slotInfos,
			}},
			AllSlots: slotInfos,
		},
		Slots:           []uiSlot{{Name: "workhorse", ModelID: "deepseek/v3.1"}},
		Models:          []uiModel{{ID: "anthropic/claude", PromptPrice: 3.0, CompletionPrice: 15.0, ContextLength: 200000, Vision: true, Tools: true}},
		CatalogProvider: "openrouter",
	}
	cases := map[string]any{
		viewIndex:         data,
		viewRouting:       data.Routing, // routing view takes uiRouting directly
		viewModelsBrowser: data,
		viewEvals: modelOpsData{
			ActiveTab: "evals",
			Checks: []modelCheckStatus{{
				Status:    "free_verified",
				Provider:  "openrouter",
				ModelID:   "qwen/qwen3.5-flash:free",
				CheckedAt: "2026-06-16T10:00:00Z",
			}},
			Evals: []modelEvalStatus{{
				Status:     "passed",
				Provider:   "openrouter",
				ModelID:    "qwen/qwen3.5-flash:free",
				CheckedAt:  "2026-06-16T10:00:00Z",
				FinishedAt: "2026-06-16T10:00:00Z",
				Passed:     1,
				Results:    []evalpack.Result{{ID: "case-1", Category: "smoke", Passed: true}},
			}},
			Suite: evalpack.Suite{Version: 1, Name: "suite", Cases: []evalpack.Case{{
				ID:       "case-1",
				Category: "smoke",
				Prompt:   "Say ok",
			}}},
		},
		viewTGAdmin: tgAdminData{FullAdminURL: "https://assistant.example/admin"},
	}
	for v, d := range cases {
		t.Run(v, func(t *testing.T) {
			var buf bytes.Buffer
			if err := render(&buf, v, d); err != nil {
				t.Fatalf("render %s: %v", v, err)
			}
			if buf.Len() == 0 {
				t.Errorf("view %s rendered empty", v)
			}
		})
	}
}

func TestModelsBrowserRendersCardAndTableModes(t *testing.T) {
	slotInfos := []uiSlotInfo{{Name: "default-or", Provider: "openrouter"}}
	base := indexData{
		Routing: uiRouting{AllSlots: slotInfos},
		Slots:   []uiSlot{{Name: "default-or", ModelID: "qwen/qwen3.5-plus"}},
		Models: []uiModel{{
			ID:              "qwen/qwen3.5-plus",
			Name:            "Qwen 3.5 Plus",
			Description:     "Balanced multilingual model for tool-heavy assistant workflows.",
			PromptPrice:     1.0,
			CompletionPrice: 3.0,
			ContextLength:   128000,
			Tools:           true,
			StatusLabel:     "Candidate",
			PrimaryReason:   "tool calling",
		}},
		CatalogProvider: "openrouter",
	}

	t.Run("compact default", func(t *testing.T) {
		data := base
		data.Filters.View = "compact"
		var buf bytes.Buffer
		if err := render(&buf, viewModelsBrowser, data); err != nil {
			t.Fatal(err)
		}
		html := buf.String()
		if !strings.Contains(html, "catalog-list") || !strings.Contains(html, "catalog-row") {
			t.Fatalf("compact view missing catalog rows: %s", html)
		}
		if !strings.Contains(html, "Balanced multilingual model") {
			t.Fatalf("compact view missing model description: %s", html)
		}
		if strings.Contains(html, "<details class=\"catalog-row__description\">") || strings.Contains(html, "catalog-row__description-full") {
			t.Fatalf("compact view should render full description without expandable clamp: %s", html)
		}
		if strings.Contains(html, "catalog-audit-table") {
			t.Fatalf("compact view should not render audit table")
		}
	})

	t.Run("table audit", func(t *testing.T) {
		data := base
		data.Filters.View = "table"
		var buf bytes.Buffer
		if err := render(&buf, viewModelsBrowser, data); err != nil {
			t.Fatal(err)
		}
		html := buf.String()
		if !strings.Contains(html, "catalog-audit-table") {
			t.Fatalf("table view missing audit table: %s", html)
		}
		if strings.Contains(html, "catalog-list") || strings.Contains(html, "catalog-row") {
			t.Fatalf("table view should not render compact layout")
		}
	})

	t.Run("preset state is preserved except clear", func(t *testing.T) {
		data := base
		data.Filters.View = "compact"
		data.Filters.ActivePreset = "default"
		var buf bytes.Buffer
		if err := render(&buf, viewModelsBrowser, data); err != nil {
			t.Fatal(err)
		}
		html := buf.String()
		if !strings.Contains(html, `name="preset" value="default"`) {
			t.Fatalf("active preset should be submitted with filters and view toggle: %s", html)
		}
		if !strings.Contains(html, `hx-get="/models?provider=openrouter&view=compact&full=1"`) {
			t.Fatalf("clear preset should refresh browser without preset: %s", html)
		}
	})
}

func TestModelsBrowserRendersFreeCheckAction(t *testing.T) {
	data := indexData{
		Slots: []uiSlot{{Name: "default-or", ModelID: "qwen/qwen3.5-plus"}},
		Models: []uiModel{{
			ID:              "qwen/qwen3.5-flash:free",
			Description:     "Free endpoint for quick routing checks.",
			PromptPrice:     0,
			CompletionPrice: 0,
			ContextLength:   64000,
			Tools:           true,
			Free:            true,
			Policy:          "free_degraded",
			CheckStatus:     "free_degraded",
			CheckLatencyMS:  321,
			StatusLabel:     "Untested",
			PolicyLabel:     "Free degraded",
			PrimaryReason:   "Free endpoint degraded",
		}},
		Filters:         uiFilters{View: "compact"},
		CatalogProvider: "openrouter",
	}
	var buf bytes.Buffer
	if err := render(&buf, viewModelsBrowser, data); err != nil {
		t.Fatal(err)
	}
	html := buf.String()
	if !strings.Contains(html, `hx-post="/models/check"`) || !strings.Contains(html, "Check free") {
		t.Fatalf("free check action missing: %s", html)
	}
	if !strings.Contains(html, "Free model must pass check before routing") {
		t.Fatalf("unverified free model should not render active assign buttons: %s", html)
	}
	if !strings.Contains(html, "free_degraded") || !strings.Contains(html, "321 ms") {
		t.Fatalf("check status not rendered: %s", html)
	}
}

func TestModelsBrowserRendersEvalControlsAndStatus(t *testing.T) {
	data := indexData{
		Slots: []uiSlot{{Name: "default-or", ModelID: "x-ai/grok-4.3"}},
		Models: []uiModel{{
			ID:              "x-ai/grok-4.3",
			PromptPrice:     1.25,
			CompletionPrice: 2.50,
			ContextLength:   1000000,
			Tools:           true,
			EvalStatus: modelEvalStatus{
				CheckedAt:  "2026-06-16T10:00:00Z",
				Passed:     4,
				Failed:     1,
				DurationMS: 1200,
				Failures:   []string{"tool-web-fetch-intent: missing tool call"},
			},
		}},
		CatalogProvider: "openrouter",
	}
	var buf bytes.Buffer
	if err := render(&buf, viewModelsBrowser, data); err != nil {
		t.Fatal(err)
	}
	html := buf.String()
	if !strings.Contains(html, "Run paid eval") || !strings.Contains(html, "Paid eval may spend credits") {
		t.Fatalf("paid eval warning controls missing: %s", html)
	}
	if !strings.Contains(html, "eval:") || !strings.Contains(html, "failed") || !strings.Contains(html, "4/5 cases") {
		t.Fatalf("eval status missing: %s", html)
	}
}

func TestModelEvalRejectsPaidWithoutConfirmation(t *testing.T) {
	s := newTestServer(t)
	s.settings = &fakeSettingsStore{values: map[string]string{}}
	form := url.Values{}
	form.Set("provider", "openrouter")
	form.Set("model_id", "x-ai/grok-4.3")
	req := httptest.NewRequest(http.MethodPost, "/models/eval", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	s.handleModelEval(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "requires confirmation") {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
	if _, ok := s.settings.(*fakeSettingsStore).values[settingKeyModelEvals]; ok {
		t.Fatal("paid eval rejection should not write eval settings")
	}
}

func TestModelEvalPaidGuard(t *testing.T) {
	if isPaidModelEval("openrouter", "google/gemma-3-27b-it:free") {
		t.Fatal("OpenRouter :free eval should not be treated as paid")
	}
	if !isPaidModelEval("openrouter", "x-ai/grok-4.3") {
		t.Fatal("paid OpenRouter eval should require confirmation")
	}
	if !isPaidModelEval("gemini", "gemini-2.5-flash") {
		t.Fatal("Gemini eval should require confirmation")
	}
	if isPaidModelEval("ollama", "qwen3:8b") {
		t.Fatal("Ollama eval should be allowed without paid confirmation")
	}
}

func TestEvalsPageRendersChecksReportsAndSuite(t *testing.T) {
	data := modelOpsData{
		ActiveTab: "evals",
		Checks: []modelCheckStatus{{
			Status:    "checking",
			Provider:  "openrouter",
			ModelID:   "qwen/qwen3.5-flash:free",
			StartedAt: "2026-06-16T10:00:00Z",
		}},
		Evals: []modelEvalStatus{{
			Status:     "failed",
			Provider:   "openrouter",
			ModelID:    "x-ai/grok-4.3",
			Suite:      "personal-assistant-core-workloads",
			StartedAt:  "2026-06-16T10:00:00Z",
			FinishedAt: "2026-06-16T10:00:02Z",
			Passed:     4,
			Failed:     1,
			Failures:   []string{"tool-web-fetch-intent: missing tool call"},
			Results: []evalpack.Result{{
				ID:             "tool-web-fetch-intent",
				Category:       "web_fetch",
				Passed:         false,
				LatencyMS:      1200,
				ContentPreview: "I cannot open links.",
				Failures:       []string{"missing tool call \"web_fetch\""},
			}},
		}},
		Suite: evalpack.Suite{Version: 1, Name: "personal-assistant-core-workloads", Cases: []evalpack.Case{{
			ID:       "tool-web-fetch-intent",
			Category: "web_fetch",
			Prompt:   "Open https://example.com",
			Expect:   evalpack.Expect{ToolCall: "web_fetch"},
		}}},
	}
	var buf bytes.Buffer
	if err := render(&buf, viewEvals, data); err != nil {
		t.Fatal(err)
	}
	html := buf.String()
	for _, want := range []string{"Model Operations", "checking", "x-ai/grok-4.3", "tool-web-fetch-intent", "Open https://example.com", "missing tool call"} {
		if !strings.Contains(html, want) {
			t.Fatalf("evals page missing %q: %s", want, html)
		}
	}
}

func TestModelChecksPersistCheckingBeforeProbe(t *testing.T) {
	s, _ := newTGModelTestServer(t)
	settings := &fakeSettingsStore{values: map[string]string{}}
	s.settings = settings
	s.modelProbe = func(ctx context.Context, provider, modelID string, _ llm.Capabilities) modelCheckStatus {
		got := s.modelCheckStatus(ctx, provider, modelID)
		if got.Status != "checking" || got.StartedAt == "" {
			t.Fatalf("expected checking state before probe, got %+v", got)
		}
		return modelCheckStatus{Status: "free_verified", CheckedAt: time.Now().Format(time.RFC3339), LatencyMS: 22}
	}

	form := url.Values{}
	form.Set("provider", "openrouter")
	form.Set("model_id", "qwen/qwen3.5-flash:free")
	req := httptest.NewRequest(http.MethodPost, "/models/check", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.handleModelCheck(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	got := s.modelCheckStatus(context.Background(), "openrouter", "qwen/qwen3.5-flash:free")
	if got.Status != "free_verified" || got.LatencyMS != 22 {
		t.Fatalf("final check state not persisted: %+v", got)
	}
}

func TestModelEvalLoadLegacyAndDetailedReports(t *testing.T) {
	s := newTestServer(t)
	legacy := map[string]modelEvalStatus{
		modelCheckKey("openrouter", "legacy/model"): {
			CheckedAt: "2026-06-16T10:00:00Z",
			Passed:    4,
			Failed:    1,
			Failures:  []string{"legacy failure"},
		},
	}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	s.settings = &fakeSettingsStore{values: map[string]string{settingKeyModelEvals: string(data)}}
	evals := s.loadModelEvals(context.Background())
	got := evals[modelCheckKey("openrouter", "legacy/model")]
	if got.Status != "failed" || got.Provider != "openrouter" || got.ModelID != "legacy/model" {
		t.Fatalf("legacy eval not normalized: %+v", got)
	}

	detailed := modelEvalStatus{
		Status:    "passed",
		Provider:  "openrouter",
		ModelID:   "new/model",
		Suite:     "suite",
		StartedAt: "2026-06-16T10:01:00Z",
		Passed:    1,
		Results:   []evalpack.Result{{ID: "case-1", Category: "smoke", Passed: true, ContentPreview: "ok"}},
	}
	if err := s.saveModelEval(context.Background(), "openrouter", "new/model", detailed); err != nil {
		t.Fatal(err)
	}
	evals = s.loadModelEvals(context.Background())
	got = evals[modelCheckKey("openrouter", "new/model")]
	if got.Status != "passed" || len(got.Results) != 1 || got.Results[0].ContentPreview != "ok" {
		t.Fatalf("detailed eval not persisted: %+v", got)
	}
}

func TestWebModelCheckPersistsFreeModelStatus(t *testing.T) {
	s, _ := newTGModelTestServer(t)
	settings := &fakeSettingsStore{values: map[string]string{}}
	s.settings = settings
	s.modelProbe = func(context.Context, string, string, llm.Capabilities) modelCheckStatus {
		return modelCheckStatus{
			Status:    "free_verified",
			CheckedAt: "2026-06-15T12:00:00Z",
			LatencyMS: 88,
		}
	}

	form := url.Values{}
	form.Set("provider", "openrouter")
	form.Set("model_id", "qwen/qwen3.5-flash:free")
	form.Set("view", "compact")
	req := httptest.NewRequest(http.MethodPost, "/models/check", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.handleModelCheck(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	checks := s.loadModelChecks(context.Background())
	got := checks[modelCheckKey("openrouter", "qwen/qwen3.5-flash:free")]
	if got.Status != "free_verified" || got.LatencyMS != 88 {
		t.Fatalf("status not persisted: %+v", checks)
	}
	if !strings.Contains(rec.Body.String(), "free_verified") {
		t.Fatalf("response should render updated check status: %s", rec.Body.String())
	}
}

func TestWebSlotAssignRejectsUnverifiedFreeModel(t *testing.T) {
	s, provider := newTGModelTestServer(t)
	s.settings = &fakeSettingsStore{values: map[string]string{}}

	form := url.Values{}
	form.Set("provider", "openrouter")
	form.Set("model_id", "qwen/qwen3.5-flash:free")
	req := httptest.NewRequest(http.MethodPost, "/slots/default-or/assign", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.handleSlotAssign(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if provider.CurrentModel() == "qwen/qwen3.5-flash:free" {
		t.Fatalf("unverified free model should not be assigned")
	}
}

func TestWebSlotAssignAllowsVerifiedFreeModel(t *testing.T) {
	s, provider := newTGModelTestServer(t)
	checks := map[string]modelCheckStatus{
		modelCheckKey("openrouter", "qwen/qwen3.5-flash:free"): {Status: "free_verified", CheckedAt: time.Now().Format(time.RFC3339)},
	}
	data, err := json.Marshal(checks)
	if err != nil {
		t.Fatal(err)
	}
	s.settings = &fakeSettingsStore{values: map[string]string{settingKeyModelChecks: string(data)}}

	form := url.Values{}
	form.Set("provider", "openrouter")
	form.Set("model_id", "qwen/qwen3.5-flash:free")
	req := httptest.NewRequest(http.MethodPost, "/slots/default-or/assign", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.handleSlotAssign(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if provider.CurrentModel() != "qwen/qwen3.5-flash:free" {
		t.Fatalf("model = %q, want free model", provider.CurrentModel())
	}
}

func signedTGInitData(t *testing.T, token string, userID int64, authTime time.Time) string {
	t.Helper()
	userJSON, err := json.Marshal(tgAdminUser{ID: userID, FirstName: "Alex"})
	if err != nil {
		t.Fatal(err)
	}
	values := url.Values{}
	values.Set("auth_date", strconv.FormatInt(authTime.Unix(), 10))
	values.Set("query_id", "AAEAAAE")
	values.Set("user", string(userJSON))

	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+values.Get(k))
	}

	secretMAC := hmac.New(sha256.New, []byte("WebAppData"))
	_, _ = secretMAC.Write([]byte(token))
	secret := secretMAC.Sum(nil)

	hashMAC := hmac.New(sha256.New, secret)
	_, _ = hashMAC.Write([]byte(strings.Join(parts, "\n")))
	values.Set("hash", hex.EncodeToString(hashMAC.Sum(nil)))
	return values.Encode()
}

func TestValidateTGInitDataAcceptsOwnerSignedData(t *testing.T) {
	s := newTestServer(t)
	s.cfgRef.Telegram.BotToken = "123:abc"
	now := time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC)

	user, err := s.validateTGInitData(signedTGInitData(t, "123:abc", 42, now), now)
	if err != nil {
		t.Fatalf("validateTGInitData: %v", err)
	}
	if user.ID != 42 {
		t.Fatalf("user id = %d, want 42", user.ID)
	}
}

func TestTGAdminAPIRejectsInvalidAuth(t *testing.T) {
	s := newTestServer(t)
	s.cfgRef.Telegram.BotToken = "123:abc"
	s.cfgRef.Telegram.OwnerChatID = 42

	req := httptest.NewRequest(http.MethodGet, "/tg-admin/api/summary", nil)
	req.Header.Set("X-Telegram-Init-Data", "auth_date=1&user={}&hash=bad")
	rec := httptest.NewRecorder()
	s.handleTGAdminRouter(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestTGAdminAPIAcceptsOwner(t *testing.T) {
	s := newTestServer(t)
	s.cfgRef.Telegram.BotToken = "123:abc"
	s.cfgRef.Telegram.OwnerChatID = 42
	now := time.Now()

	req := httptest.NewRequest(http.MethodGet, "/tg-admin/api/summary", nil)
	req.Header.Set("X-Telegram-Init-Data", signedTGInitData(t, "123:abc", 42, now))
	rec := httptest.NewRecorder()
	s.handleTGAdminRouter(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload tgAdminSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Status.Bot != "running" {
		t.Fatalf("bot status = %q", payload.Status.Bot)
	}
}

func TestTGAdminAPIRejectsNonOwner(t *testing.T) {
	s := newTestServer(t)
	s.cfgRef.Telegram.BotToken = "123:abc"
	s.cfgRef.Telegram.OwnerChatID = 42
	now := time.Now()

	req := httptest.NewRequest(http.MethodGet, "/tg-admin/api/summary", nil)
	req.Header.Set("X-Telegram-Init-Data", signedTGInitData(t, "123:abc", 99, now))
	rec := httptest.NewRecorder()
	s.handleTGAdminRouter(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestTGAdminModelsReturnsSearchResults(t *testing.T) {
	s, _ := newTGModelTestServer(t)
	s.cfgRef.Telegram.BotToken = "123:abc"
	s.cfgRef.Telegram.OwnerChatID = 42

	req := httptest.NewRequest(http.MethodGet, "/tg-admin/api/models?role=default&q=plus", nil)
	req.Header.Set("X-Telegram-Init-Data", signedTGInitData(t, "123:abc", 42, time.Now()))
	rec := httptest.NewRecorder()
	s.handleTGAdminRouter(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload tgAdminModelsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Role != "default" || payload.Slot != "default-or" {
		t.Fatalf("unexpected payload role/slot: %+v", payload)
	}
	if len(payload.Models) != 1 || payload.Models[0].ID != "qwen/qwen3.5-plus" {
		t.Fatalf("unexpected models: %+v", payload.Models)
	}
}

func TestTGAdminModelsIncludesUsageTelemetry(t *testing.T) {
	s, _ := newTGModelTestServer(t)
	s.cfgRef.Telegram.BotToken = "123:abc"
	s.cfgRef.Telegram.OwnerChatID = 42
	s.usageStore = fakeUsageStore{byModel: []llm.UsageModelRow{{
		Provider:     "openrouter",
		ModelID:      "qwen/qwen3.5-plus",
		Calls:        12,
		CostUSD:      0.0345,
		AvgLatencyMS: 830,
		ErrorCount:   3,
	}}}

	req := httptest.NewRequest(http.MethodGet, "/tg-admin/api/models?role=default&q=plus", nil)
	req.Header.Set("X-Telegram-Init-Data", signedTGInitData(t, "123:abc", 42, time.Now()))
	rec := httptest.NewRecorder()
	s.handleTGAdminRouter(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload tgAdminModelsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Models) != 1 {
		t.Fatalf("unexpected models: %+v", payload.Models)
	}
	tel := payload.Models[0].Telemetry
	if tel.Calls != 12 || tel.AvgLatencyMS != 830 || tel.ErrorRatePct != 25 || tel.WindowDays != 7 {
		t.Fatalf("unexpected telemetry: %+v", tel)
	}
}

func TestTGAdminModelsMarksFreeSearchResultsUnverified(t *testing.T) {
	s, _ := newTGModelTestServer(t)
	s.cfgRef.Telegram.BotToken = "123:abc"
	s.cfgRef.Telegram.OwnerChatID = 42

	req := httptest.NewRequest(http.MethodGet, "/tg-admin/api/models?role=default&q=:free", nil)
	req.Header.Set("X-Telegram-Init-Data", signedTGInitData(t, "123:abc", 42, time.Now()))
	rec := httptest.NewRecorder()
	s.handleTGAdminRouter(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload tgAdminModelsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Models) != 1 {
		t.Fatalf("models len = %d, want 1: %+v", len(payload.Models), payload.Models)
	}
	model := payload.Models[0]
	if !model.Free || model.Policy != "free_unverified" || model.Recommended {
		t.Fatalf("unexpected free model flags: %+v", model)
	}
	if len(model.Warnings) == 0 || !strings.Contains(model.Warnings[0], "validate") {
		t.Fatalf("missing validation warning: %+v", model.Warnings)
	}
}

func TestTGAdminModelsHidesBlockedFreeSearchResults(t *testing.T) {
	s, _ := newTGModelTestServer(t)
	s.cfgRef.Telegram.BotToken = "123:abc"
	s.cfgRef.Telegram.OwnerChatID = 42
	checks := map[string]modelCheckStatus{
		modelCheckKey("openrouter", "qwen/qwen3.5-flash:free"): {
			Status:    "free_blocked",
			CheckedAt: time.Now().Format(time.RFC3339),
			Error:     "api error (HTTP 404): This model is unavailable for free. The paid version is available now.",
		},
	}
	data, err := json.Marshal(checks)
	if err != nil {
		t.Fatal(err)
	}
	s.settings = &fakeSettingsStore{values: map[string]string{settingKeyModelChecks: string(data)}}

	req := httptest.NewRequest(http.MethodGet, "/tg-admin/api/models?role=default&q=:free", nil)
	req.Header.Set("X-Telegram-Init-Data", signedTGInitData(t, "123:abc", 42, time.Now()))
	rec := httptest.NewRecorder()
	s.handleTGAdminRouter(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload tgAdminModelsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Models) != 0 {
		t.Fatalf("blocked free models should be hidden: %+v", payload.Models)
	}
}

func TestTGAdminRecommendedModelsAppendFreeCandidatesUnrecommended(t *testing.T) {
	s, _ := newTGModelTestServer(t)
	s.cfgRef.Telegram.BotToken = "123:abc"
	s.cfgRef.Telegram.OwnerChatID = 42

	req := httptest.NewRequest(http.MethodGet, "/tg-admin/api/models?role=simple", nil)
	req.Header.Set("X-Telegram-Init-Data", signedTGInitData(t, "123:abc", 42, time.Now()))
	rec := httptest.NewRecorder()
	s.handleTGAdminRouter(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload tgAdminModelsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	var sawRecommended, sawFreeCandidate bool
	for _, model := range payload.Models {
		if model.Recommended && !model.Free {
			sawRecommended = true
			if model.PrimaryReason != "Pareto frontier for simple" {
				t.Fatalf("recommended primary reason = %q, want role-specific Pareto explanation: %+v", model.PrimaryReason, model)
			}
			if strings.EqualFold(model.PolicyLabel, "recommended") {
				t.Fatalf("recommended policy label should be suppressed, got %+v", model)
			}
		}
		if model.Free {
			sawFreeCandidate = true
			if model.Recommended || model.Policy != "free_unverified" || model.Source != "free" {
				t.Fatalf("unexpected free candidate flags: %+v", model)
			}
			if model.StatusLabel != "Untested" || model.PolicyLabel != "Free unverified" {
				t.Fatalf("unexpected free candidate display labels: %+v", model)
			}
		}
	}
	if !sawRecommended || !sawFreeCandidate {
		t.Fatalf("missing recommended/free candidates: %+v", payload.Models)
	}
}

func TestTGAdminRecommendedModelsHideBlockedFreeCandidates(t *testing.T) {
	s, _ := newTGModelTestServer(t)
	s.cfgRef.Telegram.BotToken = "123:abc"
	s.cfgRef.Telegram.OwnerChatID = 42
	checks := map[string]modelCheckStatus{
		modelCheckKey("openrouter", "qwen/qwen3.5-flash:free"): {
			Status:    "free_blocked",
			CheckedAt: time.Now().Format(time.RFC3339),
			Error:     "api error (HTTP 404): This model is unavailable for free. The paid version is available now.",
		},
	}
	data, err := json.Marshal(checks)
	if err != nil {
		t.Fatal(err)
	}
	s.settings = &fakeSettingsStore{values: map[string]string{settingKeyModelChecks: string(data)}}

	req := httptest.NewRequest(http.MethodGet, "/tg-admin/api/models?role=simple", nil)
	req.Header.Set("X-Telegram-Init-Data", signedTGInitData(t, "123:abc", 42, time.Now()))
	rec := httptest.NewRecorder()
	s.handleTGAdminRouter(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload tgAdminModelsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	for _, model := range payload.Models {
		if model.ID == "qwen/qwen3.5-flash:free" {
			t.Fatalf("blocked free candidate should be hidden: %+v", payload.Models)
		}
	}
}

func TestTGAdminModelsHideManualDeniedModels(t *testing.T) {
	s, _ := newTGModelTestServer(t)
	s.cfgRef.Telegram.BotToken = "123:abc"
	s.cfgRef.Telegram.OwnerChatID = 42
	overrides := map[string]modelOverride{
		modelCheckKey("openrouter", "qwen/qwen3.5-plus"): {State: "deny", Note: "too costly"},
	}
	data, err := json.Marshal(overrides)
	if err != nil {
		t.Fatal(err)
	}
	s.settings = &fakeSettingsStore{values: map[string]string{settingKeyModelOverrides: string(data)}}

	req := httptest.NewRequest(http.MethodGet, "/tg-admin/api/models?role=default&q=plus", nil)
	req.Header.Set("X-Telegram-Init-Data", signedTGInitData(t, "123:abc", 42, time.Now()))
	rec := httptest.NewRecorder()
	s.handleTGAdminRouter(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload tgAdminModelsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Models) != 0 {
		t.Fatalf("manual denied model should be hidden: %+v", payload.Models)
	}
}

func TestTGAdminRecommendedModelsIncludeManualAllowedModel(t *testing.T) {
	s, _ := newTGModelTestServer(t)
	s.cfgRef.Telegram.BotToken = "123:abc"
	s.cfgRef.Telegram.OwnerChatID = 42
	overrides := map[string]modelOverride{
		modelCheckKey("openrouter", "qwen/qwen3.5-plus"): {State: "allow", Note: "known good"},
	}
	data, err := json.Marshal(overrides)
	if err != nil {
		t.Fatal(err)
	}
	s.settings = &fakeSettingsStore{values: map[string]string{settingKeyModelOverrides: string(data)}}

	req := httptest.NewRequest(http.MethodGet, "/tg-admin/api/models?role=simple", nil)
	req.Header.Set("X-Telegram-Init-Data", signedTGInitData(t, "123:abc", 42, time.Now()))
	rec := httptest.NewRecorder()
	s.handleTGAdminRouter(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload tgAdminModelsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	for _, model := range payload.Models {
		if model.ID == "qwen/qwen3.5-plus" {
			if model.Policy != "manual_allow" || model.OverrideNote != "known good" {
				t.Fatalf("unexpected manual allow model: %+v", model)
			}
			return
		}
	}
	t.Fatalf("manual allowed model missing: %+v", payload.Models)
}

func TestTGAdminModelCheckPersistsFreeModelStatus(t *testing.T) {
	s, _ := newTGModelTestServer(t)
	s.cfgRef.Telegram.BotToken = "123:abc"
	s.cfgRef.Telegram.OwnerChatID = 42
	settings := &fakeSettingsStore{values: map[string]string{}}
	s.settings = settings
	s.modelProbe = func(context.Context, string, string, llm.Capabilities) modelCheckStatus {
		return modelCheckStatus{
			Status:    "free_verified",
			CheckedAt: "2026-06-14T12:00:00Z",
			LatencyMS: 123,
		}
	}

	body := strings.NewReader(`{"role":"simple","provider":"openrouter","model_id":"qwen/qwen3.5-flash:free"}`)
	req := httptest.NewRequest(http.MethodPost, "/tg-admin/api/model/check", body)
	req.Header.Set("X-Telegram-Init-Data", signedTGInitData(t, "123:abc", 42, time.Now()))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.handleTGAdminRouter(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload modelCheckResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.OK || payload.Status.Status != "free_verified" {
		t.Fatalf("unexpected check response: %+v", payload)
	}
	checks := s.loadModelChecks(context.Background())
	got := checks[modelCheckKey("openrouter", "qwen/qwen3.5-flash:free")]
	if got.Status != "free_verified" || got.LatencyMS != 123 {
		t.Fatalf("status not persisted: %+v", checks)
	}
}

func TestTGAdminModelCheckRejectsPaidModel(t *testing.T) {
	s, _ := newTGModelTestServer(t)
	s.cfgRef.Telegram.BotToken = "123:abc"
	s.cfgRef.Telegram.OwnerChatID = 42
	s.settings = &fakeSettingsStore{values: map[string]string{}}

	body := strings.NewReader(`{"role":"simple","provider":"openrouter","model_id":"qwen/qwen3.5-flash"}`)
	req := httptest.NewRequest(http.MethodPost, "/tg-admin/api/model/check", body)
	req.Header.Set("X-Telegram-Init-Data", signedTGInitData(t, "123:abc", 42, time.Now()))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.handleTGAdminRouter(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestFailedModelCheckBlocksFreeModelsThatBecamePaid(t *testing.T) {
	cases := []string{
		"api error (HTTP 404): This model is unavailable for free. The paid version is available now - use this slug instead: minimax/minimax-m2.5",
		"api error (HTTP 404): Hy3 preview is no longer available as a free model. It has transitioned to a paid model.",
	}
	for _, message := range cases {
		status := failedModelCheck(message, 23)
		if status.Status != "free_blocked" {
			t.Fatalf("status = %s, want free_blocked for %q", status.Status, message)
		}
	}
}

func TestFailedModelCheckKeepsTransientFailuresDegraded(t *testing.T) {
	status := failedModelCheck("api error (HTTP 500): upstream temporarily unavailable", 91)
	if status.Status != "free_degraded" {
		t.Fatalf("status = %s, want free_degraded", status.Status)
	}
}

func TestLoadModelChecksNormalizesPaidFreeModelErrors(t *testing.T) {
	s := newTestServer(t)
	checks := map[string]modelCheckStatus{
		modelCheckKey("openrouter", "minimax/minimax-m2.5:free"): {
			Status: "free_degraded",
			Error:  "api error (HTTP 404): This model is unavailable for free. The paid version is available now.",
		},
	}
	data, err := json.Marshal(checks)
	if err != nil {
		t.Fatal(err)
	}
	s.settings = &fakeSettingsStore{values: map[string]string{settingKeyModelChecks: string(data)}}

	got := s.modelCheckStatus(context.Background(), "openrouter", "minimax/minimax-m2.5:free")
	if got.Status != "free_blocked" {
		t.Fatalf("status = %s, want free_blocked", got.Status)
	}
}

func TestFreeModelCheckCandidatesSkipFreshChecks(t *testing.T) {
	s, _ := newTGModelTestServer(t)
	fresh := time.Now().Add(-time.Hour).Format(time.RFC3339)
	stale := time.Now().Add(-48 * time.Hour).Format(time.RFC3339)
	checks := map[string]modelCheckStatus{
		modelCheckKey("openrouter", "qwen/qwen3.5-flash:free"): {
			Status:    "free_verified",
			CheckedAt: fresh,
		},
		modelCheckKey("openrouter", "qwen/qwen3.5-old:free"): {
			Status:    "free_degraded",
			CheckedAt: stale,
		},
	}
	data, err := json.Marshal(checks)
	if err != nil {
		t.Fatal(err)
	}
	s.settings = &fakeSettingsStore{values: map[string]string{settingKeyModelChecks: string(data)}}
	s.capStore = fakeCapabilityStore{byProvider: map[string]map[string]llm.Capabilities{
		"openrouter": {
			"qwen/qwen3.5-flash:free": {Tools: true},
			"qwen/qwen3.5-old:free":   {Tools: true},
			"qwen/qwen3.5-new:free":   {Tools: true},
			"qwen/qwen3.5-paid":       {Tools: true, PromptPrice: 0.1},
		},
	}}

	candidates := s.freeModelCheckCandidates(context.Background(), "openrouter")
	if len(candidates) != 2 {
		t.Fatalf("candidates len = %d, want 2: %+v", len(candidates), candidates)
	}
	if candidates[0].modelID != "qwen/qwen3.5-new:free" || candidates[1].modelID != "qwen/qwen3.5-old:free" {
		t.Fatalf("unexpected candidates order: %+v", candidates)
	}
}

func TestSettingSpecsIncludeModelCheckControls(t *testing.T) {
	s := newTestServer(t)
	keys := map[string]bool{}
	for _, spec := range s.settingSpecs() {
		keys[spec.Key] = true
	}
	for _, key := range []string{
		SettingKeyModelCheckIntervalHours,
		SettingKeyModelCheckInitialDelayMins,
		SettingKeyModelCheckStaleHours,
		SettingKeyModelCheckBatchLimit,
	} {
		if !keys[key] {
			t.Fatalf("setting %s is not exposed", key)
		}
	}
}

func TestTGAdminSummaryIncludesModelCheckCounts(t *testing.T) {
	s, _ := newTGModelTestServer(t)
	checks := map[string]modelCheckStatus{
		modelCheckKey("openrouter", "verified:free"): {Status: "free_verified", CheckedAt: time.Now().Format(time.RFC3339)},
		modelCheckKey("openrouter", "blocked:free"):  {Status: "free_blocked", CheckedAt: time.Now().Format(time.RFC3339)},
	}
	data, err := json.Marshal(checks)
	if err != nil {
		t.Fatal(err)
	}
	s.settings = &fakeSettingsStore{values: map[string]string{settingKeyModelChecks: string(data)}}

	summary := s.buildTGAdminSummary(context.Background())
	if summary.ModelChecks.Total != 2 || summary.ModelChecks.Verified != 1 || summary.ModelChecks.Blocked != 1 {
		t.Fatalf("unexpected model check summary: %+v", summary.ModelChecks)
	}
}

func TestTGAdminRunModelChecksRunsSweep(t *testing.T) {
	s, _ := newTGModelTestServer(t)
	s.cfgRef.Telegram.BotToken = "123:abc"
	s.cfgRef.Telegram.OwnerChatID = 42
	s.settings = &fakeSettingsStore{values: map[string]string{}}
	s.modelProbe = func(context.Context, string, string, llm.Capabilities) modelCheckStatus {
		return modelCheckStatus{Status: "free_verified", CheckedAt: time.Now().Format(time.RFC3339), LatencyMS: 10}
	}

	req := httptest.NewRequest(http.MethodPost, "/tg-admin/api/model-checks/run", nil)
	req.Header.Set("X-Telegram-Init-Data", signedTGInitData(t, "123:abc", 42, time.Now()))
	rec := httptest.NewRecorder()
	s.handleTGAdminRouter(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload modelCheckSweepResult
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Checked != 1 || payload.Statuses["free_verified"] != 1 {
		t.Fatalf("unexpected sweep result: %+v", payload)
	}
	check := s.modelCheckStatus(context.Background(), "openrouter", "qwen/qwen3.5-flash:free")
	if check.Status != "free_verified" {
		t.Fatalf("check not persisted: %+v", check)
	}
}

func TestTGAdminModelSetUpdatesCurrentRoleSlot(t *testing.T) {
	s, provider := newTGModelTestServer(t)
	s.cfgRef.Telegram.BotToken = "123:abc"
	s.cfgRef.Telegram.OwnerChatID = 42

	body := strings.NewReader(`{"role":"default","provider":"openrouter","model_id":"qwen/qwen3.5-plus"}`)
	req := httptest.NewRequest(http.MethodPost, "/tg-admin/api/model", body)
	req.Header.Set("X-Telegram-Init-Data", signedTGInitData(t, "123:abc", 42, time.Now()))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.handleTGAdminRouter(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if provider.CurrentModel() != "qwen/qwen3.5-plus" {
		t.Fatalf("model = %q, want qwen/qwen3.5-plus", provider.CurrentModel())
	}
	if provider.caps.PromptPrice != 1.0 {
		t.Fatalf("caps not applied: %+v", provider.caps)
	}
}

func TestTGAdminModelSetRejectsUnverifiedFreeModel(t *testing.T) {
	s, _ := newTGModelTestServer(t)
	s.cfgRef.Telegram.BotToken = "123:abc"
	s.cfgRef.Telegram.OwnerChatID = 42
	s.settings = &fakeSettingsStore{values: map[string]string{}}

	body := strings.NewReader(`{"role":"default","provider":"openrouter","model_id":"qwen/qwen3.5-flash:free"}`)
	req := httptest.NewRequest(http.MethodPost, "/tg-admin/api/model", body)
	req.Header.Set("X-Telegram-Init-Data", signedTGInitData(t, "123:abc", 42, time.Now()))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.handleTGAdminRouter(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestTGAdminModelSetAllowsVerifiedFreeModel(t *testing.T) {
	s, provider := newTGModelTestServer(t)
	s.cfgRef.Telegram.BotToken = "123:abc"
	s.cfgRef.Telegram.OwnerChatID = 42
	checks := map[string]modelCheckStatus{
		modelCheckKey("openrouter", "qwen/qwen3.5-flash:free"): {Status: "free_verified", CheckedAt: time.Now().Format(time.RFC3339)},
	}
	data, err := json.Marshal(checks)
	if err != nil {
		t.Fatal(err)
	}
	s.settings = &fakeSettingsStore{values: map[string]string{settingKeyModelChecks: string(data)}}

	body := strings.NewReader(`{"role":"default","provider":"openrouter","model_id":"qwen/qwen3.5-flash:free"}`)
	req := httptest.NewRequest(http.MethodPost, "/tg-admin/api/model", body)
	req.Header.Set("X-Telegram-Init-Data", signedTGInitData(t, "123:abc", 42, time.Now()))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.handleTGAdminRouter(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if provider.CurrentModel() != "qwen/qwen3.5-flash:free" {
		t.Fatalf("model = %q, want qwen/qwen3.5-flash:free", provider.CurrentModel())
	}
}

type fakeTGOperationsAgent struct {
	clearedChatID int64
	compactChatID int64
	compactErr    error
	stats         store.ChatStats
	statsOK       bool
	tools         []agent.ToolInfo
}

func (f *fakeTGOperationsAgent) ClearHistory(chatID int64) {
	f.clearedChatID = chatID
}

func (f *fakeTGOperationsAgent) Compact(_ context.Context, chatID int64) error {
	f.compactChatID = chatID
	return f.compactErr
}

func (f *fakeTGOperationsAgent) GetStats(int64) (store.ChatStats, bool) {
	return f.stats, f.statsOK
}

func (f *fakeTGOperationsAgent) ListTools() []agent.ToolInfo {
	return f.tools
}

type fakeMCPReloader struct {
	configs   map[string]config.MCPServerConfig
	toolCount int
}

func (f *fakeMCPReloader) ReloadMCP(_ context.Context, configs map[string]config.MCPServerConfig) (int, error) {
	f.configs = configs
	return f.toolCount, nil
}

type fakeSettingsStore struct {
	values map[string]string
}

func (f *fakeSettingsStore) GetSetting(_ context.Context, key string) (string, bool, error) {
	v, ok := f.values[key]
	return v, ok, nil
}

func (f *fakeSettingsStore) PutSetting(_ context.Context, key, value string) error {
	if f.values == nil {
		f.values = map[string]string{}
	}
	f.values[key] = value
	return nil
}

func TestTGAdminStatsReturnsOwnerChatStats(t *testing.T) {
	s := newTestServer(t)
	s.cfgRef.Telegram.BotToken = "123:abc"
	s.cfgRef.Telegram.OwnerChatID = 42
	s.opsAgent = &fakeTGOperationsAgent{
		statsOK: true,
		stats: store.ChatStats{
			ActiveMessages: 7,
			ActiveChars:    1234,
			LastMessageAt:  time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC),
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/tg-admin/api/stats", nil)
	req.Header.Set("X-Telegram-Init-Data", signedTGInitData(t, "123:abc", 42, time.Now()))
	rec := httptest.NewRecorder()
	s.handleTGAdminRouter(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload tgAdminStats
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.Available || payload.ActiveMessages != 7 || payload.ActiveChars != 1234 {
		t.Fatalf("unexpected stats: %+v", payload)
	}
}

func TestTGAdminToolsReturnsGroupedTools(t *testing.T) {
	s := newTestServer(t)
	s.cfgRef.Telegram.BotToken = "123:abc"
	s.cfgRef.Telegram.OwnerChatID = 42
	s.opsAgent = &fakeTGOperationsAgent{tools: []agent.ToolInfo{
		{Name: "search", ServerName: "memory"},
		{Name: "recall", ServerName: "memory"},
		{Name: "query", ServerName: "finance"},
	}}

	req := httptest.NewRequest(http.MethodGet, "/tg-admin/api/tools", nil)
	req.Header.Set("X-Telegram-Init-Data", signedTGInitData(t, "123:abc", 42, time.Now()))
	rec := httptest.NewRecorder()
	s.handleTGAdminRouter(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload []tgAdminToolGroup
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload) != 2 || payload[0].Server != "finance" || payload[1].Server != "memory" {
		t.Fatalf("unexpected groups: %+v", payload)
	}
	if strings.Join(payload[1].Tools, ",") != "recall,search" {
		t.Fatalf("memory tools not sorted: %+v", payload[1].Tools)
	}
}

func TestTGAdminActionRejectsUnknownAction(t *testing.T) {
	s := newTestServer(t)
	s.cfgRef.Telegram.BotToken = "123:abc"
	s.cfgRef.Telegram.OwnerChatID = 42

	req := httptest.NewRequest(http.MethodPost, "/tg-admin/api/action", strings.NewReader(`{"action":"bad"}`))
	req.Header.Set("X-Telegram-Init-Data", signedTGInitData(t, "123:abc", 42, time.Now()))
	rec := httptest.NewRecorder()
	s.handleTGAdminRouter(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestTGAdminClearActionClearsOwnerChat(t *testing.T) {
	s := newTestServer(t)
	s.cfgRef.Telegram.BotToken = "123:abc"
	s.cfgRef.Telegram.OwnerChatID = 42
	ops := &fakeTGOperationsAgent{statsOK: true}
	s.opsAgent = ops

	req := httptest.NewRequest(http.MethodPost, "/tg-admin/api/action", strings.NewReader(`{"action":"clear"}`))
	req.Header.Set("X-Telegram-Init-Data", signedTGInitData(t, "123:abc", 42, time.Now()))
	rec := httptest.NewRecorder()
	s.handleTGAdminRouter(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if ops.clearedChatID != 42 {
		t.Fatalf("cleared chat id = %d, want 42", ops.clearedChatID)
	}
}

func TestTGAdminMCPReloadUsesSettingsConfig(t *testing.T) {
	s := newTestServer(t)
	s.cfgRef.Telegram.BotToken = "123:abc"
	s.cfgRef.Telegram.OwnerChatID = 42
	serversJSON := `{"memory":{"type":"http","url":"http://memory:8080"}}`
	s.settings = &fakeSettingsStore{values: map[string]string{SettingKeyMCPServers: serversJSON}}
	reloader := &fakeMCPReloader{toolCount: 3}
	s.reloader = reloader

	req := httptest.NewRequest(http.MethodPost, "/tg-admin/api/action", strings.NewReader(`{"action":"mcp_reload"}`))
	req.Header.Set("X-Telegram-Init-Data", signedTGInitData(t, "123:abc", 42, time.Now()))
	rec := httptest.NewRecorder()
	s.handleTGAdminRouter(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if len(reloader.configs) != 1 || reloader.configs["memory"].URL != "http://memory:8080" {
		t.Fatalf("unexpected reload configs: %+v", reloader.configs)
	}
}

// TestAuthRequired covers the four auth paths + the 401 default.
func TestAuthRequired(t *testing.T) {
	s := newTestServer(t)
	mux := http.NewServeMux()
	s.registerRoutes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	tests := []struct {
		name     string
		prepare  func(*http.Request)
		wantCode int
	}{
		{"no auth", func(r *http.Request) {}, http.StatusUnauthorized},
		{"wrong bearer", func(r *http.Request) { r.Header.Set("Authorization", "Bearer wrong") }, http.StatusUnauthorized},
		{"good bearer", func(r *http.Request) { r.Header.Set("Authorization", "Bearer secret-token") }, http.StatusOK},
		{"good cookie", func(r *http.Request) { r.AddCookie(&http.Cookie{Name: authCookieName, Value: "secret-token"}) }, http.StatusOK},
		{"wrong cookie", func(r *http.Request) { r.AddCookie(&http.Cookie{Name: authCookieName, Value: "wrong"}) }, http.StatusUnauthorized},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest("GET", srv.URL+"/", nil)
			tc.prepare(req)
			resp, err := srv.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.wantCode {
				t.Errorf("got %d, want %d", resp.StatusCode, tc.wantCode)
			}
		})
	}
}

// TestAuthTokenQueryBootstrap: ?token=... sets cookie and redirects.
func TestAuthTokenQueryBootstrap(t *testing.T) {
	s := newTestServer(t)
	mux := http.NewServeMux()
	s.registerRoutes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Get(srv.URL + "/?token=secret-token")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("expected 303, got %d", resp.StatusCode)
	}
	var found bool
	for _, c := range resp.Cookies() {
		if c.Name == authCookieName && c.Value == "secret-token" {
			found = true
			if !c.HttpOnly {
				t.Error("cookie should be HttpOnly")
			}
		}
	}
	if !found {
		t.Error("auth cookie not set")
	}
}

// TestAuthForwardAuth: when TrustForwardAuth and the header is set, request passes.
func TestAuthForwardAuth(t *testing.T) {
	s := newTestServer(t)
	s.cfg.TrustForwardAuth = true
	s.cfg.ForwardAuthHeader = "X-authentik-username"
	s.cfg.TrustedProxyCIDRs = []string{"127.0.0.1/32", "::1/128"}
	mux := http.NewServeMux()
	s.registerRoutes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// With header → 200 on an authed route.
	req, _ := http.NewRequest("GET", srv.URL+"/routing", nil)
	req.Header.Set("X-authentik-username", "alice")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("forward-auth req: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 with forward-auth header, got %d", resp.StatusCode)
	}

	// Without header → 401.
	req2, _ := http.NewRequest("GET", srv.URL+"/routing", nil)
	resp2, err := srv.Client().Do(req2)
	if err != nil {
		t.Fatalf("no-auth req: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 without forward-auth header, got %d", resp2.StatusCode)
	}
}

func TestAuthForwardAuthRequiresTrustedProxy(t *testing.T) {
	s := newTestServer(t)
	s.cfg.TrustForwardAuth = true
	s.cfg.ForwardAuthHeader = "X-authentik-username"
	s.cfg.TrustedProxyCIDRs = []string{"10.0.0.0/8"}

	called := false
	handler := s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/routing", nil)
	req.RemoteAddr = "203.0.113.10:5555"
	req.Header.Set("X-authentik-username", "alice")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if called {
		t.Fatal("forward-auth handler should not be called for an untrusted remote")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

// TestHealthz: always reachable.
func TestHealthz(t *testing.T) {
	s := newTestServer(t)
	mux := http.NewServeMux()
	s.registerRoutes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := srv.Client().Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("healthz: %d", resp.StatusCode)
	}
	body := make([]byte, 64)
	n, _ := resp.Body.Read(body)
	if !strings.Contains(string(body[:n]), `"ok":true`) {
		t.Errorf("healthz body: %s", string(body[:n]))
	}
}

type fakeChatAgent struct {
	history []store.HistoryItem
}

func (f *fakeChatAgent) Process(context.Context, int64, llm.Message, func(string)) (string, error) {
	return "", nil
}

func (f *fakeChatAgent) ProcessStream(context.Context, int64, llm.Message, func(string), func(string)) (string, error) {
	return "", nil
}

func (f *fakeChatAgent) GetChatHistory(int64) []llm.Message { return nil }

func (f *fakeChatAgent) GetDisplayHistory(_ int64, limit, offset int) []store.HistoryItem {
	if offset >= len(f.history) {
		return nil
	}
	end := offset + limit
	if end > len(f.history) {
		end = len(f.history)
	}
	return f.history[offset:end]
}

func (f *fakeChatAgent) ClearChatHistory(int64) {}

func (f *fakeChatAgent) PopLastUserTurn(int64) (string, bool) { return "", false }

func TestChatInitialAssistantUsesBotClassAndNoRequiredTextarea(t *testing.T) {
	s := newTestServer(t)
	s.SetAgent(&fakeChatAgent{history: []store.HistoryItem{{
		Role:      "assistant",
		Content:   "hello",
		CreatedAt: time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC),
	}}})

	req := httptest.NewRequest(http.MethodGet, "/chat", nil)
	rec := httptest.NewRecorder()
	s.handleChat(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, `chat-msg chat-msg--bot`) {
		t.Fatalf("assistant bubble should use bot class, body:\n%s", body)
	}
	if strings.Contains(body, `chat-msg--assistant`) {
		t.Fatalf("assistant class leaked into initial chat render:\n%s", body)
	}
	if strings.Contains(body, "required></textarea>") {
		t.Fatal("chat textarea should not be marked required; image-only submits must be allowed")
	}
}

func TestChatHistoryEscapesLazyLoadedMessages(t *testing.T) {
	s := newTestServer(t)
	s.SetAgent(&fakeChatAgent{history: []store.HistoryItem{{
		Role:      "user",
		Content:   `<img src=x onerror=alert(1)>`,
		ImageURLs: []string{`x" onerror="alert(2)`},
		CreatedAt: time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC),
	}}})

	req := httptest.NewRequest(http.MethodGet, "/chat/history?offset=0", nil)
	rec := httptest.NewRecorder()
	s.handleChatHistory(rec, req)

	resp := rec.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	html, _ := payload["html"].(string)
	if strings.Contains(html, `<img src=x`) || strings.Contains(html, `src="x&quot; onerror`) {
		t.Fatalf("lazy history response contains unsafe HTML: %s", html)
	}
	if !strings.Contains(html, `&lt;img src=x onerror=alert(1)&gt;`) {
		t.Fatalf("lazy history did not preserve escaped user text: %s", html)
	}
}
