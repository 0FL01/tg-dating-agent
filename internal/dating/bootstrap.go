// Package dating provides a standalone bootstrap helper for the dating skill.
// This file bridges the standalone config and LLM client wiring for external entrypoints.
package dating

import (
	"context"
	"log"

	"github.com/0FL01/tg-dating-agent/internal/llm"
	"github.com/0FL01/tg-dating-agent/internal/standalone"
	"github.com/0FL01/tg-dating-agent/internal/storage"
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
	handler := NewHandler(cfg, client, tgClient)

	var r2Store *storage.R2ObjectStore
	if cfg != nil && cfg.DatingR2Enabled {
		var err error
		r2Store, err = storage.NewR2ObjectStore(storage.R2Config{
			Bucket:          cfg.DatingR2Bucket,
			Endpoint:        cfg.DatingR2Endpoint,
			Region:          cfg.DatingR2Region,
			AccessKeyID:     cfg.DatingR2AccessKeyID,
			SecretAccessKey: cfg.DatingR2SecretAccessKey,
			InstanceName:    cfg.DatingInstanceName,
		})
		if err != nil {
			log.Printf("[dating] Profile dedupe storage disabled: %v", err)
		} else {
			handler.profileDedupe = NewProfileDedupeStore(r2Store, cfg.DatingProfileDedupTTL)
			handler.replyAudit = NewCompositeReplyAuditAppender(handler.replyAudit, NewReplyAuditR2Appender(r2Store))
		}
	}

	webhookClient, err := NewReciprocalLikeFinalWebhookClient(cfg)
	if err != nil {
		log.Printf("[dating] Reciprocal-like webhook disabled: %v", err)
		return handler
	}

	if webhookClient != nil {
		handler.deliverReciprocalLikeFinalFn = func(ctx context.Context, payload ReciprocalLikeFinalPayload, photos []ReciprocalLikePhoto) error {
			return webhookClient.DeliverReciprocalLikeFinal(ctx, payload, photos)
		}
	}

	return handler
}
