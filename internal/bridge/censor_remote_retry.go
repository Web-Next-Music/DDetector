package bridge

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

type censorRemoteTransient struct {
	status     int
	retryAfter time.Duration
}

func (e *censorRemoteTransient) Error() string {
	return fmt.Sprintf("censor API temporarily unavailable (HTTP %d)", e.status)
}
func (e *censorRemoteTransient) Unwrap() error { return ErrCensorUnavailable }

// Only repeat reads: POST /search is a read by contract. Publication, rollback
// and POST /state are never automatically retried, even with a key; mutation
// reconciliation remains an explicit replay of the persisted idempotency key.
func (b *CensorRemoteBackend) call(parent context.Context, i CensorIdentity, method, path string, in, out any, key string) error {
	ctx, cancel := context.WithTimeout(parent, 90*time.Second)
	defer cancel()
	readOnly := method == http.MethodGet || method == http.MethodPost && path == "/search"
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err = b.callOnce(ctx, i, method, path, in, out, key)
		if err == nil || !readOnly || !errors.Is(err, ErrCensorUnavailable) || attempt == 2 {
			return err
		}
		delay := 250 * time.Millisecond * time.Duration(1<<attempt)
		var transient *censorRemoteTransient
		if errors.As(err, &transient) && transient.retryAfter > delay {
			if transient.retryAfter > 5*time.Second {
				return err
			}
			delay = transient.retryAfter
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return err
}
func censorRetryAfter(value string) time.Duration {
	if value == "" {
		return 0
	}
	if seconds, e := strconv.Atoi(value); e == nil {
		if seconds <= 0 {
			return 0
		}
		if seconds > 5 {
			return 6 * time.Second
		}
		return time.Duration(seconds) * time.Second
	}
	if deadline, e := http.ParseTime(value); e == nil {
		delay := time.Until(deadline)
		if delay > 0 {
			return delay
		}
	}
	return 0
}
