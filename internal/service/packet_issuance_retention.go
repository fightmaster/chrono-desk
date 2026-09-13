package service

import (
	"context"
	"errors"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
)

const packetFeedArchiveBatchLimit int64 = 100

type PacketFeedRetentionResult struct {
	Head           int64 `json:"head"`
	FirstAvailable int64 `json:"first_available"`
	Candidate      int64 `json:"candidate"`
	Processed      int64 `json:"processed"`
	Through        int64 `json:"through"`
	Executed       bool  `json:"executed"`
}

// CompactPacketIssuanceFeed archives at most one small contiguous batch. The
// operational caller must first stop LAN admission and reject active grants.
// Dry-run is the default boundary exposed to operators.
func CompactPacketIssuanceFeed(ctx context.Context, store *sqlite.Store, eventID string,
	keep int64, execute bool, now time.Time) (PacketFeedRetentionResult, error) {
	if eventID == "" || keep < 1 || keep > 100_000 || now.IsZero() {
		return PacketFeedRetentionResult{}, errors.New("invalid packet feed retention request")
	}
	var result PacketFeedRetentionResult
	err := store.WithinTx(ctx, func(txStore *sqlite.Store) error {
		bounds, err := txStore.PacketFeedBounds(ctx, eventID)
		if err != nil {
			return err
		}
		result.Head = bounds.Head
		result.FirstAvailable = bounds.FirstAvailable
		lastArchivable := bounds.Head - keep
		if lastArchivable < bounds.FirstAvailable {
			return nil
		}
		result.Candidate = lastArchivable - bounds.FirstAvailable + 1
		result.Processed = result.Candidate
		if result.Processed > packetFeedArchiveBatchLimit {
			result.Processed = packetFeedArchiveBatchLimit
		}
		result.Through = bounds.FirstAvailable + result.Processed - 1
		if !execute {
			return nil
		}
		archived, err := txStore.ArchivePacketFeedPrefix(ctx, eventID, result.Through, now.UnixMilli())
		if err != nil {
			return err
		}
		if int64(archived.Archived) != result.Processed || archived.FirstAvailable != result.Through+1 {
			return errors.New("packet feed archive result mismatch")
		}
		result.FirstAvailable = archived.FirstAvailable
		result.Executed = true
		return nil
	})
	return result, err
}
