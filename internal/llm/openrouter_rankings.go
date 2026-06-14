package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const openRouterRankingsSettingsKey = "openrouter.rankings.cache"
const openRouterRankingsCacheTTL = 6 * time.Hour

// OpenRouterMarketSignal is an advisory usage/ranking signal from OpenRouter's
// public rankings frontend. It must not be treated as a quality benchmark.
type OpenRouterMarketSignal struct {
	ModelID    string   `json:"model_id"`
	Rank       int      `json:"rank,omitempty"`
	Share      float64  `json:"share,omitempty"`
	Score      float64  `json:"score,omitempty"`
	Categories []string `json:"categories,omitempty"`
	Source     string   `json:"source"`
}

// OpenRouterRankingsCache is stored in kv_settings. Keyed by OpenRouter model id.
type OpenRouterRankingsCache struct {
	FetchedAt time.Time                         `json:"fetched_at"`
	Models    map[string]OpenRouterMarketSignal `json:"models"`
}

type openRouterRankingsEndpoint struct {
	Path   string
	Query  url.Values
	Source string
}

var openRouterRankingEndpoints = []openRouterRankingsEndpoint{
	{Path: "/api/frontend/rankings/tools", Source: "tools"},
	{Path: "/api/frontend/rankings/programming-language", Query: url.Values{"tag": {"python"}}, Source: "programming:python"},
	{Path: "/api/frontend/rankings/use-case-category", Query: url.Values{"category": {"programming"}}, Source: "use-case:programming"},
}

// FetchOpenRouterRankings fetches advisory market signals from OpenRouter's
// rankings frontend endpoints. These endpoints are intentionally isolated from
// FetchOpenRouterModels because they are not the stable catalog API.
func FetchOpenRouterRankings(ctx context.Context) (map[string]OpenRouterMarketSignal, error) {
	out := map[string]OpenRouterMarketSignal{}
	var errs []string
	for _, ep := range openRouterRankingEndpoints {
		signals, err := fetchOpenRouterRankingEndpoint(ctx, ep)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		mergeMarketSignals(out, signals)
	}
	if len(out) == 0 && len(errs) > 0 {
		return nil, fmt.Errorf("openrouter rankings: %s", strings.Join(errs, "; "))
	}
	return out, nil
}

func fetchOpenRouterRankingEndpoint(ctx context.Context, ep openRouterRankingsEndpoint) (map[string]OpenRouterMarketSignal, error) {
	u := url.URL{Scheme: "https", Host: "openrouter.ai", Path: ep.Path}
	if ep.Query != nil {
		u.RawQuery = ep.Query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := openRouterHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ep.Path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 5*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("%s: read: %w", ep.Path, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d: %s", ep.Path, resp.StatusCode, string(body))
	}
	signals, err := parseOpenRouterRankingSignals(body, ep.Source)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ep.Path, err)
	}
	return signals, nil
}

func parseOpenRouterRankingSignals(body []byte, source string) (map[string]OpenRouterMarketSignal, error) {
	var root any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&root); err != nil {
		return nil, fmt.Errorf("parse rankings json: %w", err)
	}
	rows := make([]OpenRouterMarketSignal, 0)
	collectRankingSignals(root, source, &rows)
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Rank > 0 && rows[j].Rank > 0 {
			return rows[i].Rank < rows[j].Rank
		}
		return rows[i].Score > rows[j].Score
	})
	out := make(map[string]OpenRouterMarketSignal, len(rows))
	for i, row := range rows {
		if row.ModelID == "" {
			continue
		}
		if row.Rank == 0 {
			row.Rank = i + 1
		}
		mergeMarketSignals(out, map[string]OpenRouterMarketSignal{row.ModelID: row})
	}
	return out, nil
}

func collectRankingSignals(v any, source string, out *[]OpenRouterMarketSignal) {
	switch x := v.(type) {
	case []any:
		for _, item := range x {
			collectRankingSignals(item, source, out)
		}
	case map[string]any:
		if sig, ok := signalFromRankingObject(x, source); ok {
			*out = append(*out, sig)
		}
		for _, item := range x {
			collectRankingSignals(item, source, out)
		}
	}
}

