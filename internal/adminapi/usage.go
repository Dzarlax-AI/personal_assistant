package adminapi

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"telegram-agent/internal/llm"
)

// usageView is the data passed to the usage template. Populated by handleUsage
// from llm.UsageStore aggregations over three time windows.
type usageView struct {
	Period         string // "24h" / "7d" / "30d" — echoed back to keep the UI in sync
	Since          time.Time
	Totals         llm.UsageTotals
	TotalsToday    llm.UsageTotals // always computed for the forecast card, independent of Period
	Totals7d       llm.UsageTotals // always computed for the forecast card
	Totals30d      llm.UsageTotals
	CacheHitPct    float64 // 100 * cached_prompt / prompt
	ReasoningPct   float64 // 100 * reasoning / completion
	ForecastUSD    float64 // 30-day run-rate projected from 7d trailing
	ByModel        []llm.UsageModelRow
	ByRole         []llm.UsageRoleRow
	ExpensiveTurns []expensiveTurnView
	DailyChart     dailyChart
}

// expensiveTurnView trims the raw ExpensiveTurn fields for display — questions
// and answers get truncated to keep the table readable.
type expensiveTurnView struct {
	Ts         string
	ChatID     int64
	Role       string
	ModelID    string
	CostUSD    float64
	Tokens     int
	Question   string // first line, up to 200 chars
	Answer     string // first line, up to 200 chars
}

// dailyChart is a pre-sized SVG description rendered in the template — keeps
// the template free of math and the HTML free of JS dependencies.
type dailyChart struct {
	Width    int
	Height   int
	Bars     []dailyBar
	MaxCalls int
	MaxCost  float64
	Days     int
}

type dailyBar struct {
	X         int
	CallsY    int
	CallsH    int
	CostY     int
	CostH     int
	DayLabel  string // short label for x-axis, e.g. "04-20"
	Calls     int
	CostUSD   float64
}

