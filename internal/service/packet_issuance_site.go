package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
	"gitlab.com/fightmaster1/chrono-desk/internal/packetissuance"
)

var packetRelayUUID = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

const maxPacketRelayURLBytes = 2048

var (
	ErrPacketSiteFeedCursorAhead   = errors.New("packet_site_feed_cursor_ahead")
	ErrPacketSiteFeedCursorExpired = errors.New("packet_site_feed_cursor_expired")
)

type PacketRelayDescriptor struct {
	SchemaVersion  int    `json:"schemaVersion"`
	RelayID        string `json:"relayId"`
	APIBaseURL     string `json:"apiBaseUrl"`
	ScopeID        string `json:"scopeId"`
	EventID        string `json:"eventId"`
	DeskInstanceID string `json:"deskInstanceId"`
	ExpiresAt      string `json:"expiresAt"`
	Receiver       struct {
		Bootstrap  bool `json:"bootstrap"`
		Operations bool `json:"operations"`
		Feed       bool `json:"feed"`
	} `json:"receiver"`
}

type PacketRelayStatus struct {
	Configured      bool   `json:"configured"`
	RelayID         string `json:"relay_id,omitempty"`
	ScopeID         string `json:"scope_id,omitempty"`
	ExpiresAt       string `json:"expires_at,omitempty"`
	FeedCursor      string `json:"feed_cursor,omitempty"`
	RosterInstalled bool   `json:"roster_installed"`
}

type PacketOperationSyncResult struct {
	Attempted int `json:"attempted"`
	Accepted  int `json:"accepted"`
	Waiting   int `json:"waiting"`
}

type PacketFeedSyncResult struct {
	Received int    `json:"received"`
	Applied  int    `json:"applied"`
	Review   int    `json:"review"`
	Cursor   string `json:"cursor,omitempty"`
	Rebased  bool   `json:"rebased,omitempty"`
}

func ConnectPacketIssuanceSite(ctx context.Context, events *EventService, eventID, label string) (PacketRelayStatus, error) {
	store, err := events.Open(eventID)
	if err != nil {
		return PacketRelayStatus{}, err
	}
	config, err := store.GetSyncConfig(ctx, eventID)
	if err != nil {
		return PacketRelayStatus{}, err
	}
	if strings.TrimSpace(config.BaseURL) == "" || strings.TrimSpace(config.Token) == "" {
		return PacketRelayStatus{}, errors.New("настройте адрес сайта и токен синхронизации")
	}
	state, err := events.PreparePacketRelay(ctx, eventID, strings.TrimRight(strings.TrimSpace(config.BaseURL), "/"))
	if err != nil {
		return PacketRelayStatus{}, err
	}
	descriptor, err := EnrollPacketRelay(ctx, state.SiteBaseURL, config.Token, eventID,
		state.DeskInstanceID, state.Credential, label)
	if err != nil {
		return PacketRelayStatus{}, err
	}
	state.RelayID, state.APIBaseURL, state.ScopeID, state.ExpiresAt =
		descriptor.RelayID, descriptor.APIBaseURL, descriptor.ScopeID, descriptor.ExpiresAt
	if err := events.CompletePacketRelay(ctx, state); err != nil {
		return PacketRelayStatus{}, err
	}
	bootstrap, err := FetchPacketBootstrap(ctx, descriptor, state.Credential)
	if err != nil {
		return PacketRelayStatus{}, err
	}
	if err := store.InstallPacketIssuanceRoster(ctx, sqlite.PacketIssuanceScope{
		EventID: bootstrap.Event.ID, ScopeID: bootstrap.ScopeID, BaselineID: bootstrap.BaselineID,
		SourceKind: bootstrap.SourceKind, SiteFeedCursor: bootstrap.FeedCursor,
	}, bootstrap.Registrations, bootstrap.ReserveOrigins); err != nil {
		return PacketRelayStatus{}, err
	}
	return PacketRelayStatus{
		Configured: true, RelayID: state.RelayID, ScopeID: state.ScopeID,
		ExpiresAt: state.ExpiresAt, FeedCursor: bootstrap.FeedCursor, RosterInstalled: true,
	}, nil
}

func GetPacketRelayStatus(ctx context.Context, events *EventService, eventID string) (PacketRelayStatus, error) {
	state, found, err := events.GetPacketRelay(ctx, eventID)
	if err != nil || !found {
		return PacketRelayStatus{}, err
	}
	store, err := events.Open(eventID)
	if err != nil {
		return PacketRelayStatus{}, err
	}
	scope, err := store.GetPacketIssuanceScope(ctx, eventID)
	if err != nil {
		return PacketRelayStatus{}, err
	}
	return PacketRelayStatus{
		Configured: state.RelayID != "", RelayID: state.RelayID, ScopeID: state.ScopeID,
		ExpiresAt: state.ExpiresAt, FeedCursor: scope.SiteFeedCursor,
		RosterInstalled: scope.ScopeID != "" && scope.ScopeID == state.ScopeID,
	}, nil
}

