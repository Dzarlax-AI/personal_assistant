package adminapi

import (
	"fmt"
	"regexp"
	"sort"

	"telegram-agent/internal/llm"
)

// Role-driven recommendation engine. When the user clicks "Suggest" next to a
// routing role in the admin UI, the model browser applies the matching
// preset: a set of include/exclude filters + a Pareto frontier on
// (role-specific quality metric, price). Result: every model shown is a
// valid trade-off — no model in the list is strictly dominated (worse AND
// more expensive) by another.

var multilingualRegex = regexp.MustCompile(
	`^(` +
		`deepseek/(deepseek-chat|deepseek-r1|deepseek-v3)` +
		`|qwen/qwen3(\.[0-9]+)?(-|$)` +
		`|qwen/qwen-(plus|max|turbo)` +
		`|qwen/qwq` +
		`|z-ai/glm-4\.[5-9]` +
		`|moonshotai/kimi-` +
		`|google/gemini-(2\.5|3|3\.1)-(flash|pro)` +
		`|mistralai/mistral-(large|medium)` +
		`|x-ai/grok-[34]` +
		`)`,
)

var excludedVendorsRegex = regexp.MustCompile(`^(anthropic|openai)/`)

var specialisedCoderRegex = regexp.MustCompile(`-coder(-|$|:)`)

var specialisedVisionRegex = regexp.MustCompile(`-vl-`)

var unstableVariantRegex = regexp.MustCompile(
	`(^|[-/:])(preview|beta|exp|experimental|customtools)([-/:]|$)`,
)

// lightweightDefaultRegex catches models that are good L1/classifier
// candidates but too small or latency-oriented for the default workhorse role.
// Do not match every "flash" model: Gemini Flash-class models can still be a
// reasonable default, while "flash-lite"/"mini"/small-B variants usually are
// not.
var lightweightDefaultRegex = regexp.MustCompile(
	`(^|[-/:])(flash-lite|lite|mini|small|nano)([-/:]|$)|` +
		`(^|[-/:])([1-9]|1[0-4])b([-/:]|$)|` +
		`^qwen/qwen3(\.[0-9]+)?-flash([-/:]|$)`,
)

// thinkingRegex matches model ids that are actually frontier reasoners — the
// `reasoning: true` capability flag alone is not enough (8B models also set
// it just because the API accepts a reasoning parameter).
var thinkingRegex = regexp.MustCompile(
	`(-thinking|:thinking|/qwq|/deepseek-r[0-9]|-r1(-|$)|-reasoner|^x-ai/grok-(3-mini|4))`,
)

// paretoAxes returns (quality, price) for a model under a given role.
type paretoAxes func(m uiModel) (quality, price float64)

// rolePreset describes how to filter + rank models for a given routing role.
type rolePreset struct {
	Description string
	Filter      func(caps llm.Capabilities, modelID string, aa llm.AAModelInfo) bool
	Axes        paretoAxes
}

// imageShare is the assumed fraction of user messages that contain an image.
// Used to inflate a non-vision candidate's effective prompt price: those
// messages always route to the (more expensive) multimodal slot, so the
// candidate's real cost for agentic/default traffic is:
//
//	(1 - imageShare) * candidate.prompt + imageShare * multimodal_slot.prompt
const imageShare = 0.10

func roleQuality(m uiModel, role string) float64 {
	switch role {
	case "simple", "classifier":
		if m.TTFT > 0 {
			return inverseTTFT(m)
		}
		if m.SpeedTPS > 0 {
			return m.SpeedTPS / 100
		}
		return maxPositive(m.AgenticIndex, m.Score, m.CodingIndex)
	case "default", "complex":
		return maxPositive(m.AgenticIndex, m.CodingIndex, m.Score)
	case "multimodal", "compaction":
		return maxPositive(m.Score, m.CodingIndex)
	default:
		return maxPositive(m.AgenticIndex, m.Score, m.CodingIndex)
	}
}

