package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
)

func (s *Store) EdgeRelayConfig(ctx context.Context, eventID string) (domain.EdgeRelayConfig, error) {
	var config domain.EdgeRelayConfig
	err := s.db.QueryRowContext(ctx, `SELECT endpoint,enabled,revision,tls_bundle FROM edge_relay_config WHERE event_id=?`, eventID).
		Scan(&config.Endpoint, &config.Enabled, &config.Revision, &config.TLSBundle)
	if errors.Is(err, sql.ErrNoRows) {
		return config, nil
	}
	return config, err
}

// ConfigureEdgeRelay requires explicit consent to redirect a pending queue.
// Only the delivery destination changes; immutable source packets never do.
func (s *Store) ConfigureEdgeRelay(ctx context.Context, eventID string, requested domain.EdgeRelayConfig, confirmPending bool) (domain.EdgeRelayConfig, error) {
	requested.Endpoint = strings.TrimPrefix(strings.TrimSpace(requested.Endpoint), "tcp://")
	secure := strings.HasPrefix(requested.Endpoint, "tls://")
	if secure {
		requested.Endpoint = strings.TrimPrefix(requested.Endpoint, "tls://")
	}
	if (secure && (requested.Endpoint == "" || requested.TLSBundle == "" || !filepath.IsAbs(requested.TLSBundle) || filepath.Clean(requested.TLSBundle) != requested.TLSBundle)) ||
		(!secure && requested.TLSBundle != "") || len(requested.TLSBundle) > 4096 || strings.ContainsAny(requested.TLSBundle, "\r\n\x00") {
		return domain.EdgeRelayConfig{}, errors.New("для TLS укажите отдельный каталог сертификата Desk; обычный адрес не может использовать TLS-ключ")
	}
	if requested.Endpoint != "" {
		host, portText, err := net.SplitHostPort(requested.Endpoint)
		port, portErr := strconv.Atoi(portText)
		if err != nil || portErr != nil || port < 1 || port > 65535 || host == "" || len(host) > 253 || strings.ContainsAny(host, " /?#@\t\r\n\x00") {
			return domain.EdgeRelayConfig{}, errors.New("укажите адрес edge-входа Hub как host:port")
		}
		requested.Endpoint = net.JoinHostPort(host, strconv.Itoa(port))
		if secure {
			requested.Endpoint = "tls://" + requested.Endpoint
		}
	}
	if requested.Revision < 0 || (requested.Enabled && requested.Endpoint == "") {
		return domain.EdgeRelayConfig{}, errors.New("для досылки требуется явный адрес Hub")
	}
	var saved domain.EdgeRelayConfig
	err := s.WithinTx(ctx, func(tx *Store) error {
		if _, err := tx.GetEvent(ctx, eventID); err != nil {
			return err
		}
		previous, err := tx.EdgeRelayConfig(ctx, eventID)
		if err != nil {
			return err
		}
		if previous.Revision != requested.Revision {
			return errors.New("настройки досылки изменились; обновите страницу")
		}
		if previous.Endpoint == requested.Endpoint && previous.Enabled == requested.Enabled && previous.TLSBundle == requested.TLSBundle {
			saved = previous
			return nil
		}
		pending, err := tx.EdgePendingCount(ctx, eventID)
		if err != nil {
			return err
		}
		if (previous.Endpoint != requested.Endpoint || previous.TLSBundle != requested.TLSBundle) && pending > 0 && !confirmPending {
			return errors.New("подтвердите отправку сохранённой очереди на новый адрес")
		}
		saved = requested
		saved.Revision++
		if _, err := tx.db.ExecContext(ctx, `INSERT INTO edge_relay_config(event_id,endpoint,enabled,revision,tls_bundle) VALUES(?,?,?,?,?)
			ON CONFLICT(event_id) DO UPDATE SET endpoint=excluded.endpoint,enabled=excluded.enabled,revision=excluded.revision,tls_bundle=excluded.tls_bundle`, eventID, saved.Endpoint, saved.Enabled, saved.Revision, saved.TLSBundle); err != nil {
			return err
		}
		before, _ := json.Marshal(previous)
		after, _ := json.Marshal(struct {
			domain.EdgeRelayConfig
			Pending        int64 `json:"pending"`
			ConfirmPending bool  `json:"confirm_pending"`
		}{saved, pending, confirmPending})
		return tx.InsertLocalChange(ctx, LocalChange{Entity: "edge_relay", EntityID: eventID, Field: "config", OldValue: string(before), NewValue: string(after)})
	})
	return saved, err
}

type EdgeRelayClaim struct {
	Sequence      int64
	ObservationID string
	Payload       []byte
	Token         string
	Endpoint      string
	Revision      int64
	Attempts      int64
}