func SyncPacketOperations(ctx context.Context, events *EventService, eventID string) (PacketOperationSyncResult, error) {
	state, found, err := events.GetPacketRelay(ctx, eventID)
	if err != nil {
		return PacketOperationSyncResult{}, err
	}
	if !found || state.RelayID == "" || state.APIBaseURL == "" || state.Credential == "" {
		return PacketOperationSyncResult{}, nil
	}
	store, err := events.Open(eventID)
	if err != nil {
		return PacketOperationSyncResult{}, err
	}
	result := PacketOperationSyncResult{}
	for batch := 0; batch < 16; batch++ {
		operations, err := store.ListPendingPacketOperations(ctx, eventID, 64)
		if err != nil || len(operations) == 0 {
			return result, err
		}
		receipts, err := PushPacketOperations(ctx, state.APIBaseURL, state.Credential, operations)
		if err != nil {
			return result, err
		}
		result.Attempted += len(receipts)
		waitingThisBatch := 0
		err = store.WithinTx(ctx, func(txStore *sqlite.Store) error {
			for _, receipt := range receipts {
				if err := txStore.MarkPacketOperationSiteReceipt(ctx, receipt); err != nil {
					return err
				}
				if receipt.Outcome == "waiting_dependency" {
					result.Waiting++
					waitingThisBatch++
				} else {
					result.Accepted++
				}
			}
			return nil
		})
		if err != nil || len(operations) < 64 || waitingThisBatch > 0 {
			return result, err
		}
	}
	return result, errors.New("packet operation delivery limit reached; repeat synchronization")
}

func SyncPacketFeed(ctx context.Context, events *EventService, eventID string) (PacketFeedSyncResult, error) {
	state, found, err := events.GetPacketRelay(ctx, eventID)
	if err != nil {
		return PacketFeedSyncResult{}, err
	}
	if !found || state.RelayID == "" || state.APIBaseURL == "" || state.Credential == "" {
		return PacketFeedSyncResult{}, nil
	}
	store, err := events.Open(eventID)
	if err != nil {
		return PacketFeedSyncResult{}, err
	}
	result := PacketFeedSyncResult{}
	for pageNumber := 0; pageNumber < 100; pageNumber++ {
		scope, err := store.GetPacketIssuanceScope(ctx, eventID)
		if err != nil {
			return result, err
		}
		if scope.ScopeID == "" || scope.ScopeID != state.ScopeID {
			return result, errors.New("packet issuance roster is not installed")
		}
		page, err := PullPacketFeedPage(ctx, state.APIBaseURL, state.Credential, scope.ScopeID, scope.SiteFeedCursor)
		if err != nil {
			if errors.Is(err, ErrPacketSiteFeedCursorAhead) || errors.Is(err, ErrPacketSiteFeedCursorExpired) {
				bootstrap, fetchErr := FetchPacketBootstrap(ctx, PacketRelayDescriptor{
					APIBaseURL: state.APIBaseURL,
					ScopeID:    state.ScopeID,
					EventID:    eventID,
				}, state.Credential)
				if fetchErr != nil {
					return result, fetchErr
				}
				rebase, rebaseErr := RebasePacketIssuanceRoster(ctx, store, eventID, bootstrap)
				if rebaseErr != nil {
					return result, rebaseErr
				}
				result.Applied += rebase.Applied
				result.Review += rebase.Review
				result.Cursor = bootstrap.FeedCursor
				result.Rebased = true
				return result, nil
			}
			return result, err
		}
		applications, err := ApplyPacketFeedPage(ctx, store, eventID, page)
		if err != nil {
			return result, err
		}
		result.Received += len(applications)
		result.Cursor = page.Cursor.Next
		for _, application := range applications {
			switch application.Application {
			case "applied", "observed":
				result.Applied++
			case "review":
				result.Review++
			}
		}
		if !page.Cursor.HasMore {
			return result, nil
		}
	}
	return result, errors.New("packet feed page limit reached; repeat synchronization")
}

