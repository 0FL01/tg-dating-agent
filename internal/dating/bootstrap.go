// Package dating provides a standalone bootstrap helper for the dating skill.
// This file bridges the standalone config and LLM client wiring for external entrypoints.
package dating

import (
	"github.com/0FL01/tg-dating-agent/internal/llm"
	"github.com/0FL01/tg-dating-agent/internal/standalone"
	"github.com/amarnathcjd/gogram/telegram"
)

// NewStandaloneHandler creates a fully initialized dating Handler using the
// provided standalone configuration and Telegram client. It handles LLM client
// initialization internally and returns a handler ready to process messages.
//
// Usage:
//
//	cfg, err := standalone.Load()
//	if err != nil { ... }
//	// ... create telegram client ...
//	handler := dating.NewStandaloneHandler(cfg, tgClient)
func NewStandaloneHandler(cfg *standalone.Config, tgClient *telegram.Client) *Handler {
	client := llm.NewClient(cfg.OpenRouterAPIKey)
	return NewHandler(cfg, client, tgClient)
}
