package adminapi

import "strings"

type modelDisplay struct {
	StatusLabel      string
	PolicyLabel      string
	PrimaryReason    string
	SecondaryReasons []string
}

func decorateModelDisplay(models []uiModel, role string) {
	for i := range models {
		applyModelDisplay(&models[i], role, false)
	}
}

func applyModelDisplay(m *uiModel, role string, current bool) {
	d := modelDisplayFor(*m, role, current)
	m.StatusLabel = d.StatusLabel
	m.PolicyLabel = d.PolicyLabel
	m.PrimaryReason = d.PrimaryReason
	m.SecondaryReasons = d.SecondaryReasons
}

func modelDisplayFor(m uiModel, role string, current bool) modelDisplay {
	status := modelStatusLabel(m, current)
	policy := modelPolicyLabel(m, status)
	primary := modelPrimaryReason(m, role, status)
	secondary := modelSecondaryReasons(m.Reasons, primary)
	return modelDisplay{
		StatusLabel:      status,
		PolicyLabel:      policy,
		PrimaryReason:    primary,
		SecondaryReasons: secondary,
	}
}

func modelStatusLabel(m uiModel, current bool) string {
	if current {
		return "Current"
	}
	switch {
	case m.Policy == "manual_deny" || m.Policy == "free_blocked":
		return "Blocked"
	case m.Section == "recommended" || m.Recommended:
		return "Recommended"
	case m.Section == "untested" || m.Source == "untested" || m.Policy == "free_unverified":
		return "Untested"
	case m.Section == "interesting" || m.Source == "near_frontier" || m.Source == "manual" || m.Policy == "manual_allow":
		return "Interesting"
	}
	return "Candidate"
}

func modelPolicyLabel(m uiModel, status string) string {
	switch m.Policy {
	case "", "candidate", "recommended":
		return ""
	case "manual_allow":
		return "Manual allow"
	case "manual_deny":
		return "Manual deny"
	case "free_unverified":
		return "Free unverified"
	case "free_verified":
		return "Free verified"
	case "free_degraded":
		return "Free degraded"
	case "free_blocked":
		return "Free blocked"
	default:
		label := strings.ReplaceAll(m.Policy, "_", " ")
		return strings.ToUpper(label[:1]) + label[1:]
	}
}

func modelPrimaryReason(m uiModel, role, status string) string {
	switch {
	case m.Recommended:
		if role != "" {
			return "Pareto frontier for " + role
		}
		return "Pareto frontier"
	case m.Policy == "manual_allow":
		return firstNonEmpty("Manual allow", m.OverrideNote)
	case m.Policy == "manual_deny":
		return firstNonEmpty("Manual deny", m.OverrideNote)
	case m.Policy == "free_blocked":
		return "Free endpoint is blocked"
	case m.Policy == "free_degraded":
		return "Free endpoint degraded"
	case m.Policy == "free_verified":
		return "Free endpoint verified"
	case m.Policy == "free_unverified":
		return "Free endpoint needs a check"
	case m.Source == "near_frontier":
		if role != "" {
			return "Near frontier for " + role
		}
		return "Near recommendation frontier"
	case status == "Untested":
		return "Compatible but missing role evidence"
	}
	if len(m.Reasons) > 0 {
		return m.Reasons[0]
	}
	return ""
}

func modelSecondaryReasons(reasons []string, primary string) []string {
	out := make([]string, 0, len(reasons))
	for _, reason := range uniqueStrings(reasons) {
		if reason == "" || reason == primary {
			continue
		}
		out = append(out, reason)
	}
	return out
}

func firstNonEmpty(fallback, value string) string {
	if strings.TrimSpace(value) != "" {
		return fallback + ": " + strings.TrimSpace(value)
	}
	return fallback
}
