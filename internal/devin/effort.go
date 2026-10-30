package devin

import "strings"

func NormalizeEffortLevel(raw string) (ThinkingLevel, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "minimal":
		return ThinkingMinimal, true
	case "low":
		return ThinkingLow, true
	case "medium":
		return ThinkingMedium, true
	case "high":
		return ThinkingHigh, true
	case "xhigh", "x-high", "extra-high":
		return ThinkingXHigh, true
	case "max":
		return ThinkingMax, true
	case "none":
		return ThinkingNone, true
	default:
		return "", false
	}
}

func splitModifiers(uid string) (string, string) {
	suffix := ""
	for {
		matched := false
		for _, modifier := range variantModifiers {
			if strings.HasSuffix(uid, modifier) {
				uid = strings.TrimSuffix(uid, modifier)
				suffix = modifier + suffix
				matched = true
				break
			}
		}
		if !matched {
			return uid, suffix
		}
	}
}

func levelSuffix(level ThinkingLevel) string {
	for _, candidate := range levelSuffixes {
		if candidate.level == level {
			return candidate.suffix
		}
	}
	return ""
}

func RewriteUpstream(upstream string, level ThinkingLevel) (string, bool) {
	core, mods := splitModifiers(upstream)
	for _, candidate := range levelSuffixes {
		if strings.HasSuffix(core, candidate.suffix) {
			if candidate.level == level {
				return upstream, false
			}
			return strings.TrimSuffix(core, candidate.suffix) + levelSuffix(level) + mods, true
		}
	}
	if strings.HasSuffix(core, "-thinking") {
		return strings.TrimSuffix(core, "-thinking") + levelSuffix(level) + mods, true
	}
	return upstream, false
}

func EffectiveUpstream(upstream string, effort *string, known map[string]bool) string {
	if effort == nil {
		return upstream
	}
	level, ok := NormalizeEffortLevel(*effort)
	if !ok {
		return upstream
	}
	candidate, changed := RewriteUpstream(upstream, level)
	if !changed {
		return upstream
	}
	if len(known) > 0 && !known[candidate] {
		return upstream
	}
	return candidate
}