func roleQualityLabel(m uiModel, role string) string {
	switch role {
	case "simple", "classifier":
		if m.TTFT > 0 {
			return fmt.Sprintf("AA TTFT %.2fs", m.TTFT)
		}
		if m.SpeedTPS > 0 {
			return fmt.Sprintf("AA speed %.0f t/s", m.SpeedTPS)
		}
	case "default", "complex":
		if m.AgenticIndex > 0 && m.AgenticIndex >= m.CodingIndex && m.AgenticIndex >= m.Score {
			return fmt.Sprintf("AA agentic %.0f", m.AgenticIndex)
		}
		if m.CodingIndex > 0 && m.CodingIndex >= m.Score {
			return fmt.Sprintf("AA coding %.0f", m.CodingIndex)
		}
	case "multimodal", "compaction":
		if m.Score > 0 && m.Score >= m.CodingIndex {
			return fmt.Sprintf("AA intelligence %.0f", m.Score)
		}
		if m.CodingIndex > 0 {
			return fmt.Sprintf("AA coding %.0f", m.CodingIndex)
		}
	}
	if m.AgenticIndex > 0 {
		return fmt.Sprintf("AA agentic %.0f", m.AgenticIndex)
	}
	if m.Score > 0 {
		return fmt.Sprintf("AA intelligence %.0f", m.Score)
	}
	if m.CodingIndex > 0 {
		return fmt.Sprintf("AA coding %.0f", m.CodingIndex)
	}
	return ""
}

func maxPositive(values ...float64) float64 {
	best := 0.0
	for _, v := range values {
		if v > best {
			best = v
		}
	}
	return best
}

// effectivePromptOf returns the cost-adjusted prompt price. If the model is
// non-vision AND applyPreset populated EffectivePrompt (by passing a
// visionFallbackPrompt in the relevant role), use that. Otherwise nominal.
func effectivePromptOf(m uiModel) float64 {
	if m.EffectivePrompt > 0 {
		return m.EffectivePrompt
	}
	return m.PromptPrice
}

func effectiveBlendedPriceOf(m uiModel) float64 {
	return orBlendedPrice(effectivePromptOf(m), m.CompletionPrice)
}

// usesVisionFallback returns true for roles where non-vision traffic routes
// image messages to the multimodal slot (so the role's candidates should be
// penalised for missing vision).
func usesVisionFallback(role string) bool {
	switch role {
	case "simple", "default", "complex":
		return true
	}
	return false
}

// inverseTTFT — classifier emits one digit; TTFT (time-to-first-token)
// dominates total latency. Returns 0 when no TTFT data (excluded by
// paretoFrontier's quality>0 guard).
func inverseTTFT(m uiModel) float64 {
	if m.TTFT <= 0 {
		return 0
	}
	return 1.0 / m.TTFT
}

