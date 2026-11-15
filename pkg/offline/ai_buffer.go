package offline

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/wakatime/wakatime-cli/pkg/heartbeat"
)

const aiPendingBucket = "ai_pending"

// WithAIBuffering persists session-based AI activity until its UTC bucket closes.
// Custom routing bypasses buffering because queue serialization omits credentials.
func WithAIBuffering(filepath string) heartbeat.HandleOption {
	return func(next heartbeat.Handle) heartbeat.Handle {
		return func(ctx context.Context, hh []heartbeat.Heartbeat) ([]heartbeat.Result, error) {
			var pending, immediate []heartbeat.Heartbeat

			for _, h := range hh {
				if h.Category == heartbeat.AICodingCategory.String() && h.AISession != "" && !h.SkipAIBuffering {
					pending = append(pending, h)
				} else {
					immediate = append(immediate, h)
				}
			}

			if len(pending) > 0 {
				if err := pushHeartbeatsWithRetry(ctx, filepath, aiPendingBucket, pending); err != nil {
					return nil, fmt.Errorf("failed to buffer ai heartbeats: %w", err)
				}
			}

			if len(immediate) == 0 {
				return nil, nil
			}

			return next(ctx, immediate)
		}
	}
}

// PromoteAI moves closed AI buckets into the ready queue within the caller's
// transaction. Pending originals are deleted only after all compacted events save.
func (q *Queue) PromoteAI(now time.Time) error {
	pending := q.tx.Bucket([]byte(aiPendingBucket))
	if pending == nil {
		return nil
	}

	cutoff := float64(now.Truncate(heartbeat.AIBufferDuration).Unix())

	var (
		closed []heartbeat.Heartbeat
		ids    [][]byte
	)

	cursor := pending.Cursor()

	for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
		var h heartbeat.Heartbeat
		if err := json.Unmarshal(value, &h); err != nil {
			return fmt.Errorf("failed to decode pending ai heartbeat: %w", err)
		}

		if h.Time >= cutoff {
			continue
		}

		closed = append(closed, h)
		ids = append(ids, append([]byte(nil), key...))
	}

	if len(closed) == 0 {
		return nil
	}

	if err := q.PushMany(heartbeat.CompactAIHeartbeats(closed)); err != nil {
		return fmt.Errorf("failed to queue compacted ai heartbeats: %w", err)
	}

	for _, id := range ids {
		if err := pending.Delete(id); err != nil {
			return fmt.Errorf("failed to delete promoted ai heartbeat: %w", err)
		}
	}

	return nil
}
