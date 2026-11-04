package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type syncRoundTripFunc func(*http.Request) (*http.Response, error)

func (f syncRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSyncRateLimitWaitsAndRetriesIdenticalBody(t *testing.T) {
	calls := 0
	var delay time.Duration
	transport := &syncRetryTransport{wait: func(_ context.Context, d time.Duration) error { delay = d; return nil }}
	transport.base = syncRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"batch_id":"sealed"}` {
			t.Fatalf("retry changed body: %s", body)
		}
		calls++
		status := 200
		if calls == 1 {
			status = 429
		}
		return &http.Response{StatusCode: status, Header: http.Header{"Retry-After": []string{"17"}}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})
	request, _ := http.NewRequest("POST", "https://example.test", strings.NewReader(`{"batch_id":"sealed"}`))
	response, err := transport.RoundTrip(request)
	if err != nil || response.StatusCode != 200 || calls != 2 || delay != 17*time.Second {
		t.Fatalf("response=%v calls=%d delay=%v err=%v", response, calls, delay, err)
	}
}

func TestSyncRateLimitRetryStopsOnCancellationAndIsBounded(t *testing.T) {
	calls := 0
	base := syncRoundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 429, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})
	transport := &syncRetryTransport{base: base, wait: func(context.Context, time.Duration) error { return context.Canceled }}
	request, _ := http.NewRequest("GET", "https://example.test", nil)
	if _, err := transport.RoundTrip(request); !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
	transport.wait = func(context.Context, time.Duration) error { return nil }
	calls = 0
	response, err := transport.RoundTrip(request)
	if err != nil || response.StatusCode != 429 || calls != 4 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}