// ClaimEdgeRelay commits a short lease before network I/O. Leases also bound
// restart recovery and prevent two app instances from draining one row at once.
func (s *Store) ClaimEdgeRelay(ctx context.Context, eventID string, config domain.EdgeRelayConfig, now time.Time) (EdgeRelayClaim, bool, error) {
	var claim EdgeRelayClaim
	found := false
	err := s.WithinTx(ctx, func(tx *Store) error {
		current, err := tx.EdgeRelayConfig(ctx, eventID)
		if err != nil {
			return err
		}
		if !current.Enabled || current != config {
			return nil
		}
		var payload string
		err = tx.db.QueryRowContext(ctx, `SELECT sequence,observation_id,payload_json,attempts FROM edge_observation_outbox
			WHERE event_id=? AND state IN ('pending','sent','rejected') AND next_attempt_at<=? AND lease_until<=?
			ORDER BY sequence LIMIT 1`, eventID, now.UnixMilli(), now.UnixMilli()).
			Scan(&claim.Sequence, &claim.ObservationID, &payload, &claim.Attempts)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		claim.Payload = []byte(payload)
		claim.Endpoint, claim.Revision, claim.Token = current.Endpoint, current.Revision, rand.Text()
		claim.Attempts++
		result, err := tx.db.ExecContext(ctx, `UPDATE edge_observation_outbox SET state='sent',relay_endpoint=?,attempts=attempts+1,last_attempt_at=?,lease_token=?,lease_until=?
			WHERE event_id=? AND sequence=? AND state!='acked' AND lease_until<=?`, claim.Endpoint, now.UnixMilli(), claim.Token, now.Add(30*time.Second).UnixMilli(), eventID, claim.Sequence, now.UnixMilli())
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		found = n == 1
		return err
	})
	return claim, found, err
}

// FinishEdgeRelay never changes a raw observation or creates a site edit. A
// stale worker/config cannot acknowledge another lease or re-open an ACKed row.
func (s *Store) FinishEdgeRelay(ctx context.Context, eventID string, claim EdgeRelayClaim, now time.Time, deliveryErr error, retryAt time.Time) error {
	return s.WithinTx(ctx, func(tx *Store) error {
		current, err := tx.EdgeRelayConfig(ctx, eventID)
		if err != nil {
			return err
		}
		if current.Revision != claim.Revision || current.Endpoint != claim.Endpoint || !current.Enabled {
			return errors.New("edge relay configuration changed during delivery; confirmation remains pending")
		}
		state := "acked"
		var ackedAt any = now.UnixMilli()
		var failure any
		next := int64(0)
		if deliveryErr != nil {
			state, ackedAt = "pending", nil
			// Sender diagnostics contain no source packet/remote ACK body.
			message := deliveryErr.Error()
			if len(message) > 512 {
				message = message[:512]
			}
			failure = message
			next = retryAt.UnixMilli()
		}
		result, err := tx.db.ExecContext(ctx, `UPDATE edge_observation_outbox SET state=?,acked_at=?,rejection=?,next_attempt_at=?,lease_token=NULL,lease_until=0
			WHERE event_id=? AND sequence=? AND observation_id=? AND payload_json=? AND lease_token=? AND state='sent'`,
			state, ackedAt, failure, next, eventID, claim.Sequence, claim.ObservationID, string(claim.Payload), claim.Token)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("edge relay claim %d is no longer current", claim.Sequence)
		}
		return nil
	})
}

type EdgeRelayProgress struct {
	Pending     int64  `json:"pending"`
	Acked       int64  `json:"acked"`
	Attempts    int64  `json:"attempts"`
	LastAttempt int64  `json:"last_attempt_at"`
	LastError   string `json:"last_error"`
}

func (s *Store) EdgeRelayProgress(ctx context.Context, eventID string) (EdgeRelayProgress, error) {
	var progress EdgeRelayProgress
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(state!='acked'),0),COALESCE(SUM(state='acked'),0),COALESCE(SUM(attempts),0),COALESCE(MAX(last_attempt_at),0)
		FROM edge_observation_outbox WHERE event_id=?`, eventID).
		Scan(&progress.Pending, &progress.Acked, &progress.Attempts, &progress.LastAttempt)
	if err != nil {
		return progress, err
	}
	err = s.db.QueryRowContext(ctx, `SELECT rejection FROM edge_observation_outbox WHERE event_id=? AND state!='acked' AND rejection IS NOT NULL ORDER BY last_attempt_at DESC LIMIT 1`, eventID).Scan(&progress.LastError)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return progress, err
}
