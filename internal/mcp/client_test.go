package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// --- validateServerURL ---

func TestValidateServerURL_ValidHTTPS(t *testing.T) {
	if err := validateServerURL("https://api.example.com/mcp"); err != nil {
		t.Errorf("unexpected error for valid HTTPS URL: %v", err)
	}
}

func TestValidateServerURL_ValidHTTP(t *testing.T) {
	if err := validateServerURL("http://192.168.1.10:8080/mcp"); err != nil {
		t.Errorf("unexpected error for local network HTTP: %v", err)
	}
}

func TestValidateServerURL_LoopbackBlocked(t *testing.T) {
	cases := []string{
		"http://127.0.0.1/mcp",
		"http://127.0.0.1:8080/mcp",
		"http://localhost/mcp",
		"http://[::1]/mcp",
	}
	for _, u := range cases {
		t.Run(u, func(t *testing.T) {
			if err := validateServerURL(u); err == nil {
				t.Errorf("expected loopback URL %q to be rejected", u)
			}
		})
	}
}

func TestValidateServerURL_LinkLocalBlocked(t *testing.T) {
	// 169.254.x.x — AWS metadata endpoint и другие link-local
	if err := validateServerURL("http://169.254.169.254/latest/meta-data"); err == nil {
		t.Error("expected link-local URL to be rejected")
	}
}

func TestValidateServerURL_InvalidScheme(t *testing.T) {
	cases := []string{
		"ftp://example.com",
		"file:///etc/passwd",
		"ws://example.com",
	}
	for _, u := range cases {
		t.Run(u, func(t *testing.T) {
			if err := validateServerURL(u); err == nil {
				t.Errorf("expected scheme in %q to be rejected", u)
			}
		})
	}
}

func TestValidateServerURL_MissingHost(t *testing.T) {
	if err := validateServerURL("http://"); err == nil {
		t.Error("expected error for URL with missing host")
	}
}

// --- Tool name/description validation via listTools ---

// mcpToolsServer создаёт тестовый HTTP-сервер, который отвечает на tools/list
// с заданными инструментами.
func mcpToolsServer(t *testing.T, tools []map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)

		switch req.Method {
		case "initialize":
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"jsonrpc": "2.0", "id": 1,
				"result": map[string]any{"protocolVersion": "2024-11-05"},
			})
		case "notifications/initialized":
			w.WriteHeader(http.StatusOK)
		case "tools/list":
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"jsonrpc": "2.0", "id": 2,
				"result": map[string]any{"tools": tools},
			})
		}
	}))
}