var rolePresets = map[string]rolePreset{
	"simple": {
		Description: "tools + multilingual, ≤ $0.2/M prompt, ctx ≥ 32k. Pareto frontier on role quality (TTFT, throughput, then AA quality) versus blended cost.",
		Filter: func(c llm.Capabilities, id string, aa llm.AAModelInfo) bool {
			return multilingualRegex.MatchString(id) &&
				!excludedVendorsRegex.MatchString(id) &&
				!specialisedCoderRegex.MatchString(id) &&
				!specialisedVisionRegex.MatchString(id) &&
				!isUnstableVariant(id) &&
				!isFreeVariant(id) &&
				c.Tools &&
				c.ContextLength >= 32000 &&
				c.PromptPrice > 0 && c.PromptPrice <= 0.2
		},
		// Axes selected dynamically in applyPreset (see simpleAxes).
		Axes: nil,
	},

	"default": {
		Description: "workhorse tools + multilingual, excludes L1/classifier-sized models, ≤ $2/M prompt, ctx ≥ 32k. Pareto frontier on role quality (AA Agentic/Coding/Intelligence) versus blended cost.",
		Filter: func(c llm.Capabilities, id string, aa llm.AAModelInfo) bool {
			return multilingualRegex.MatchString(id) &&
				!excludedVendorsRegex.MatchString(id) &&
				!specialisedCoderRegex.MatchString(id) &&
				!specialisedVisionRegex.MatchString(id) &&
				!lightweightDefaultRegex.MatchString(id) &&
				!isUnstableVariant(id) &&
				!isFreeVariant(id) &&
				c.Tools &&
				c.ContextLength >= 32000 &&
				c.PromptPrice > 0 && c.PromptPrice <= 2.0
		},
		Axes: func(m uiModel) (float64, float64) { return roleQuality(m, "default"), effectiveBlendedPriceOf(m) },
	},

	"complex": {
		Description: "frontier reasoners (thinking/r1/qwq/grok) with tools + multilingual, ≤ $5/M prompt, ctx ≥ 64k. Pareto frontier on role quality (AA Agentic/Coding/Intelligence) versus blended cost. Claude via bridge is preferred when configured.",
		Filter: func(c llm.Capabilities, id string, aa llm.AAModelInfo) bool {
			return multilingualRegex.MatchString(id) &&
				!excludedVendorsRegex.MatchString(id) &&
				!specialisedCoderRegex.MatchString(id) &&
				!specialisedVisionRegex.MatchString(id) &&
				!isUnstableVariant(id) &&
				!isFreeVariant(id) &&
				thinkingRegex.MatchString(id) &&
				c.Tools && c.Reasoning &&
				c.ContextLength >= 64000 &&
				c.PromptPrice > 0 && c.PromptPrice <= 5.0
		},
		Axes: func(m uiModel) (float64, float64) { return roleQuality(m, "complex"), effectiveBlendedPriceOf(m) },
	},

	"multimodal": {
		Description: "vision + tools + multilingual, ≤ $2/M prompt, ctx ≥ 32k. Pareto frontier on role quality (AA Intelligence/Coding) versus blended cost.",
		Filter: func(c llm.Capabilities, id string, aa llm.AAModelInfo) bool {
			return multilingualRegex.MatchString(id) &&
				!excludedVendorsRegex.MatchString(id) &&
				!isUnstableVariant(id) &&
				!isFreeVariant(id) &&
				c.Vision && c.Tools &&
				c.ContextLength >= 32000 &&
				c.PromptPrice > 0 && c.PromptPrice <= 2.0
		},
		Axes: func(m uiModel) (float64, float64) { return roleQuality(m, "multimodal"), effectiveBlendedPriceOf(m) },
	},

	"compaction": {
		Description: "multilingual, ctx ≥ 64k (long history in, short summary out), completion ≤ $2/M. Pareto frontier on role quality (AA Intelligence/Coding) versus completion price.",
		Filter: func(c llm.Capabilities, id string, aa llm.AAModelInfo) bool {
			return multilingualRegex.MatchString(id) &&
				!excludedVendorsRegex.MatchString(id) &&
				!specialisedCoderRegex.MatchString(id) &&
				!specialisedVisionRegex.MatchString(id) &&
				!isUnstableVariant(id) &&
				!isFreeVariant(id) &&
				c.ContextLength >= 64000 &&
				c.CompletionPrice > 0 && c.CompletionPrice <= 2.0
		},
		Axes: func(m uiModel) (float64, float64) { return roleQuality(m, "compaction"), m.CompletionPrice },
	},

	"classifier": {
		Description: "≤ $0.1/M prompt, multilingual, verified paid path for automatic recommendations. Free variants are shown separately until checked. Pareto frontier on role quality (TTFT, throughput, then AA quality) versus prompt price. Local Ollama stays the primary recommendation.",
		Filter: func(c llm.Capabilities, id string, aa llm.AAModelInfo) bool {
			return multilingualRegex.MatchString(id) &&
				!excludedVendorsRegex.MatchString(id) &&
				!specialisedCoderRegex.MatchString(id) &&
				!specialisedVisionRegex.MatchString(id) &&
				!isUnstableVariant(id) &&
				!isFreeVariant(id) &&
				c.PromptPrice > 0 && c.PromptPrice <= 0.1
		},
		// Axes selected dynamically in applyPreset (see classifierAxes).
		Axes: nil,
	},

	// "fallback" has no preset — it should point at a DIRECT provider
	// (different vendor from the default) to survive an OpenRouter outage.
}

func isFreeVariant(modelID string) bool {
	return len(modelID) > 5 && modelID[len(modelID)-5:] == ":free"
}

func isUnstableVariant(modelID string) bool {
	return unstableVariantRegex.MatchString(modelID)
}

// classifierAxes ranks tiny prompt-only models by latency first, then speed or
// available AA quality when latency is absent.
func classifierAxes(candidates []uiModel) paretoAxes {
	return func(m uiModel) (float64, float64) { return roleQuality(m, "classifier"), m.PromptPrice }
}

// simpleAxes keeps L1 recommendations distinct from default: latency and
// throughput are stronger signals than benchmark quality for this role.
func simpleAxes(candidates []uiModel) paretoAxes {
	return func(m uiModel) (float64, float64) { return roleQuality(m, "simple"), effectiveBlendedPriceOf(m) }
}

func axesForPreset(role string, preset rolePreset, candidates []uiModel) paretoAxes {
	if preset.Axes != nil {
		return preset.Axes
	}
	switch role {
	case "simple":
		return simpleAxes(candidates)
	case "classifier":
		return classifierAxes(candidates)
	default:
		return nil
	}
}

