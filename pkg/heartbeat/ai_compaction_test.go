package heartbeat_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wakatime/wakatime-cli/pkg/heartbeat"
)

func TestCompactAIHeartbeatsPreservesMetricsAndActivityBoundaries(t *testing.T) {
	start := float64(time.Date(2026, 1, 2, 23, 55, 0, 0, time.UTC).Unix())
	first := compactionHeartbeat(start+5, 3)
	first.AICachedInputTokens = 5
	first.AIOutputTokens = 7
	first.AILineChanges = heartbeat.PointerTo(2)
	first.HumanLineChanges = heartbeat.PointerTo(1)
	middle := compactionHeartbeat(start+60, 7)
	middle.AICachedInputTokens = 11
	middle.AIOutputTokens = 13
	middle.AILineChanges = heartbeat.PointerTo(3)
	middle.HumanLineChanges = heartbeat.PointerTo(2)
	last := compactionHeartbeat(start+298, 11)
	last.AICachedInputTokens = 17
	last.AIOutputTokens = 19
	last.AILineChanges = heartbeat.PointerTo(5)
	last.HumanLineChanges = heartbeat.PointerTo(4)
	prompt := compactionHeartbeat(start+120, 23)
	prompt.AIPromptLength = 10
	otherPrompt := compactionHeartbeat(start+180, 29)
	otherPrompt.AIPromptLength = 20
	nextDay := compactionHeartbeat(start+300, 31)
	nextDay.AILineChanges = heartbeat.PointerTo(13)
	nextDay.HumanLineChanges = heartbeat.PointerTo(17)
	emptySession := compactionHeartbeat(start+100, 37)
	emptySession.AISession = ""
	otherEmptySession := emptySession
	otherEmptySession.Time = start + 105
	otherEmptySession.AIInputTokens = 41
	human := compactionHeartbeat(start+110, 0)
	human.Category = heartbeat.CodingCategory.String()
	human.HumanLineChanges = heartbeat.PointerTo(41)
	input := []heartbeat.Heartbeat{last, prompt, first, nextDay, emptySession, middle, otherPrompt, human, otherEmptySession}
	before, err := json.Marshal(input)
	require.NoError(t, err)

	got := heartbeat.CompactAIHeartbeats(input)

	first.AILineChanges = nil
	first.HumanLineChanges = nil
	first.AIInputTokens = 0
	first.AICachedInputTokens = 0
	first.AIOutputTokens = 0
	last.AILineChanges = heartbeat.PointerTo(10)
	last.HumanLineChanges = heartbeat.PointerTo(7)
	last.AIInputTokens = 21
	last.AICachedInputTokens = 33
	last.AIOutputTokens = 39
	expected := []heartbeat.Heartbeat{first, emptySession, otherEmptySession, human, prompt, otherPrompt, last, nextDay}
	assert.Equal(t, expected, got)

	after, err := json.Marshal(input)
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after))
}

func TestCompactAIHeartbeatsSeparatesWireIdentity(t *testing.T) {
	tests := map[string]func(*heartbeat.Heartbeat){
		"session":             func(h *heartbeat.Heartbeat) { h.AISession = "session-b" },
		"model":               func(h *heartbeat.Heartbeat) { h.UserAgent = "model-b/1 plugin/1" },
		"full user agent":     func(h *heartbeat.Heartbeat) { h.UserAgent = "model-a/1 plugin/2" },
		"subscription":        func(h *heartbeat.Heartbeat) { h.AISubscriptionPlan = "team" },
		"project":             func(h *heartbeat.Heartbeat) { h.Project = heartbeat.PointerTo("other-app") },
		"branch":              func(h *heartbeat.Heartbeat) { h.Branch = heartbeat.PointerTo("feature") },
		"entity":              func(h *heartbeat.Heartbeat) { h.Entity = "/home/u/src/app/lib.go" },
		"write":               func(h *heartbeat.Heartbeat) { h.IsWrite = heartbeat.PointerTo(true) },
		"language":            func(h *heartbeat.Heartbeat) { h.Language = heartbeat.PointerTo("Python") },
		"line metadata":       func(h *heartbeat.Heartbeat) { h.Lines = heartbeat.PointerTo(23) },
		"zero ai changes":     func(h *heartbeat.Heartbeat) { h.AILineChanges = heartbeat.PointerTo(0) },
		"zero human changes":  func(h *heartbeat.Heartbeat) { h.HumanLineChanges = heartbeat.PointerTo(0) },
		"dependency metadata": func(h *heartbeat.Heartbeat) { h.Dependencies = []string{"example"} },
	}

	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			first := compactionHeartbeat(601, 3)
			middle := compactionHeartbeat(602, 7)
			change(&middle)

			last := compactionHeartbeat(603, 11)

			got := heartbeat.CompactAIHeartbeats([]heartbeat.Heartbeat{first, middle, last})

			first.AIInputTokens = 0
			last.AIInputTokens = 14
			assert.Equal(t, []heartbeat.Heartbeat{first, middle, last}, got)
		})
	}
}

