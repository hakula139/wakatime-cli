package offline_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	cmdoffline "github.com/wakatime/wakatime-cli/cmd/offline"
	"github.com/wakatime/wakatime-cli/pkg/heartbeat"
	"github.com/wakatime/wakatime-cli/pkg/offline"
	bolt "go.etcd.io/bbolt"
)

func TestSaveHeartbeatsPreservesProjectConfiguredAIActivity(t *testing.T) {
	for _, configured := range []bool{false, true} {
		name := "buffered"
		if configured {
			name = "project configuration bypass"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			entity := filepath.Join(dir, "main.go")
			require.NoError(t, os.WriteFile(entity, []byte("package main\n"), 0600))
			if configured {
				require.NoError(t, os.WriteFile(filepath.Join(dir, ".wakatime"), nil, 0600))
			}
			config := filepath.Join(t.TempDir(), "config")
			require.NoError(t, os.WriteFile(config, nil, 0600))
			queuePath := filepath.Join(t.TempDir(), "queue.bdb")
			v := viper.New()
			v.Set("config", config)
			v.Set("entity", entity)
			v.Set("key", "00000000-0000-4000-8000-000000000000")
			v.Set("project", "app")
			h := heartbeat.Heartbeat{
				AISession:       "session-a",
				AIInputTokens:   3,
				AIPromptLength:  7,
				Category:        heartbeat.AICodingCategory.String(),
				Entity:          entity,
				EntityType:      heartbeat.FileType,
				ProjectOverride: "app",
				Time:            float64(time.Now().Unix()),
				UserAgent:       "model-a/1 plugin/1",
			}

			require.NoError(t, cmdoffline.SaveHeartbeats(t.Context(), v, queuePath, []heartbeat.Heartbeat{h}))

			db, err := bolt.Open(queuePath, 0600, nil)
			require.NoError(t, err)
			defer db.Close()
			require.NoError(t, db.Update(func(tx *bolt.Tx) error {
				for _, bucket := range []string{"heartbeats", "ai_pending"} {
					queue := offline.NewQueue(tx)
					queue.Bucket = bucket
					stored, err := queue.ReadMany(10)
					require.NoError(t, err)
					if (bucket == "heartbeats") != configured {
						assert.Empty(t, stored)
						continue
					}
					require.Len(t, stored, 1)
					assert.Equal(t, h.AISession, stored[0].AISession)
					assert.Equal(t, h.AIInputTokens, stored[0].AIInputTokens)
					assert.Equal(t, h.AIPromptLength, stored[0].AIPromptLength)
					assert.Equal(t, h.Time, stored[0].Time)
					assert.Equal(t, h.Entity, stored[0].Entity)
					require.NotNil(t, stored[0].Project)
					assert.Equal(t, "app", *stored[0].Project)
				}
				return nil
			}))
		})
	}
}
