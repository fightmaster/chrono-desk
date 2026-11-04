package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// Rate-limit retries reuse the exact sealed request body. A transport failure or
// ambiguous acknowledgement is left to the existing durable outbox retry path.
type syncRetryTransport struct {
	base http.RoundTripper
	wait func(context.Context, time.Duration) error
}

func (t *syncRetryTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	current := request
	for attempt := 0; ; attempt++ {
		response, err := t.base.RoundTrip(current)
		if err != nil || response.StatusCode != http.StatusTooManyRequests || attempt >= 3 || (request.Body != nil && request.GetBody == nil) {
			return response, err
		}
		delay := syncRetryDelay(response.Header.Get("Retry-After"), time.Now())
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		_ = response.Body.Close()
		if err := t.wait(request.Context(), delay); err != nil {
			return nil, err
		}
		current = request.Clone(request.Context())
		if request.GetBody != nil {
			body, err := request.GetBody()
			if err != nil {
				return nil, fmt.Errorf("reopen sync retry body: %w", err)
			}
			current.Body = body
		}
	}
}

func syncRetryDelay(value string, now time.Time) time.Duration {
	if seconds, err := strconv.ParseInt(value, 10, 32); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if date, err := http.ParseTime(value); err == nil {
		if delay := date.Sub(now); delay > 0 {
			return delay
		}
	}
	return time.Minute
}

func waitSyncRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (t *syncRetryTransport) CloseIdleConnections() {
	if closer, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}