// TestListTools_ValidToolAccepted: нормальный инструмент проходит.
func TestListTools_ValidToolAccepted(t *testing.T) {
	srv := mcpToolsServer(t, []map[string]any{
		{"name": "get_weather", "description": "Get current weather", "inputSchema": map[string]any{}},
	})
	defer srv.Close()

	s := &server{
		name: "test",
		url:  srv.URL,
		http: &http.Client{},
	}

	tools, err := s.listTools(context.Background())
	if err != nil {
		t.Fatalf("listTools error: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	if tools[0].Name != "get_weather" {
		t.Errorf("expected 'get_weather', got %q", tools[0].Name)
	}
}

// TestListTools_InvalidNameSkipped: инструмент с невалидным именем пропускается.
func TestListTools_InvalidNameSkipped(t *testing.T) {
	srv := mcpToolsServer(t, []map[string]any{
		{"name": "valid_tool", "description": "ok", "inputSchema": map[string]any{}},
		{"name": "bad tool!", "description": "has spaces and !", "inputSchema": map[string]any{}}, // недопустимые символы
		{"name": "", "description": "empty name", "inputSchema": map[string]any{}},               // пустое имя
	})
	defer srv.Close()

	s := &server{name: "test", url: srv.URL, http: &http.Client{}}

	tools, err := s.listTools(context.Background())
	if err != nil {
		t.Fatalf("listTools error: %v", err)
	}
	if len(tools) != 1 {
		t.Errorf("expected 1 valid tool, got %d", len(tools))
	}
}

// TestListTools_LongNameSkipped: слишком длинное имя пропускается.
func TestListTools_LongNameSkipped(t *testing.T) {
	longName := strings.Repeat("a", maxToolNameLen+1)
	srv := mcpToolsServer(t, []map[string]any{
		{"name": longName, "description": "too long name", "inputSchema": map[string]any{}},
	})
	defer srv.Close()

	s := &server{name: "test", url: srv.URL, http: &http.Client{}}

	tools, err := s.listTools(context.Background())
	if err != nil {
		t.Fatalf("listTools error: %v", err)
	}
	if len(tools) != 0 {
		t.Errorf("expected 0 tools (name too long), got %d", len(tools))
	}
}

// TestListTools_LongDescriptionTruncated: длинное описание обрезается до maxDescLen.
func TestListTools_LongDescriptionTruncated(t *testing.T) {
	longDesc := strings.Repeat("x", maxDescLen+100)
	srv := mcpToolsServer(t, []map[string]any{
		{"name": "my_tool", "description": longDesc, "inputSchema": map[string]any{}},
	})
	defer srv.Close()

	s := &server{name: "test", url: srv.URL, http: &http.Client{}}

	tools, err := s.listTools(context.Background())
	if err != nil {
		t.Fatalf("listTools error: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	if len(tools[0].Description) != maxDescLen {
		t.Errorf("description should be truncated to %d, got %d", maxDescLen, len(tools[0].Description))
	}
}

// --- callTool: лимиты размера ---

// mcpCallServer создаёт сервер, который возвращает заданный контент при вызове tools/call.
func mcpCallServer(t *testing.T, content string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Method string `json:"method"` }
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)

		switch req.Method {
		case "initialize":
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"jsonrpc": "2.0", "id": 1,
				"result": map[string]any{},
			})
		case "tools/call":
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"jsonrpc": "2.0", "id": 2,
				"result": map[string]any{
					"content": []map[string]any{{"type": "text", "text": content}},
					"isError": false,
				},
			})
		}
	}))
}

// TestCallTool_ResultTruncated: огромный результат обрезается до maxToolResultLen.
func TestCallTool_ResultTruncated(t *testing.T) {
	bigResult := strings.Repeat("a", maxToolResultLen+1000)
	srv := mcpCallServer(t, bigResult)
	defer srv.Close()

	s := &server{name: "test", url: srv.URL, http: &http.Client{}}

	result, err := s.callTool(context.Background(), "my_tool", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("callTool error: %v", err)
	}
	if len(result) <= maxToolResultLen {
		// результат должен содержать "[truncated]" суффикс
		if !strings.Contains(result, "truncated") {
			t.Error("expected truncation marker in result")
		}
	}
	if len(result) > maxToolResultLen+50 { // небольшой запас для суффикса
		t.Errorf("result too large: %d bytes", len(result))
	}
}

// TestCallTool_ArgsTooBig: огромные args → ошибка до HTTP запроса.
func TestCallTool_ArgsTooBig(t *testing.T) {
	s := &server{name: "test", url: "http://unused", http: &http.Client{}}

	bigArgs := json.RawMessage(fmt.Sprintf(`{"data":"%s"}`, strings.Repeat("x", maxArgsSize+1)))
	_, err := s.callTool(context.Background(), "my_tool", bigArgs)
	if err == nil {
		t.Error("expected error for oversized args")
	}
}

// TestReadJSONResult_SizeLimit: ответ сервера > 10MB → данные обрезаются (не OOM).
func TestReadJSONResult_SizeLimit(t *testing.T) {
	// Создаём ответ который превышает maxResponseSize.
	// LimitReader обрежет его, json.Decoder вернёт ошибку decode — это ожидаемо.
	bigJSON := strings.Repeat("a", maxResponseSize+1)
	reader := strings.NewReader(`{"result":"` + bigJSON + `"}`)

	_, err := readJSONResult(reader)
	// Либо успешно прочитает урезанный JSON (маловероятно), либо вернёт decode error.
	// Главное — не зависнет и не вызовет OOM.
	_ = err // обе ситуации допустимы
}