func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request) {
	if s.usageStore == nil {
		http.Error(w, "usage store not configured", http.StatusServiceUnavailable)
		return
	}
	period := r.URL.Query().Get("period")
	since, label := resolvePeriod(period)

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	view, err := s.buildUsageView(ctx, since, label)
	if err != nil {
		s.logger.Error("usage: aggregation failed", "err", err)
		http.Error(w, "aggregation failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := render(w, viewUsage, view); err != nil {
		s.logger.Error("usage: render failed", "err", err)
		http.Error(w, "render error", http.StatusInternalServerError)
	}
}

func (s *Server) buildUsageView(ctx context.Context, since time.Time, label string) (usageView, error) {
	now := time.Now()
	v := usageView{Period: label, Since: since}

	// Primary period — drives the main table view.
	totals, err := s.usageStore.UsageTotals(ctx, since)
	if err != nil {
		return v, err
	}
	v.Totals = totals

	// Additional windows for the forecast/summary cards. Always the same three
	// windows regardless of active period, so the user gets a consistent glance.
	if v.TotalsToday, err = s.usageStore.UsageTotals(ctx, now.Add(-24*time.Hour)); err != nil {
		return v, err
	}
	if v.Totals7d, err = s.usageStore.UsageTotals(ctx, now.Add(-7*24*time.Hour)); err != nil {
		return v, err
	}
	if v.Totals30d, err = s.usageStore.UsageTotals(ctx, now.Add(-30*24*time.Hour)); err != nil {
		return v, err
	}
	// Monthly run-rate = 7d cost × (30/7).
	v.ForecastUSD = v.Totals7d.CostUSD * 30.0 / 7.0

	if v.Totals.PromptTokens > 0 {
		v.CacheHitPct = 100 * float64(v.Totals.CachedPromptTokens) / float64(v.Totals.PromptTokens)
	}
	if v.Totals.CompletionTokens > 0 {
		v.ReasoningPct = 100 * float64(v.Totals.ReasoningTokens) / float64(v.Totals.CompletionTokens)
	}

	byModel, err := s.usageStore.UsageByModel(ctx, since, 15)
	if err != nil {
		return v, err
	}
	v.ByModel = byModel

	byRole, err := s.usageStore.UsageByRole(ctx, since)
	if err != nil {
		return v, err
	}
	v.ByRole = byRole

	// Expensive turns — always from the primary period.
	turns, err := s.usageStore.ExpensiveTurns(ctx, since, 10)
	if err != nil {
		return v, err
	}
	for _, t := range turns {
		v.ExpensiveTurns = append(v.ExpensiveTurns, expensiveTurnView{
			Ts:       t.Ts.Format("2006-01-02 15:04"),
			ChatID:   t.ChatID,
			Role:     t.Role,
			ModelID:  t.ModelID,
			CostUSD:  t.CostUSD,
			Tokens:   t.Tokens,
			Question: truncText(firstLine(t.Question), 200),
			Answer:   truncText(firstLine(t.Answer), 200),
		})
	}

	// Daily chart — always from 30 days regardless of active period, so the
	// shape is comparable across period toggles.
	buckets, err := s.usageStore.UsageByDay(ctx, now.Add(-30*24*time.Hour))
	if err != nil {
		return v, err
	}
	v.DailyChart = buildDailyChart(buckets)

	return v, nil
}

// resolvePeriod converts "24h" / "7d" / "30d" (default "7d") into a time
// window + the label echoed in the UI.
func resolvePeriod(period string) (time.Time, string) {
	now := time.Now()
	switch period {
	case "24h":
		return now.Add(-24 * time.Hour), "24h"
	case "30d":
		return now.Add(-30 * 24 * time.Hour), "30d"
	default:
		return now.Add(-7 * 24 * time.Hour), "7d"
	}
}

// buildDailyChart turns a slice of UsageDayBucket into SVG coordinates. The
// chart is a fixed 600×160 box with two bars per day (calls + cost, grouped).
func buildDailyChart(buckets []llm.UsageDayBucket) dailyChart {
	c := dailyChart{Width: 720, Height: 160, Days: len(buckets)}
	if len(buckets) == 0 {
		return c
	}
	// Normalize — find max for each axis.
	for _, b := range buckets {
		if b.Calls > c.MaxCalls {
			c.MaxCalls = b.Calls
		}
		if b.CostUSD > c.MaxCost {
			c.MaxCost = b.CostUSD
		}
	}
	if c.MaxCalls == 0 {
		c.MaxCalls = 1
	}
	if c.MaxCost == 0 {
		c.MaxCost = 0.0001
	}
	// Layout: barWidth pair per day, 20px of padding total. For 30-day view we
	// want ~10px per day comfortably.
	sort.Slice(buckets, func(i, j int) bool { return buckets[i].Day.Before(buckets[j].Day) })
	dayWidth := (c.Width - 40) / max(len(buckets), 1)
	for i, b := range buckets {
		x := 20 + i*dayWidth
		callsH := int(float64(b.Calls) / float64(c.MaxCalls) * 140)
		costH := int(b.CostUSD / c.MaxCost * 140)
		c.Bars = append(c.Bars, dailyBar{
			X:        x,
			CallsY:   c.Height - 20 - callsH,
			CallsH:   callsH,
			CostY:    c.Height - 20 - costH,
			CostH:    costH,
			DayLabel: b.Day.Format("01-02"),
			Calls:    b.Calls,
			CostUSD:  b.CostUSD,
		})
	}
	return c
}

// firstLine returns the first non-empty line of s, trimmed.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return strings.TrimSpace(s)
}

// truncText caps a string at n runes, appending an ellipsis when truncated.
func truncText(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// priceFmt formats a USD cost with more precision when small.
func priceFmt(v float64) string {
	switch {
	case v == 0:
		return "—"
	case v < 0.01:
		return fmt.Sprintf("$%.4f", v)
	case v < 1:
		return fmt.Sprintf("$%.3f", v)
	default:
		return fmt.Sprintf("$%.2f", v)
	}
}

// intFmt adds thousand separators for large numbers.
func intFmt(n int) string {
	s := fmt.Sprintf("%d", n)
	if n < 1000 {
		return s
	}
	var b strings.Builder
	rem := len(s) % 3
	if rem > 0 {
		b.WriteString(s[:rem])
		if len(s) > rem {
			b.WriteString(",")
		}
	}
	for i := rem; i < len(s); i += 3 {
		b.WriteString(s[i : i+3])
		if i+3 < len(s) {
			b.WriteString(",")
		}
	}
	return b.String()
}