// paretoFrontier keeps only non-dominated models. A model is also excluded
// if its quality is 0 — Pareto would otherwise keep untested models at the
// price floor just because no one beats them on both axes. Requiring quality
// > 0 means recommendations are always based on real AA data.
func paretoFrontier(models []uiModel, axes paretoAxes) []uiModel {
	out := make([]uiModel, 0, len(models))
	for i, m := range models {
		qi, pi := axes(m)
		if qi <= 0 {
			continue
		}
		dominated := false
		for j, other := range models {
			if i == j {
				continue
			}
			qj, pj := axes(other)
			if qj <= 0 {
				continue
			}
			strictlyBetter := (qj > qi && pj <= pi) || (qj >= qi && pj < pi)
			if strictlyBetter {
				dominated = true
				break
			}
		}
		if !dominated {
			out = append(out, m)
		}
	}
	return out
}

func appendNearFrontierAlternatives(frontier, candidates []uiModel, axes paretoAxes, role string, limit int) []uiModel {
	if len(candidates) == 0 || axes == nil || limit <= 0 {
		return frontier
	}
	seen := make(map[string]bool, len(frontier))
	topQuality := 0.0
	for _, m := range candidates {
		q, _ := axes(m)
		if q > topQuality {
			topQuality = q
		}
	}
	if topQuality <= 0 {
		return frontier
	}
	for _, m := range frontier {
		seen[m.ID] = true
	}
	alts := make([]uiModel, 0, len(candidates))
	for _, m := range candidates {
		if seen[m.ID] {
			continue
		}
		q, p := axes(m)
		if q <= 0 || p <= 0 || q < 0.50*topQuality {
			continue
		}
		m.Recommended = false
		m.Section = "interesting"
		if p > 0 {
			m.ValuePerDollar = q / p
		}
		annotateModelForRole(&m, role, "near_frontier")
		alts = append(alts, m)
	}
	sort.Slice(alts, func(i, j int) bool {
		qi, pi := axes(alts[i])
		qj, pj := axes(alts[j])
		if qi != qj {
			return qi > qj
		}
		if pi != pj {
			return pi < pj
		}
		return alts[i].ID < alts[j].ID
	})
	if len(alts) > limit {
		alts = alts[:limit]
	}
	out := append(frontier, alts...)
	return appendUntestedAlternatives(out, candidates, axes, role, 2)
}

func appendUntestedAlternatives(models, candidates []uiModel, axes paretoAxes, role string, limit int) []uiModel {
	if limit <= 0 {
		return models
	}
	seen := make(map[string]bool, len(models))
	for _, m := range models {
		seen[m.ID] = true
	}
	untested := make([]uiModel, 0, len(candidates))
	for _, m := range candidates {
		if seen[m.ID] {
			continue
		}
		q, _ := axes(m)
		if q > 0 {
			continue
		}
		m.Recommended = false
		m.Section = "untested"
		annotateModelForRole(&m, role, "untested")
		untested = append(untested, m)
	}
	sort.Slice(untested, func(i, j int) bool {
		if untested[i].ContextLength != untested[j].ContextLength {
			return untested[i].ContextLength > untested[j].ContextLength
		}
		if untested[i].PromptPrice != untested[j].PromptPrice {
			return untested[i].PromptPrice < untested[j].PromptPrice
		}
		return untested[i].ID < untested[j].ID
	})
	if len(untested) > limit {
		untested = untested[:limit]
	}
	return append(models, untested...)
}

