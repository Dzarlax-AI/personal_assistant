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

// dailyChart describes two stacked SVG charts (calls on top, cost below)
// that share the same X-axis layout so day columns line up exactly. Each
// chart carries its own Y-axis ticks with labels. Pre-computed here so the
// template stays math-free.
type dailyChart struct {
	Width      int
	Height     int // per-chart height (each chart renders separately)
	PlotTop    int
	PlotBot    int
	PlotLeft   int // x-coordinate of the Y-axis line
	PlotRight  int
	Days       int
	// Calls series (bar chart, top)
	CallsBars  []chartBar
	CallsYAxis []yTick
	MaxCalls   int
	// Cost series (bar chart, bottom)
	CostBars  []chartBar
	CostYAxis []yTick
	MaxCost   float64
	// Shared X-axis — applied to the cost chart only (bottom of the stack)
	XAxisTags []xAxisTag
}

type chartBar struct {
	X        int
	W        int
	Y        int
	H        int
	DayLabel string
	// Tooltip content — both metrics shown on hover so users don't need to
	// cross-reference.
	Calls   int
	CostUSD float64
}

type yTick struct {
	Y     int
	Label string
}

type xAxisTag struct {
	X     int
	Label string
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

// buildDailyChart produces two synchronized SVG chart descriptions (calls
// on top, cost on bottom) sharing padLeft/padRight + slot widths so day
// columns line up vertically. Size: 520×100 per chart, Y-axis on the left
// with up to 3 tick labels (0 / mid / max).
func buildDailyChart(buckets []llm.UsageDayBucket) dailyChart {
	const (
		width     = 520
		height    = 100
		padLeft   = 48 // room for Y-axis labels
		padRight  = 8
		padTop    = 8
		padBottom = 20
	)
	c := dailyChart{
		Width:     width,
		Height:    height,
		PlotTop:   padTop,
		PlotBot:   height - padBottom,
		PlotLeft:  padLeft,
		PlotRight: width - padRight,
		Days:      len(buckets),
	}
	if len(buckets) == 0 {
		return c
	}
	sort.Slice(buckets, func(i, j int) bool { return buckets[i].Day.Before(buckets[j].Day) })

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

	plotW := c.PlotRight - c.PlotLeft
	plotH := c.PlotBot - c.PlotTop
	slot := float64(plotW) / float64(len(buckets))
	barW := int(slot * 0.65)
	if barW < 2 {
		barW = 2
	}
	if barW > 28 {
		barW = 28
	}

	for i, b := range buckets {
		slotCenter := c.PlotLeft + int((float64(i)+0.5)*slot)
		barX := slotCenter - barW/2
		dayLabel := b.Day.Format("01-02")

		// Calls bar
		callsH := int(float64(b.Calls) / float64(c.MaxCalls) * float64(plotH))
		if callsH < 0 {
			callsH = 0
		}
		c.CallsBars = append(c.CallsBars, chartBar{
			X: barX, W: barW,
			Y: c.PlotBot - callsH, H: callsH,
			DayLabel: dayLabel, Calls: b.Calls, CostUSD: b.CostUSD,
		})

		// Cost bar
		costH := int(b.CostUSD / c.MaxCost * float64(plotH))
		if costH < 0 {
			costH = 0
		}
		c.CostBars = append(c.CostBars, chartBar{
			X: barX, W: barW,
			Y: c.PlotBot - costH, H: costH,
			DayLabel: dayLabel, Calls: b.Calls, CostUSD: b.CostUSD,
		})
	}

	// Y-axis ticks: 0, max/2, max — 3 lines only, keeps chart uncluttered
	// while giving the reader concrete numbers to reference.
	c.CallsYAxis = []yTick{
		{Y: c.PlotBot, Label: "0"},
		{Y: c.PlotTop + (c.PlotBot-c.PlotTop)/2, Label: intFmt(c.MaxCalls / 2)},
		{Y: c.PlotTop, Label: intFmt(c.MaxCalls)},
	}
	c.CostYAxis = []yTick{
		{Y: c.PlotBot, Label: "$0"},
		{Y: c.PlotTop + (c.PlotBot-c.PlotTop)/2, Label: priceFmt(c.MaxCost / 2)},
		{Y: c.PlotTop, Label: priceFmt(c.MaxCost)},
	}

	// X-axis — sparse labels at first / middle / last day, applied to the
	// bottom chart in the template.
	addTag := func(i int) {
		slotCenter := c.PlotLeft + int((float64(i)+0.5)*slot)
		c.XAxisTags = append(c.XAxisTags, xAxisTag{X: slotCenter, Label: buckets[i].Day.Format("01-02")})
	}
	addTag(0)
	if len(buckets) >= 3 {
		addTag(len(buckets) / 2)
	}
	if len(buckets) > 1 {
		addTag(len(buckets) - 1)
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
