package tghelper

import (
	"context"
	"errors"
	"math/rand"
	"net"
	"net/url"
	"time"
)

// RetryOptions описывает параметры ретраев.
type RetryOptions struct {
	Attempts  int                                               // количество попыток
	BaseDelay time.Duration                                     // базовая задержка перед экспонентой
	MaxDelay  time.Duration                                     // максимальная задержка (0 — без ограничений)
	Jitter    bool                                              // включить джиттер
	OnRetry   func(attempt int, err error, delay time.Duration) // коллбек на ретрай
}

// DefaultRetryOptions возвращает настройки по умолчанию: 5 попыток, база 2с, джиттер включен.
func DefaultRetryOptions() RetryOptions {
	return RetryOptions{
		Attempts:  5,
		BaseDelay: 2 * time.Second,
		MaxDelay:  0,
		Jitter:    true,
	}
}

// RetryableError помечает ошибку как подходящую для повторной попытки.
type RetryableError struct{ Err error }

func (e RetryableError) Error() string { return e.Err.Error() }
func (e RetryableError) Unwrap() error { return e.Err }

// MarkRetryable оборачивает ошибку в RetryableError.
func MarkRetryable(err error) error {
	if err == nil {
		return nil
	}
	return RetryableError{Err: err}
}

// IsRetryable проверяет, была ли ошибка помечена как ретраимая.
func IsRetryable(err error) bool {
	var r RetryableError
	return errors.As(err, &r)
}

// IsNetError возвращает true для сетевых ошибок/таймаутов.
func IsNetError(err error) bool {
	if err == nil {
		return false
	}

	var netErr net.Error
	if errors.As(err, &netErr) && (netErr.Timeout() || netErr.Temporary()) {
		return true
	}

	var urlErr *url.Error
	return errors.As(err, &urlErr)
}

func (o RetryOptions) withDefaults() RetryOptions {
	d := DefaultRetryOptions()

	if o.Attempts <= 0 {
		o.Attempts = d.Attempts
	}
	if o.BaseDelay <= 0 {
		o.BaseDelay = d.BaseDelay
	}
	if o.MaxDelay < 0 {
		o.MaxDelay = 0
	}
	// Jitter и OnRetry остаются как указано (bool не имеет "zero" понятия настройки)
	return o
}

func backoffDelay(base time.Duration, attempt int, jitter bool, max time.Duration) time.Duration {
	if attempt < 1 {
		attempt = 1
	}

	delay := base * time.Duration(1<<(attempt-1))
	if max > 0 && delay > max {
		delay = max
	}

	if !jitter {
		return delay
	}

	// Full jitter: случайная задержка в диапазоне [0, delay]
	if delay <= 0 {
		return base
	}
	return time.Duration(rand.Int63n(delay.Nanoseconds() + 1)) //nolint:gosec
}

func defaultShouldRetry(err error, custom func(error) bool) bool {
	if custom != nil {
		return custom(err)
	}
	if IsRetryable(err) {
		return true
	}
	return IsNetError(err)
}

// DoRetry выполняет функцию с ретраями. Возвращает первый успешный результат или последнюю ошибку.
func DoRetry[T any](ctx context.Context, fn func(context.Context) (T, error), shouldRetry func(error) bool, opts RetryOptions) (T, error) {
	opts = opts.withDefaults()
	var zero T
	var lastErr error

	for attempt := 1; attempt <= opts.Attempts; attempt++ {
		if ctx != nil {
			select {
			case <-ctx.Done():
				return zero, ctx.Err()
			default:
			}
		}

		res, err := fn(ctx)
		if err == nil {
			return res, nil
		}

		lastErr = err

		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return zero, err
		}

		if attempt == opts.Attempts || !defaultShouldRetry(err, shouldRetry) {
			return zero, err
		}

		delay := backoffDelay(opts.BaseDelay, attempt, opts.Jitter, opts.MaxDelay)
		if opts.OnRetry != nil {
			opts.OnRetry(attempt, err, delay)
		}

		if ctx != nil {
			select {
			case <-ctx.Done():
				return zero, ctx.Err()
			case <-time.After(delay):
			}
		} else {
			time.Sleep(delay)
		}
	}

	return zero, lastErr
}