func EnrollPacketRelay(ctx context.Context, baseURL, token, eventID, deskInstanceID, credential, label string) (PacketRelayDescriptor, error) {
	endpoint, err := packetEnrollmentURL(baseURL, eventID)
	if err != nil {
		return PacketRelayDescriptor{}, err
	}
	payload, err := json.Marshal(map[string]any{
		"schemaVersion": 1, "deskInstanceId": deskInstanceID, "credential": credential, "label": label,
	})
	if err != nil {
		return PacketRelayDescriptor{}, fmt.Errorf("encode packet relay enrollment: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return PacketRelayDescriptor{}, fmt.Errorf("create packet relay enrollment: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-SYNC-TOKEN", token)
	resp, err := syncHTTPClient.Do(req)
	if err != nil {
		return PacketRelayDescriptor{}, fmt.Errorf("packet relay enrollment unavailable: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4097))
	if len(body) > 4096 {
		return PacketRelayDescriptor{}, errors.New("packet relay descriptor exceeds limit")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return PacketRelayDescriptor{}, fmt.Errorf("packet relay enrollment returned %d: %s", resp.StatusCode, summaryError(body))
	}
	descriptor, err := decodePacketRelayDescriptor(body)
	if err != nil {
		return PacketRelayDescriptor{}, err
	}
	if descriptor.EventID != eventID || descriptor.DeskInstanceID != deskInstanceID {
		return PacketRelayDescriptor{}, errors.New("packet relay descriptor identity mismatch")
	}
	return descriptor, nil
}

func FetchPacketBootstrap(ctx context.Context, descriptor PacketRelayDescriptor, credential string) (packetissuance.Bootstrap, error) {
	endpoint, err := packetRelayActionURL(descriptor.APIBaseURL, "/bootstrap")
	if err != nil {
		return packetissuance.Bootstrap{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?reserveOrigins=1", nil)
	if err != nil {
		return packetissuance.Bootstrap{}, fmt.Errorf("create packet roster request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+credential)
	resp, err := syncHTTPClient.Do(req)
	if err != nil {
		return packetissuance.Bootstrap{}, fmt.Errorf("packet roster unavailable: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, (32<<20)+1))
	if len(body) > 32<<20 {
		return packetissuance.Bootstrap{}, errors.New("packet roster exceeds limit")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return packetissuance.Bootstrap{}, fmt.Errorf("packet roster returned %d: %s", resp.StatusCode, summaryError(body))
	}
	bootstrap, err := packetissuance.ParseBootstrap(body)
	if err != nil || bootstrap.ScopeID != descriptor.ScopeID || bootstrap.Event.ID != descriptor.EventID {
		return packetissuance.Bootstrap{}, errors.New("packet roster contract mismatch")
	}
	return bootstrap, nil
}

func PushPacketOperations(ctx context.Context, apiBaseURL, credential string, operations []packetissuance.Operation) ([]packetissuance.Receipt, error) {
	if len(operations) < 1 || len(operations) > 64 {
		return nil, errors.New("invalid packet operation batch")
	}
	payload, err := json.Marshal(struct {
		SchemaVersion int                        `json:"schemaVersion"`
		Operations    []packetissuance.Operation `json:"operations"`
	}{SchemaVersion: 1, Operations: operations})
	if err != nil || len(payload) > 1<<20 {
		return nil, errors.New("invalid packet operation batch")
	}
	endpoint, err := packetRelayActionURL(apiBaseURL, "/operations")
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("create packet operation request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+credential)
	resp, err := syncHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("packet operation delivery unavailable: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if len(body) > 1<<20 {
		return nil, errors.New("packet operation receipt exceeds limit")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("packet operation delivery returned %d: %s", resp.StatusCode, summaryError(body))
	}
	var envelope struct {
		SchemaVersion *int `json:"schemaVersion"`
		Receipts      []struct {
			OperationID string          `json:"operationId"`
			ContentHash string          `json:"contentHash"`
			Outcome     string          `json:"outcome"`
			Known       *bool           `json:"known"`
			Code        json.RawMessage `json:"code"`
		} `json:"receipts"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil || envelope.SchemaVersion == nil || *envelope.SchemaVersion != 1 || len(envelope.Receipts) != len(operations) {
		return nil, errors.New("invalid packet operation receipts")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("invalid packet operation receipts")
	}
	receipts := make([]packetissuance.Receipt, 0, len(envelope.Receipts))
	for index, wire := range envelope.Receipts {
		if wire.Known == nil || wire.Code == nil {
			return nil, errors.New("invalid packet operation receipt")
		}
		var code *string
		if string(wire.Code) != "null" {
			var value string
			if err := json.Unmarshal(wire.Code, &value); err != nil {
				return nil, errors.New("invalid packet operation receipt")
			}
			code = &value
		}
		receipt := packetissuance.Receipt{OperationID: wire.OperationID, ContentHash: wire.ContentHash,
			Outcome: wire.Outcome, Known: *wire.Known, Code: code}
		operation := operations[index]
		if receipt.OperationID != operation.OperationID || receipt.ContentHash != packetissuance.ContentHash(operation) ||
			!validPacketReceipt(receipt) {
			return nil, errors.New("invalid packet operation receipt")
		}
		receipts = append(receipts, receipt)
	}
	return receipts, nil
}

func PullPacketFeedPage(ctx context.Context, apiBaseURL, credential, scopeID, after string) (packetissuance.FeedPage, error) {
	endpointString, err := packetRelayActionURL(apiBaseURL, "/feed")
	if err != nil {
		return packetissuance.FeedPage{}, err
	}
	endpoint, err := url.Parse(endpointString)
	if err != nil {
		return packetissuance.FeedPage{}, errors.New("invalid packet relay endpoint")
	}
	query := endpoint.Query()
	query.Set("after", after)
	query.Set("limit", "100")
	endpoint.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return packetissuance.FeedPage{}, fmt.Errorf("create packet feed request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+credential)
	resp, err := syncHTTPClient.Do(req)
	if err != nil {
		return packetissuance.FeedPage{}, fmt.Errorf("packet feed unavailable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4097))
		if cursorErr := packetFeedCursorError(resp, body); cursorErr != nil {
			return packetissuance.FeedPage{}, cursorErr
		}
		return packetissuance.FeedPage{}, fmt.Errorf("packet feed returned %d: %s", resp.StatusCode, summaryError(body))
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, (32<<20)+1))
	if len(body) > 32<<20 {
		return packetissuance.FeedPage{}, errors.New("packet feed exceeds limit")
	}
	page, err := packetissuance.ParseFeedPage(body, scopeID, after)
	if err != nil {
		return packetissuance.FeedPage{}, err
	}
	return page, nil
}

func packetFeedCursorError(resp *http.Response, body []byte) error {
	if resp.StatusCode != http.StatusConflict || len(body) == 0 || len(body) > 4096 {
		return nil
	}
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return nil
	}
	var envelope map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&envelope); err != nil || len(envelope) != 1 {
		return nil
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil
	}
	for _, key := range []string{"message", "error"} {
		raw, found := envelope[key]
		if !found {
			continue
		}
		var code string
		if json.Unmarshal(raw, &code) != nil {
			return nil
		}
		switch code {
		case "cursor_ahead":
			return ErrPacketSiteFeedCursorAhead
		case "cursor_expired":
			return ErrPacketSiteFeedCursorExpired
		}
	}
	return nil
}

func validPacketReceipt(receipt packetissuance.Receipt) bool {
	switch receipt.Outcome {
	case "applied", "equivalent":
		return receipt.Code == nil
	case "waiting_dependency", "conflict", "rejected":
		return receipt.Code != nil && *receipt.Code != ""
	default:
		return false
	}
}

func packetEnrollmentURL(baseURL, eventID string) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if len(base) == 0 || len(base) > maxPacketRelayURLBytes || strings.ContainsRune(base, '\x00') {
		return "", errors.New("packet issuance requires a bounded HTTPS site address")
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("packet issuance requires an HTTPS site address")
	}
	return base + "/api/sync/events/" + url.PathEscape(eventID) + "/packet-issuance-relays", nil
}

func decodePacketRelayDescriptor(body []byte) (PacketRelayDescriptor, error) {
	var descriptor PacketRelayDescriptor
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&descriptor); err != nil {
		return PacketRelayDescriptor{}, fmt.Errorf("invalid packet relay descriptor: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return PacketRelayDescriptor{}, errors.New("invalid packet relay descriptor")
	}
	if len(descriptor.APIBaseURL) == 0 || len(descriptor.APIBaseURL) > maxPacketRelayURLBytes || strings.ContainsRune(descriptor.APIBaseURL, '\x00') {
		return PacketRelayDescriptor{}, errors.New("invalid packet relay endpoint")
	}
	endpoint, err := url.Parse(descriptor.APIBaseURL)
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" ||
		!strings.HasSuffix(strings.TrimRight(endpoint.Path, "/"), "/relays/"+descriptor.RelayID) {
		return PacketRelayDescriptor{}, errors.New("invalid packet relay endpoint")
	}
	expires, err := time.Parse("2006-01-02T15:04:05.000Z", descriptor.ExpiresAt)
	if descriptor.SchemaVersion != 1 || !packetRelayUUID.MatchString(descriptor.RelayID) ||
		!packetRelayUUID.MatchString(descriptor.DeskInstanceID) || descriptor.ScopeID == "" || descriptor.EventID == "" ||
		err != nil || !expires.After(time.Now().UTC()) || !descriptor.Receiver.Bootstrap ||
		!descriptor.Receiver.Operations || !descriptor.Receiver.Feed {
		return PacketRelayDescriptor{}, errors.New("unsupported packet relay descriptor")
	}
	return descriptor, nil
}

func packetRelayActionURL(apiBaseURL, suffix string) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(apiBaseURL), "/")
	if len(base) == 0 || len(base) > maxPacketRelayURLBytes || strings.ContainsRune(base, '\x00') {
		return "", errors.New("invalid packet relay endpoint")
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("invalid packet relay endpoint")
	}
	return base + suffix, nil
}