func TestCompactAIHeartbeatsDeduplicatesLogicalRouteCopies(t *testing.T) {
	first := compactionHeartbeat(601, 3)
	first.AILineChanges = heartbeat.PointerTo(1)
	first.APIKey = "example-key-a"
	first.APIURL = "https://one.example.com/api/v1"
	copy := first
	copy.APIKey = "example-key-b"
	copy.APIURL = "https://two.example.com/api/v1"
	simultaneous := compactionHeartbeat(601, 5)
	simultaneous.AILineChanges = heartbeat.PointerTo(2)
	last := compactionHeartbeat(603, 7)
	last.AILineChanges = heartbeat.PointerTo(11)
	prompt := compactionHeartbeat(602, 13)
	prompt.AIPromptLength = 17
	promptCopy := prompt
	promptCopy.APIURL = "https://two.example.com/api/v1"

	got := heartbeat.CompactAIHeartbeats([]heartbeat.Heartbeat{first, copy, simultaneous, prompt, promptCopy, last, last})

	first.AIInputTokens = 0
	first.AILineChanges = nil
	last.AIInputTokens = 15
	last.AILineChanges = heartbeat.PointerTo(14)
	expectedJSON, err := json.Marshal([]heartbeat.Heartbeat{first, prompt, last})
	require.NoError(t, err)
	gotJSON, err := json.Marshal(got)
	require.NoError(t, err)
	assert.JSONEq(t, string(expectedJSON), string(gotJSON))
}

func TestCompactAIHeartbeatsPreservesGrossLineAdditionsAndDeletions(t *testing.T) {
	var input []heartbeat.Heartbeat

	for i, changes := range [][2]int{{10, 10}, {-5, 7}, {7, -5}, {-2, -2}} {
		h := compactionHeartbeat(float64(601+i), int64(i+1))
		h.AILineChanges = heartbeat.PointerTo(changes[0])
		h.HumanLineChanges = heartbeat.PointerTo(changes[1])
		input = append(input, h)
	}

	got := heartbeat.CompactAIHeartbeats(input)

	var (
		aiAdded, aiDeleted, humanAdded, humanDeleted int
		tokens                                       int64
	)
	for _, h := range got {
		tokens += h.AIInputTokens
		if h.AILineChanges != nil {
			aiAdded += max(0, *h.AILineChanges)
			aiDeleted -= min(0, *h.AILineChanges)
		}

		if h.HumanLineChanges != nil {
			humanAdded += max(0, *h.HumanLineChanges)
			humanDeleted -= min(0, *h.HumanLineChanges)
		}
	}

	assert.Equal(t, 17, aiAdded)
	assert.Equal(t, 7, aiDeleted)
	assert.Equal(t, 17, humanAdded)
	assert.Equal(t, 7, humanDeleted)
	assert.Equal(t, int64(10), tokens)
}

func compactionHeartbeat(at float64, input int64) heartbeat.Heartbeat {
	return heartbeat.Heartbeat{
		AISession:     "session-a",
		AIInputTokens: input,
		Category:      heartbeat.AICodingCategory.String(),
		Entity:        "/home/u/src/app/main.go",
		EntityType:    heartbeat.FileType,
		Project:       heartbeat.PointerTo("app"),
		Time:          at,
		UserAgent:     "model-a/1 plugin/1",
	}
}
