package helps

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
)

// knownDevinSuffixes lists recognized model uid suffixes.
var knownDevinSuffixes = []string{
	"-none",
	"-minimal",
	"-low",
	"-medium",
	"-high",
	"-xhigh",
	"-max",
	"-fast",
	"-priority",
	"-low-priority",
	"-medium-priority",
	"-high-priority",
	"-xhigh-priority",
	"-max-priority",
}

// Special private Devin upstream aliases that cannot be dynamically inferred.
var specialDevinAliases = map[string]string{
	"claude-haiku-4-5": "MODEL_PRIVATE_11",
	"gpt-4-1":          "MODEL_CHAT_GPT_4_1_2025_04_14",
}

// HasDevinEffortSuffix reports whether the model name already ends with a known Devin effort suffix.
func HasDevinEffortSuffix(model string) bool {
	lower := strings.ToLower(strings.TrimSpace(model))
	for _, s := range knownDevinSuffixes {
		if strings.HasSuffix(lower, s) {
			return true
		}
	}
	return false
}

// DevinCatalogModelUID resolves the public model name to the catalog entry used on the wire.
func DevinCatalogModelUID(rawModel string) string {
	model := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(rawModel)), "devin/")
	model = strings.ReplaceAll(thinking.ParseSuffix(model).ModelName, ".", "-")
	if model == "gemini-3-flash" {
		return "gemini-3-8-flash"
	}
	if registry.LookupDevinModel(model) != nil {
		return model
	}
	for _, suffix := range knownDevinSuffixes {
		if base, ok := strings.CutSuffix(model, suffix); ok && registry.LookupDevinModel(base) != nil {
			return base
		}
	}
	return model
}

// IsDevinModelRouter reports whether the requested model is a server-side router
// (e.g. "adaptive"). Routers are not valid chat_model_uid values: they must be
// resolved through AssignModel before GetChatMessage.
func IsDevinModelRouter(rawModel string) bool {
	info := registry.LookupDevinModel(DevinCatalogModelUID(rawModel))
	return info != nil && info.IsModelRouter
}

// ResolveDevinChatModelUID resolves a model identifier into a valid upstream Devin chat_model_uid.
// Effort must already be normalized and validated by the canonical thinking pipeline.
func ResolveDevinChatModelUID(rawModel string, effort string) string {
	model := strings.TrimSpace(rawModel)
	if model == "" {
		return "swe-2-high"
	}

	// 1. Strip devin/ prefix if present (case-insensitive)
	cleanModel := model
	if strings.HasPrefix(strings.ToLower(cleanModel), "devin/") {
		cleanModel = cleanModel[6:]
	}

	// 2. If already ends with an exact Devin effort suffix, use directly
	if HasDevinEffortSuffix(cleanModel) {
		return cleanModel
	}

	baseModel := cleanModel
	lowerBase := strings.ToLower(baseModel)
	canonicalBase := DevinCatalogModelUID(baseModel)

	// 5. Check special private upstream aliases
	if alias, exists := specialDevinAliases[canonicalBase]; exists {
		return alias
	}
	if canonicalBase == "claude-sonnet-4-5" || strings.Contains(canonicalBase, "sonnet-4-5") {
		if effort != "" && effort != "none" {
			return "MODEL_PRIVATE_3"
		}
		return "MODEL_PRIVATE_2"
	}

	// 6. Look up dynamic model metadata from the Devin catalog (devin_models.json)
	modelInfo := registry.LookupDevinModel(canonicalBase)
	if modelInfo == nil && canonicalBase != lowerBase {
		modelInfo = registry.LookupDevinModel(lowerBase)
	}

	var allowedLevels []string
	if modelInfo != nil && modelInfo.Thinking != nil && len(modelInfo.Thinking.Levels) > 0 {
		allowedLevels = modelInfo.Thinking.Levels
	}

	// 7. Special base models that default to bare name unless specific variant requested
	switch canonicalBase {
	case "swe-1-7":
		if effort == "medium" {
			return "swe-1-7-medium"
		}
		return "swe-1-7"
	case "swe-1-6":
		if effort == "fast" {
			return "swe-1-6-fast"
		}
		return "swe-1-6"
	case "glm-5-2":
		if effort == "none" {
			return "glm-5-2-none"
		}
		if effort == "max" {
			return "glm-5-2-max"
		}
		return "glm-5-2"
	}

	// 8. If model has no thinking levels defined in catalog, treat as bare model
	if len(allowedLevels) == 0 {
		return canonicalBase
	}

	// Select a default variant only when the request leaves effort unspecified.
	defaultEffort := selectDefaultDevinEffort(canonicalBase, allowedLevels)
	if effort == "" {
		effort = defaultEffort
	}
	return canonicalBase + "-" + effort
}

func selectDefaultDevinEffort(baseModel string, levels []string) string {
	if strings.Contains(baseModel, "swe-2") {
		return "high"
	}
	hasNone := false
	hasLow := false
	hasMedium := false
	hasHigh := false
	for _, l := range levels {
		switch l {
		case "none":
			hasNone = true
		case "low":
			hasLow = true
		case "medium":
			hasMedium = true
		case "high":
			hasHigh = true
		}
	}
	// For OpenAI GPT-5.x families in Devin CLI (which support "none" and "low"), default to low
	if hasNone && hasLow && strings.HasPrefix(baseModel, "gpt-5") {
		return "low"
	}
	// For models with high thinking (e.g. deepseek, gemini, grok, glm, kimi, nemotron), prefer high
	if hasHigh && (strings.Contains(baseModel, "gemini") ||
		strings.Contains(baseModel, "grok") ||
		strings.Contains(baseModel, "glm") ||
		strings.Contains(baseModel, "deepseek") ||
		strings.Contains(baseModel, "kimi") ||
		strings.Contains(baseModel, "nemotron")) {
		return "high"
	}
	if hasMedium {
		return "medium"
	}
	if hasHigh {
		return "high"
	}
	if hasLow {
		return "low"
	}
	return levels[0]
}
