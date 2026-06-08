package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Outbound client for the run5 sync API. This is the app's only outbound HTTP
// surface; everything else is the local embedded server. Auth is a per-event
// token sent as X-SYNC-TOKEN (mirrors run5's PWA token).

const syncHTTPTimeout = 120 * time.Second

var syncHTTPClient = &http.Client{Timeout: syncHTTPTimeout}

// PushSync POSTs the assembled payload to run5 and returns the decoded summary.
func PushSync(ctx context.Context, baseURL, token, eventID string, payload []byte) (map[string]any, error) {
	url, err := syncURL(baseURL, eventID)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("сформировать запрос: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-SYNC-TOKEN", token)

	resp, err := syncHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("нет связи с сайтом: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("сайт ответил %d: %s", resp.StatusCode, summaryError(body))
	}
	var summary map[string]any
	if len(body) > 0 {
		if err := json.Unmarshal(body, &summary); err != nil {
			return nil, fmt.Errorf("ответ сайта не разобран: %w", err)
		}
	}
	return summary, nil
}

// PullExport GETs the current event export from run5 (same contract chrono-desk
// imports). Returns the raw JSON bytes.
func PullExport(ctx context.Context, baseURL, token, eventID string) ([]byte, error) {
	url, err := syncURL(baseURL, eventID)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("сформировать запрос: %w", err)
	}
	req.Header.Set("X-SYNC-TOKEN", token)

	resp, err := syncHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("нет связи с сайтом: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("сайт ответил %d: %s", resp.StatusCode, summaryError(body))
	}
	return body, nil
}

func syncURL(baseURL, eventID string) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return "", fmt.Errorf("не задан адрес сайта")
	}
	return base + "/api/sync/events/" + eventID, nil
}

// summaryError pulls a human message out of a run5 error body, falling back to
// the raw text.
func summaryError(body []byte) string {
	var m map[string]any
	if json.Unmarshal(body, &m) == nil {
		for _, k := range []string{"error", "message"} {
			if v, ok := m[k].(string); ok && v != "" {
				return v
			}
		}
	}
	s := strings.TrimSpace(string(body))
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}