func signalFromRankingObject(obj map[string]any, source string) (OpenRouterMarketSignal, bool) {
	id := firstString(obj,
		"model_id", "modelId", "modelID", "model_slug", "modelSlug", "slug", "id",
	)
	if id == "" {
		if nested, ok := obj["model"].(map[string]any); ok {
			id = firstString(nested, "id", "slug", "model_id", "modelId")
		}
	}
	if !looksLikeOpenRouterModelID(id) {
		return OpenRouterMarketSignal{}, false
	}
	return OpenRouterMarketSignal{
		ModelID: id,
		Rank:    firstInt(obj, "rank", "position", "index"),
		Share:   firstFloat(obj, "share", "token_share", "tokenShare", "percentage", "percent"),
		Score:   firstFloat(obj, "score", "value", "tokens", "usage", "count"),
		Source:  source,
	}, true
}

func looksLikeOpenRouterModelID(id string) bool {
	if id == "" || strings.ContainsAny(id, " \t\n\r") {
		return false
	}
	creator, model, ok := strings.Cut(id, "/")
	return ok && creator != "" && model != ""
}

func mergeMarketSignals(dst map[string]OpenRouterMarketSignal, src map[string]OpenRouterMarketSignal) {
	for id, incoming := range src {
		if incoming.ModelID == "" {
			incoming.ModelID = id
		}
		existing, ok := dst[id]
		if !ok {
			incoming.Categories = appendCategory(incoming.Categories, incoming.Source)
			dst[id] = incoming
			continue
		}
		if incoming.Rank > 0 && (existing.Rank == 0 || incoming.Rank < existing.Rank) {
			existing.Rank = incoming.Rank
		}
		if incoming.Share > existing.Share {
			existing.Share = incoming.Share
		}
		if incoming.Score > existing.Score {
			existing.Score = incoming.Score
		}
		existing.Categories = appendCategory(existing.Categories, incoming.Source)
		dst[id] = existing
	}
}

func appendCategory(categories []string, category string) []string {
	if category == "" {
		return categories
	}
	for _, existing := range categories {
		if existing == category {
			return categories
		}
	}
	return append(categories, category)
}

func firstString(obj map[string]any, keys ...string) string {
	for _, key := range keys {
		if v, ok := obj[key]; ok {
			if s, ok := v.(string); ok {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}

func firstInt(obj map[string]any, keys ...string) int {
	for _, key := range keys {
		if f := firstFloat(obj, key); f > 0 {
			return int(f)
		}
	}
	return 0
}

func firstFloat(obj map[string]any, keys ...string) float64 {
	for _, key := range keys {
		v, ok := obj[key]
		if !ok {
			continue
		}
		switch n := v.(type) {
		case json.Number:
			f, _ := n.Float64()
			return f
		case float64:
			return n
		case string:
			f, _ := strconv.ParseFloat(strings.TrimSuffix(n, "%"), 64)
			return f
		}
	}
	return 0
}

func StoreOpenRouterRankingsCache(ctx context.Context, settings SettingsStore, models map[string]OpenRouterMarketSignal) error {
	cache := OpenRouterRankingsCache{FetchedAt: time.Now(), Models: models}
	data, err := json.Marshal(cache)
	if err != nil {
		return fmt.Errorf("openrouter rankings cache marshal: %w", err)
	}
	return settings.PutSetting(ctx, openRouterRankingsSettingsKey, string(data))
}

func LoadOpenRouterRankingsCache(ctx context.Context, settings SettingsStore) (*OpenRouterRankingsCache, error) {
	raw, ok, err := settings.GetSetting(ctx, openRouterRankingsSettingsKey)
	if err != nil || !ok {
		return nil, err
	}
	var cache OpenRouterRankingsCache
	if err := json.Unmarshal([]byte(raw), &cache); err != nil {
		return nil, fmt.Errorf("openrouter rankings cache unmarshal: %w", err)
	}
	if time.Since(cache.FetchedAt) > openRouterRankingsCacheTTL {
		return nil, nil
	}
	return &cache, nil
}
