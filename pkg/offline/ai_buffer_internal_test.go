package offline

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wakatime/wakatime-cli/pkg/heartbeat"
	bolt "go.etcd.io/bbolt"
)

func TestAIBufferingSurvivesRestartAndDrainsWithoutNewActivity(t *testing.T) {
	for _, partitioned := range []bool{false, true} {
		name := "one batch"
		if partitioned {
			name = "separate invocations"
		}

		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "queue.bdb")
				start := time.Now().Truncate(heartbeat.AIBufferDuration)
				first := bufferedTestHeartbeat(start.Add(10*time.Second), 3)
				middle := bufferedTestHeartbeat(start.Add(80*time.Second), 7)
				last := bufferedTestHeartbeat(start.Add(250*time.Second), 11)
				human := first
				human.AISession = ""
				human.AIInputTokens = 0
				human.Category = heartbeat.CodingCategory.String()
				human.Time = float64(start.Add(50 * time.Second).Unix())
				time.Sleep(270 * time.Second)

				if partitioned {
					stageTestAIBuffer(t, path, []heartbeat.Heartbeat{first, middle})
					stageTestAIBuffer(t, path, []heartbeat.Heartbeat{last, first})
				} else {
					stageTestAIBuffer(t, path, []heartbeat.Heartbeat{first, middle, last, first})
				}

				require.NoError(t, pushHeartbeats(t.Context(), path, dbBucket, []heartbeat.Heartbeat{human}))

				count, err := CountHeartbeats(t.Context(), path)
				require.NoError(t, err)
				assert.Equal(t, 4, count)
				visible, err := ReadHeartbeats(t.Context(), path, 2)
				require.NoError(t, err)
				assert.Equal(t, []heartbeat.Heartbeat{first, human}, visible)
				assert.ElementsMatch(t, []heartbeat.Heartbeat{first, middle, last}, readTestBufferBucket(t, path, "ai_pending"))

				time.Sleep(30 * time.Second)

				var sent []heartbeat.Heartbeat

				require.NoError(t, Sync(t.Context(), path, 100)(func(
					_ context.Context,
					hh []heartbeat.Heartbeat,
				) ([]heartbeat.Result, error) {
					sent = append(sent, hh...)
					return acceptedBufferResults(hh), nil
				}))

				first.AIInputTokens = 0
				last.AIInputTokens = 21
				assert.ElementsMatch(t, []heartbeat.Heartbeat{first, human, last}, sent)
				count, err = CountHeartbeats(t.Context(), path)
				require.NoError(t, err)
				assert.Zero(t, count)
			})
		})
	}
}

func TestAIBufferingPassesHumanAndUnbufferableAIImmediately(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.bdb")
	human := bufferedTestHeartbeat(time.Unix(601, 0), 3)
	human.Category = heartbeat.CodingCategory.String()
	emptySession := bufferedTestHeartbeat(time.Unix(602, 0), 7)
	emptySession.AISession = ""
	configured := bufferedTestHeartbeat(time.Unix(603, 0), 11)
	configured.SkipAIBuffering = true
	expected := []heartbeat.Heartbeat{human, emptySession, configured}

	var sent []heartbeat.Heartbeat

	_, err := WithAIBuffering(path)(func(_ context.Context, hh []heartbeat.Heartbeat) ([]heartbeat.Result, error) {
		sent = hh
		return acceptedBufferResults(hh), nil
	})(t.Context(), expected)
	require.NoError(t, err)

	assert.Equal(t, expected, sent)
	count, err := CountHeartbeats(t.Context(), path)
	require.NoError(t, err)
	assert.Zero(t, count)
}

func TestQueuePromoteAIIsAtomicAndPreservesOpenAndLateActivity(t *testing.T) {
	db := openTestQueueDB(t)
	defer db.Close()

	first := bufferedTestHeartbeat(time.Unix(601, 0), 3)
	middle := bufferedTestHeartbeat(time.Unix(650, 0), 7)
	last := bufferedTestHeartbeat(time.Unix(899, 0), 11)
	prompt := bufferedTestHeartbeat(time.Unix(670, 0), 13)
	prompt.AIPromptLength = 17
	open := bufferedTestHeartbeat(time.Unix(901, 0), 19)
	input := []heartbeat.Heartbeat{first, middle, last, prompt, open}

	require.NoError(t, db.Update(func(tx *bolt.Tx) error {
		pending := NewQueue(tx)
		pending.Bucket = "ai_pending"

		return pending.PushMany(input)
	}))

	aborted := errors.New("abort promotion")

	err := db.Update(func(tx *bolt.Tx) error {
		require.NoError(t, NewQueue(tx).PromoteAI(time.Unix(900, 0)))
		return aborted
	})

	require.ErrorIs(t, err, aborted)
	require.NoError(t, db.Update(func(tx *bolt.Tx) error {
		ready, err := NewQueue(tx).ReadMany(100)
		require.NoError(t, err)
		assert.Empty(t, ready)

		pending := NewQueue(tx)
		pending.Bucket = "ai_pending"
		stored, err := pending.ReadMany(100)
		require.NoError(t, err)
		assert.ElementsMatch(t, input, stored)

		return nil
	}))

	first.AIInputTokens = 0
	last.AIInputTokens = 21
	late := bufferedTestHeartbeat(time.Unix(620, 0), 23)

	require.NoError(t, db.Update(func(tx *bolt.Tx) error {
		queue := NewQueue(tx)
		require.NoError(t, queue.PromoteAI(time.Unix(900, 0)))

		pending := NewQueue(tx)
		pending.Bucket = "ai_pending"
		require.NoError(t, pending.PushMany([]heartbeat.Heartbeat{late}))
		require.NoError(t, queue.PromoteAI(time.Unix(900, 0)))
		require.NoError(t, queue.PromoteAI(time.Unix(900, 0)))

		ready, err := queue.ReadMany(100)
		require.NoError(t, err)
		assert.ElementsMatch(t, []heartbeat.Heartbeat{first, last, prompt, late}, ready)

		stored, err := pending.ReadMany(100)
		require.NoError(t, err)
		assert.Equal(t, []heartbeat.Heartbeat{open}, stored)

		return nil
	}))
}

