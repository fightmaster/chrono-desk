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

type SyncCapabilities struct {
	PushSchemaVersions         []int `json:"push_schema_versions"`
	PreferredPushSchemaVersion int   `json:"preferred_push_schema_version"`
}

type ObservationAckItem struct {
	ID             string `json:"id"`
	OriginSequence int64  `json:"origin_sequence"`
	Status         string `json:"status"`
	Reason         string `json:"reason,omitempty"`
}

type ObservationAck struct {
	BatchID                 string               `json:"batch_id"`
	OriginInstanceID        string               `json:"origin_instance_id"`
	AcceptedThroughSequence *int64               `json:"accepted_through_sequence"`
	Items                   []ObservationAckItem `json:"items"`
}

type SyncPushResponse struct {
	Summary        map[string]any
	ObservationAck *ObservationAck
}

// PushSync POSTs the assembled payload to run5 and returns the decoded summary.
func PushSync(ctx context.Context, baseURL, token, eventID string, payload []byte) (SyncPushResponse, error) {
	var header struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal(payload, &header); err != nil || header.SchemaVersion <= 0 {
		return SyncPushResponse{}, fmt.Errorf("не определить версию payload")
	}
	capabilities, err := GetSyncCapabilities(ctx, baseURL, token, eventID)
	if err != nil {
		return SyncPushResponse{}, err
	}
	if !supportsSyncSchema(capabilities.PushSchemaVersions, header.SchemaVersion) {
		return SyncPushResponse{}, fmt.Errorf("сайт не поддерживает безопасную схему синхронизации v%d", header.SchemaVersion)
	}
	url, err := syncURL(baseURL, eventID)
	if err != nil {
		return SyncPushResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return SyncPushResponse{}, fmt.Errorf("сформировать запрос: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-SYNC-TOKEN", token)

	resp, err := syncHTTPClient.Do(req)
	if err != nil {
		return SyncPushResponse{}, fmt.Errorf("нет связи с сайтом: %w", err)
	}
	defer resp.Body.Close()
	// A maximum-size v3 batch can legitimately return 20,000 item-level
	// acknowledgements. Keep the response bounded, but large enough for the
	// contract's worst-case identifiers and rejection reasons.
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return SyncPushResponse{}, fmt.Errorf("сайт ответил %d: %s", resp.StatusCode, summaryError(body))
	}
	var summary map[string]any
	var envelope struct {
		ObservationAck *ObservationAck `json:"observation_ack"`
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &summary); err != nil {
			return SyncPushResponse{}, fmt.Errorf("ответ сайта не разобран: %w", err)
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			return SyncPushResponse{}, fmt.Errorf("подтверждение сайта не разобрано: %w", err)
		}
	}
	return SyncPushResponse{Summary: summary, ObservationAck: envelope.ObservationAck}, nil
}

func GetSyncCapabilities(ctx context.Context, baseURL, token, eventID string) (SyncCapabilities, error) {
	url, err := syncURL(baseURL, eventID)
	if err != nil {
		return SyncCapabilities{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/capabilities", nil)
	if err != nil {
		return SyncCapabilities{}, fmt.Errorf("сформировать запрос возможностей: %w", err)
	}
	req.Header.Set("X-SYNC-TOKEN", token)
	resp, err := syncHTTPClient.Do(req)
	if err != nil {
		return SyncCapabilities{}, fmt.Errorf("не проверить версию синхронизации сайта: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return SyncCapabilities{}, fmt.Errorf("сайт не подтвердил безопасную синхронизацию (%d): %s", resp.StatusCode, summaryError(body))
	}
	var capabilities SyncCapabilities
	if err := json.Unmarshal(body, &capabilities); err != nil {
		return SyncCapabilities{}, fmt.Errorf("возможности синхронизации сайта не разобраны: %w", err)
	}
	return capabilities, nil
}

func supportsSyncSchema(versions []int, want int) bool {
	for _, version := range versions {
		if version == want {
			return true
		}
	}
	return false
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