// applyPreset returns the Pareto-optimal models for the role, sorted by
// quality descending (best first). If the role has no preset, returns nil.
// Each returned model has ValuePerDollar populated using the role's axes.
//
// visionFallbackPrompt is the current prompt price of the multimodal slot's
// model. Non-vision candidates for roles in usesVisionFallback have their
// EffectivePrompt set to a blended cost that accounts for image messages
// bouncing to multimodal. Pass 0 to disable this adjustment.
func applyPreset(all map[string]llm.Capabilities, aaModels map[string]llm.AAModelInfo, role string, visionFallbackPrompt float64) []uiModel {
	preset, ok := rolePresets[role]
	if !ok {
		return nil
	}
	applyEffective := usesVisionFallback(role) && visionFallbackPrompt > 0
	candidates := make([]uiModel, 0, len(all))
	for id, c := range all {
		var aa llm.AAModelInfo
		if aaModels != nil {
			if info := llm.LookupAAInfo(id, aaModels); info != nil {
				aa = *info
			}
		}
		if !preset.Filter(c, id, aa) {
			continue
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
		enrichFromAA(&m, aa)
		if applyEffective && !c.Vision {
			m.EffectivePrompt = (1-imageShare)*c.PromptPrice + imageShare*visionFallbackPrompt
		}
		candidates = append(candidates, m)
	}
	axes := axesForPreset(role, preset, candidates)
	if axes == nil {
		return nil
	}
	frontier := paretoFrontier(candidates, axes)
	sort.Slice(frontier, func(i, j int) bool {
		qi, pi := axes(frontier[i])
		qj, pj := axes(frontier[j])
		if qi != qj {
			return qi > qj
		}
		if pi != pj {
			return pi < pj
		}
		return frontier[i].ID < frontier[j].ID
	})
	for i := range frontier {
		q, p := axes(frontier[i])
		if p > 0 && q > 0 {
			frontier[i].ValuePerDollar = q / p
		}
		frontier[i].Recommended = true
		frontier[i].Section = "recommended"
		annotateModelForRole(&frontier[i], role, "preset")
	}
	return appendNearFrontierAlternatives(frontier, candidates, axes, role, 4)
}

func annotateModelForRole(m *uiModel, role, source string) {
	if source == "" {
		source = "catalog"
	}
	m.Source = source
	if m.Recommended {
		m.Policy = "recommended"
	} else if m.Free {
		m.Policy = "free_unverified"
	} else if m.Policy == "" {
		m.Policy = "candidate"
	}

	reasons := make([]string, 0, 6)
	warnings := make([]string, 0, 4)
	if m.Tools {
		reasons = append(reasons, "tool calling")
	} else if role != "compaction" {
		warnings = append(warnings, "no tool calling")
	}
	if m.Vision {
		reasons = append(reasons, "vision")
	} else if usesVisionFallback(role) && m.EffectivePrompt > 0 {
		warnings = append(warnings, "image traffic uses multimodal fallback")
	}
	if m.Reasoning {
		reasons = append(reasons, "reasoning")
	}
	if m.ContextLength > 0 {
		reasons = append(reasons, fmt.Sprintf("ctx %s", shortContextLabel(m.ContextLength)))
	}
	if label := roleQualityLabel(*m, role); label != "" {
		reasons = append(reasons, label)
	} else {
		warnings = append(warnings, "no role benchmark data")
	}
	if m.ValuePerDollar > 0 {
		reasons = append(reasons, fmt.Sprintf("value %.0f/$", m.ValuePerDollar))
	}
	if m.Free {
		warnings = append(warnings, "free model: validate availability before routing")
	}
	m.Reasons = uniqueStrings(reasons)
	m.Warnings = uniqueStrings(warnings)
}

func shortContextLabel(n int) string {
	if n >= 1000000 {
		return fmt.Sprintf("%.1fM", float64(n)/1000000)
	}
	if n >= 1000 {
		return fmt.Sprintf("%dk", n/1000)
	}
	return fmt.Sprintf("%d", n)
}

func uniqueStrings(in []string) []string {
	out := in[:0]
	seen := make(map[string]bool, len(in))
	for _, v := range in {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

// valueLeader returns the frontier model with the best quality/price ratio,
// subject to role-quality ≥ qualityFloor × topQuality. Returns nil when the
// value leader is already the top-quality model (no meaningful trade-off to
// surface) or the frontier is trivially small.
func valueLeader(frontier []uiModel, axes paretoAxes, qualityFloor float64) *uiModel {
	if len(frontier) < 2 {
		return nil
	}
	topQuality, _ := axes(frontier[0])
	floor := qualityFloor * topQuality
	bestIdx := -1
	bestVal := 0.0
	for i := range frontier {
		q, p := axes(frontier[i])
		if q < floor || p <= 0 {
			continue
		}
		if v := q / p; v > bestVal {
			bestVal = v
			bestIdx = i
		}
	}
	if bestIdx <= 0 { // not found, or it's frontier[0] itself
		return nil
	}
	return &frontier[bestIdx]
}

// presetRoles returns the list of roles that have a preset, in display order.
func presetRoles() map[string]bool {
	out := make(map[string]bool, len(rolePresets))
	for role := range rolePresets {
		out[role] = true
	}
	return out
}