func TestAIBufferingRetriesReadyRecordsWithoutBufferingThemAgain(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "queue.bdb")
		start := time.Now().Truncate(heartbeat.AIBufferDuration)
		first := bufferedTestHeartbeat(start.Add(10*time.Second), 3)
		middle := bufferedTestHeartbeat(start.Add(20*time.Second), 7)
		last := bufferedTestHeartbeat(start.Add(25*time.Second), 11)
		time.Sleep(30 * time.Second)
		stageTestAIBuffer(t, path, []heartbeat.Heartbeat{first, middle, last})
		time.Sleep(270 * time.Second)

		var failed heartbeat.Heartbeat

		require.NoError(t, Sync(t.Context(), path, 100)(func(
			_ context.Context,
			hh []heartbeat.Heartbeat,
		) ([]heartbeat.Result, error) {
			results := acceptedBufferResults(hh)
			for i, h := range hh {
				if h.AIInputTokens != 0 {
					failed = h
					results[i].Status = http.StatusServiceUnavailable
				}
			}

			return results, nil
		}))

		last.AIInputTokens = 21
		assert.Equal(t, last, failed)
		assert.Equal(t, []heartbeat.Heartbeat{last}, readTestBufferBucket(t, path, dbBucket))
		assert.Empty(t, readTestBufferBucket(t, path, "ai_pending"))

		open := bufferedTestHeartbeat(start.Add(305*time.Second), 13)
		time.Sleep(10 * time.Second)
		stageTestAIBuffer(t, path, []heartbeat.Heartbeat{open})

		var retried []heartbeat.Heartbeat

		require.NoError(t, Sync(t.Context(), path, 100)(func(
			_ context.Context,
			hh []heartbeat.Heartbeat,
		) ([]heartbeat.Result, error) {
			retried = append(retried, hh...)
			return acceptedBufferResults(hh), nil
		}))

		assert.Equal(t, []heartbeat.Heartbeat{last}, retried)
		assert.Equal(t, []heartbeat.Heartbeat{open}, readTestBufferBucket(t, path, "ai_pending"))
	})
}

func bufferedTestHeartbeat(at time.Time, input int64) heartbeat.Heartbeat {
	return heartbeat.Heartbeat{
		AISession:     "session-a",
		AIInputTokens: input,
		Category:      heartbeat.AICodingCategory.String(),
		Entity:        "Assistant session",
		EntityType:    heartbeat.AppType,
		Project:       heartbeat.PointerTo("app"),
		Time:          float64(at.Unix()),
		UserAgent:     "model-a/1 plugin/1",
	}
}

func stageTestAIBuffer(t *testing.T, path string, hh []heartbeat.Heartbeat) {
	t.Helper()

	_, err := WithAIBuffering(path)(func(_ context.Context, immediate []heartbeat.Heartbeat) ([]heartbeat.Result, error) {
		assert.Empty(t, immediate)
		return acceptedBufferResults(immediate), nil
	})(t.Context(), hh)
	require.NoError(t, err)
}

func readTestBufferBucket(t *testing.T, path, bucket string) []heartbeat.Heartbeat {
	t.Helper()

	db, err := bolt.Open(path, 0600, nil)
	require.NoError(t, err)

	defer db.Close()

	var stored []heartbeat.Heartbeat

	require.NoError(t, db.Update(func(tx *bolt.Tx) error {
		queue := NewQueue(tx)
		queue.Bucket = bucket
		stored, err = queue.ReadMany(100)

		return err
	}))

	return stored
}

func acceptedBufferResults(hh []heartbeat.Heartbeat) []heartbeat.Result {
	results := make([]heartbeat.Result, len(hh))
	for i, h := range hh {
		results[i] = heartbeat.Result{Status: http.StatusCreated, Heartbeat: h}
	}

	return results
}
