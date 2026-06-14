package telegram

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNormalizeRichMarkdownConvertsImagesToLinks(t *testing.T) {
	got := normalizeRichMarkdown(`Report ![Chart](https://example.com/chart.png "Q1") done`)
	want := `Report [Chart](https://example.com/chart.png) done`
	if got != want {
		t.Fatalf("\n got: %q\nwant: %q", got, want)
	}
}

func TestNormalizeRichMarkdownEscapesMediaHTML(t *testing.T) {
	got := normalizeRichMarkdown(`<figure><img src="https://example.com/a.jpg"/><figcaption>Caption</figcaption></figure>`)
	if strings.Contains(got, "<img") || strings.Contains(got, "<figure") || strings.Contains(got, "<figcaption") {
		t.Fatalf("media HTML tag was not escaped: %s", got)
	}
	if !strings.Contains(got, "&lt;img") || !strings.Contains(got, "&lt;figure") {
		t.Fatalf("escaped media HTML missing: %s", got)
	}
}

func TestRichMarkdownEligibleRejectsOversizedPayload(t *testing.T) {
	if !richMarkdownEligible(strings.Repeat("a", richMessageLimit)) {
		t.Fatal("payload at limit should be eligible")
	}
	if richMarkdownEligible(strings.Repeat("a", richMessageLimit+1)) {
		t.Fatal("payload over limit should be rejected")
	}
}

func TestSendRichMessagePayload(t *testing.T) {
	var path string
	var payload sendRichMessageRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Fatalf("Content-Type = %q, want application/json", ct)
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true,"result":{"message_id":10}}`)) //nolint:errcheck
	}))
	defer server.Close()

	err := sendRichMessageWithClient(server.Client(), server.URL+"/bot%s/%s", "123:token", 42, "# Title\n\n![Chart](https://example.com/chart.png)")
	if err != nil {
		t.Fatalf("sendRichMessageWithClient returned error: %v", err)
	}
	if path != "/bot123:token/sendRichMessage" {
		t.Fatalf("path = %q", path)
	}
	if payload.ChatID != 42 {
		t.Fatalf("chat_id = %d", payload.ChatID)
	}
	wantMarkdown := "# Title\n\n[Chart](https://example.com/chart.png)"
	if payload.RichMessage.Markdown != wantMarkdown {
		t.Fatalf("\n got markdown: %q\nwant markdown: %q", payload.RichMessage.Markdown, wantMarkdown)
	}
	if payload.RichMessage.HTML != "" {
		t.Fatalf("HTML field should be empty, got %q", payload.RichMessage.HTML)
	}
}

func TestSendRichMessageReturnsTelegramDescription(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"ok":false,"description":"Bad Request: rich markdown invalid"}`)) //nolint:errcheck
	}))
	defer server.Close()

	err := sendRichMessageWithClient(server.Client(), server.URL+"/bot%s/%s", "123:token", 42, "# Broken")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "Bad Request: rich markdown invalid") {
		t.Fatalf("telegram description missing from error: %v", err)
	}
}
