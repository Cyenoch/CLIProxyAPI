// Package devin maps canonical thinking configuration to Devin model selection.
package devin

import (
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/tidwall/sjson"
)

type Applier struct{}

func init() { thinking.RegisterProvider("devin", &Applier{}) }

// Apply preserves the validated effort, including explicit none, for the model UID resolver.
func (*Applier) Apply(body []byte, config thinking.ThinkingConfig, _ *registry.ModelInfo) ([]byte, error) {
	body = thinking.StripThinkingConfig(body, "devin")
	level := string(config.Level)
	switch config.Mode {
	case thinking.ModeBudget:
		level, _ = thinking.ConvertBudgetToLevel(config.Budget)
	case thinking.ModeNone:
		if level == "" {
			level = "none"
		}
	case thinking.ModeAuto:
		// An automatic request selects the catalog model's default variant.
		return body, nil
	}
	if level == "" {
		return body, nil
	}
	return sjson.SetBytes(body, "generation_config.thinking_level", level)
}
