package ai

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wakatime/wakatime-cli/pkg/heartbeat"
)

func TestReplaceAppHeartbeatsPreservesMetricsAndTimestamps(t *testing.T) {
	input := []heartbeat.Heartbeat{
		{
			Entity:         "Codex session",
			EntityType:     heartbeat.AppType,
			AISession:      "session-1",
			AILineChanges:  heartbeat.PointerTo(2),
			AIInputTokens:  10,
			AIOutputTokens: 4,
			AIPromptLength: 20,
			Category:       "ai coding",
			Time:           100,
			UserAgent:      "Codex/1.0.0",
		},
		{
			Entity:         "/tmp/main.go",
			EntityType:     heartbeat.FileType,
			AISession:      "session-1",
			AILineChanges:  heartbeat.PointerTo(3),
			AIInputTokens:  7,
			AIOutputTokens: 5,
			Category:       "ai coding",
			Time:           130,
			UserAgent:      "Codex/1.0.0",
		},
		{
			Entity:         "Codex session",
			EntityType:     heartbeat.AppType,
			AISession:      "session-1",
			AILineChanges:  heartbeat.PointerTo(6),
			AIInputTokens:  1,
			AIOutputTokens: 2,
			Category:       "ai coding",
			Time:           150,
			UserAgent:      "Codex/1.0.0",
		},
		{
			Entity:         "/tmp/main.go",
			EntityType:     heartbeat.FileType,
			AISession:      "session-1",
			AIInputTokens:  8,
			AIOutputTokens: 9,
			Category:       "ai coding",
			Time:           191,
			UserAgent:      "Codex/1.0.0",
		},
	}
	expected := append([]heartbeat.Heartbeat(nil), input...)
	for i := range expected {
		expected[i].Entity = "/tmp/main.go"
		expected[i].EntityType = heartbeat.FileType
	}

	got := replaceAppHeartbeats(input)

	assert.Equal(t, expected, got)
}

func TestRemoveNoopHeartbeatsDoesNotLeakEntityAcrossSessions(t *testing.T) {
	got := replaceAppHeartbeats([]heartbeat.Heartbeat{
		{
			Entity:     "/repo-a/main.go",
			EntityType: heartbeat.FileType,
			AISession:  "session-a",
			Category:   "ai coding",
			Time:       100,
			UserAgent:  "Claude/1.0.0",
		},
		{
			Entity:     "Claude session",
			EntityType: heartbeat.AppType,
			AISession:  "session-b",
			Category:   "ai coding",
			Time:       110,
			UserAgent:  "Claude/1.0.0",
		},
		{
			Entity:     "/repo-b/main.go",
			EntityType: heartbeat.FileType,
			AISession:  "session-b",
			Category:   "ai coding",
			Time:       1000,
			UserAgent:  "Claude/1.0.0",
		},
		{
			Entity:     "Claude session",
			EntityType: heartbeat.AppType,
			AISession:  "session-c",
			Category:   "ai coding",
			Time:       200,
			UserAgent:  "Claude/1.0.0",
		},
	})

	require.Len(t, got, 4)

	assert.Equal(t, "/repo-a/main.go", got[0].Entity)

	assert.Equal(t, "/repo-b/main.go", got[1].Entity)
	assert.Equal(t, heartbeat.FileType, got[1].EntityType)

	assert.Equal(t, "/repo-b/main.go", got[2].Entity)

	assert.Equal(t, "Claude session", got[3].Entity)
	assert.Equal(t, heartbeat.AppType, got[3].EntityType)
}

func TestRemoveNoopHeartbeatsKeepsDifferentIdentityHeartbeats(t *testing.T) {
	got := replaceAppHeartbeats([]heartbeat.Heartbeat{
		{
			Entity:     "/tmp/main.go",
			EntityType: heartbeat.FileType,
			AISession:  "session-1",
			Category:   "ai coding",
			Time:       100,
			UserAgent:  "Codex/1.0.0",
		},
		{
			Entity:     "/tmp/main.go",
			EntityType: heartbeat.FileType,
			AISession:  "session-1",
			Category:   "debugging",
			Time:       130,
			UserAgent:  "Codex/1.0.0",
		},
	})

	require.Len(t, got, 2)
	assert.Equal(t, "ai coding", got[0].Category)
	assert.Equal(t, "debugging", got[1].Category)
}
