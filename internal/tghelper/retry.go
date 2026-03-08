package tghelper

import (
	"context"
	"log"
	"strings"
	"time"
)

func RetryTelegram[T any](ctx context.Context, action string, fn func() (T, error)) (T, error) {
	opts := DefaultRetryOptions()
	opts.OnRetry = func(attempt int, err error, delay time.Duration) {
		log.Printf("[telegram] %s retry %d: %v (sleep %s)", action, attempt, err, delay)
	}

	return DoRetry(ctx, func(context.Context) (T, error) {
		return fn()
	}, ShouldRetryTelegram, opts)
}

func ShouldRetryTelegram(err error) bool {
	if err == nil {
		return false
	}

	msg := strings.ToUpper(err.Error())

	if strings.Contains(msg, "FLOOD_WAIT") {
		return false
	}

	if IsRetryable(err) || IsNetError(err) {
		return true
	}

	msgLower := strings.ToLower(err.Error())
	if strings.Contains(msgLower, "timeout") ||
		strings.Contains(msgLower, "temporarily unavailable") ||
		strings.Contains(msgLower, "try again later") {
		return true
	}

	return false
}
