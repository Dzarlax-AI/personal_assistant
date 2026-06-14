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

	"telegram-agent/internal/config"
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
	router := llm.NewRouter(map[string]llm.Provider{"default-or": provider}, llm.RouterConfig{Default: "default-or"})
	cfg := &config.Config{
		Models: config.ModelsConfig{
			"default-or": config.ModelConfig{Provider: "openrouter", Model: "qwen/qwen3.5-flash"},
		},
	}
	capStore := fakeCapabilityStore{byProvider: map[string]map[string]llm.Capabilities{
		"openrouter": {
			"qwen/qwen3.5-flash": {Tools: true, Vision: true, PromptPrice: 0.1, CompletionPrice: 0.4, ContextLength: 64000, Score: 80},
			"qwen/qwen3.5-plus":  {Tools: true, Vision: true, PromptPrice: 1.0, CompletionPrice: 3.0, ContextLength: 128000, Score: 90},
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
		viewTGAdmin:       tgAdminData{FullAdminURL: "https://assistant.example/admin"},
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
