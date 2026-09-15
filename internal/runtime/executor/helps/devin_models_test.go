package helps

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestResolveDevinChatModelUID(t *testing.T) {
	for _, tc := range []struct{ model, effort, want string }{
		{"devin/swe-2", "", "swe-2-high"},
		{"devin/gpt-6-astra", "xhigh", "gpt-6-astra-xhigh"},
		{"devin/glm-5-2", "none", "glm-5-2-none"},
		{"devin/glm-5-2", "max", "glm-5-2-max"},
		{"devin/glm-5-2", "", "glm-5-2"},
		{"devin/claude-haiku-4-5", "", "MODEL_PRIVATE_11"},
		{"devin/gpt-4-1", "", "MODEL_CHAT_GPT_4_1_2025_04_14"},
		{"swe-2-high", "", "swe-2-high"},
		{"claude-fable-5-1-max", "", "claude-fable-5-1-max"},
		{"swe-2-high", "", "swe-2-high"},
		{"swe-2", "", "swe-2-high"},
		{"swe-2", "max", "swe-2-max"},
		{"claude-fable-5-1", "xhigh", "claude-fable-5-1-xhigh"},
		{"devin/glm-5-3-flash", "", "glm-5-3-flash-high"},
		{"devin/gpt-5-6-sol", "none", "gpt-5-6-sol-none"},
		{"devin/swe-2", "", "swe-2-high"},
		{"devin/claude-fable-5-1", "", "claude-fable-5-1-medium"},
		{"devin/swe-2-high", "", "swe-2-high"},
		{"devin/gemini-3-8-flash", "", "gemini-3-8-flash-high"},
		{"devin/grok-4-6", "", "grok-4-6-high"},
		{"devin/deepseek-v4-flash", "", "deepseek-v4-flash-high"},
		{"devin/swe-1-7", "", "swe-1-7"},
		{"devin/claude-haiku-4-5", "", "MODEL_PRIVATE_11"},
		{"devin/claude-sonnet-4-5", "", "MODEL_PRIVATE_2"},
		{"devin/gpt-4-1", "", "MODEL_CHAT_GPT_4_1_2025_04_14"},
		{"devin/gemini-3-flash", "", "gemini-3-8-flash-high"},
		{"devin/gpt-5-6-luna", "", "gpt-5-6-luna-low"},
		{"devin/gpt-5-6-sol", "", "gpt-5-6-sol-low"},
		{"devin/gpt-5-6-terra", "", "gpt-5-6-terra-low"},
		{"devin/gpt-5-5", "", "gpt-5-5-low"},
		{"devin/gpt-5-4", "", "gpt-5-4-low"},
		{"devin/gpt-5-4-mini", "", "gpt-5-4-mini-medium"},
		{"devin/gpt-5-3-codex", "", "gpt-5-3-codex-medium"},
		{"devin/claude-opus-5", "", "claude-opus-5-medium"},
		{"devin/claude-opus-4-8", "", "claude-opus-4-8-medium"},
		{"devin/claude-opus-4-7", "", "claude-opus-4-7-medium"},
		{"devin/claude-sonnet-5", "", "claude-sonnet-5-medium"},
		{"devin/gemini-3-7-flash", "", "gemini-3-7-flash-high"},
		{"devin/gemini-3-6-flash", "", "gemini-3-6-flash-high"},
		{"devin/gemini-3-5-flash", "", "gemini-3-5-flash-high"},
		{"devin/deepseek-v4-pro", "", "deepseek-v4-pro-high"},
		{"devin/grok-4-5", "", "grok-4-5-high"},
		{"devin/kimi-k3", "", "kimi-k3-high"},
		{"devin/nemotron-3-ultra", "", "nemotron-3-ultra-high"},
		{"devin/swe-1-6", "", "swe-1-6"},
		{"devin/kimi-k2-6", "", "kimi-k2-6"},
		{"devin/kimi-k2-7", "", "kimi-k2-7"},
		{"devin/claude-opus-4-6", "", "claude-opus-4-6"},
		{"devin/claude-sonnet-4-6", "", "claude-sonnet-4-6"},
	} {
		if got := ResolveDevinChatModelUID(tc.model, tc.effort); got != tc.want {
			t.Errorf("%s (%s) = %q, want %q", tc.model, tc.effort, got, tc.want)
		}
	}
}

func TestResolveDevinChatModelUID_AllCatalogModels(t *testing.T) {
	models := registry.GetDevinModels()
	if len(models) == 0 {
		t.Fatal("GetDevinModels() returned empty list")
	}

	for _, m := range models {
		baseID := strings.TrimPrefix(m.ID, "devin/")
		testEfforts := []string{""}
		if m.Thinking != nil {
			effective := registry.LookupDevinModel(DevinCatalogModelUID(m.ID))
			if effective != nil && effective.Thinking != nil {
				testEfforts = append(testEfforts, effective.Thinking.Levels...)
			}
		}
		for _, eff := range testEfforts {
			resolved := ResolveDevinChatModelUID("devin/"+baseID, eff)
			if resolved == "" {
				t.Errorf("model %q with effort %q resolved to empty string", baseID, eff)
			}
			// If model defines thinking levels, resolved UID must have an effort suffix
			if m.Thinking != nil && len(m.Thinking.Levels) > 0 {
				if !HasDevinEffortSuffix(resolved) && baseID != "swe-1-7" && baseID != "glm-5-2" {
					t.Errorf("thinking model %q with effort %q resolved to bare UID %q without effort suffix", baseID, eff, resolved)
				}
			}
		}
	}
}
