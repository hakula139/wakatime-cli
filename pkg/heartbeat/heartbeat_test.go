package heartbeat_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/wakatime/wakatime-cli/pkg/heartbeat"
	"github.com/wakatime/wakatime-cli/pkg/version"

	"github.com/matishsiao/goInfo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNew(t *testing.T) {
	h := heartbeat.New(
		heartbeat.PointerTo(5),
		"feature/branch",
		heartbeat.CodingCategory.String(),
		heartbeat.PointerTo(12),
		"testdata/main.go",
		heartbeat.FileType,
		heartbeat.PointerTo(2),
		true,
		heartbeat.PointerTo(true),
		heartbeat.PointerTo("Go"),
		"Golang",
		heartbeat.PointerTo(42),
		heartbeat.PointerTo(100),
		"/path/to/file",
		"billing",
		false,
		"pci",
		"/custom-path",
		1592868313.541149,
		"wakatime/13.0.7",
	)

	assert.True(t, strings.HasSuffix(h.Entity, "testdata/main.go"))

	assert.Equal(t, heartbeat.Heartbeat{
		AILineChanges:       heartbeat.PointerTo(5),
		BranchAlternate:     "feature/branch",
		Category:            heartbeat.CodingCategory.String(),
		CursorPosition:      heartbeat.PointerTo(12),
		EntityType:          heartbeat.FileType,
		HumanLineChanges:    heartbeat.PointerTo(2),
		IsUnsavedEntity:     true,
		IsWrite:             heartbeat.PointerTo(true),
		Language:            heartbeat.PointerTo("Go"),
		LanguageAlternate:   "Golang",
		LineNumber:          heartbeat.PointerTo(42),
		Lines:               heartbeat.PointerTo(100),
		LocalFile:           "/path/to/file",
		ProjectAlternate:    "billing",
		ProjectOverride:     "pci",
		ProjectPathOverride: "/custom-path",
		Time:                1592868313.541149,
		UserAgent:           "wakatime/13.0.7",
		Entity:              h.Entity,
	}, h)
}

func TestNewWithAITokens(t *testing.T) {
	h := heartbeat.NewWithAITokens(
		heartbeat.PointerTo(5),
		"session-1",
		heartbeat.AITokens{
			LastInput:          10,
			LastCachedInput:    3,
			LastOutput:         20,
			CurrentInput:       7,
			CurrentCachedInput: 11,
			CurrentOutput:      25,
		},
		"feature/branch",
		heartbeat.AICodingCategory.String(),
		heartbeat.PointerTo(12),
		"testdata/main.go",
		heartbeat.FileType,
		heartbeat.PointerTo(2),
		false,
		heartbeat.PointerTo(true),
		heartbeat.PointerTo("Go"),
		"Golang",
		heartbeat.PointerTo(42),
		heartbeat.PointerTo(100),
		"/path/to/file",
		"billing",
		true,
		"pci",
		"/custom-path",
		1592868313.541149,
		"wakatime/13.0.7",
	)

	assert.Equal(t, "session-1", h.AISession)
	assert.Zero(t, h.AIInputTokens)
	assert.Equal(t, int64(8), h.AICachedInputTokens)
	assert.Equal(t, int64(5), h.AIOutputTokens)
	assert.Equal(t, heartbeat.AICodingCategory.String(), h.Category)
	assert.Equal(t, "testdata/main.go", h.Entity)
	assert.True(t, h.ProjectFromGitRemote)
}

func TestHeartbeat_ID(t *testing.T) {
	h := heartbeat.Heartbeat{
		Branch:         heartbeat.PointerTo("heartbeat"),
		Category:       heartbeat.CodingCategory.String(),
		CursorPosition: heartbeat.PointerTo(42),
		Entity:         "/tmp/main.go",
		EntityType:     heartbeat.FileType,
		IsWrite:        heartbeat.PointerTo(true),
		Project:        heartbeat.PointerTo("wakatime"),
		Time:           1592868313.541149,
	}
	assert.Equal(t, "1592868313.541149-42-file-coding-wakatime-heartbeat-/tmp/main.go-true", h.ID())
}

func TestHeartbeat_ID_NilFields(t *testing.T) {
	h := heartbeat.Heartbeat{
		Entity:     "/tmp/main.go",
		EntityType: heartbeat.FileType,
		Time:       1592868313.541149,
	}
	assert.Equal(t, "1592868313.541149-nil-file-undefined-unset-unset-/tmp/main.go-false", h.ID())
}

func TestHeartbeat_ID_AICodingDistinguishesWireData(t *testing.T) {
	tests := map[string]func(*heartbeat.Heartbeat){
		"session":             func(h *heartbeat.Heartbeat) { h.AISession = "session-b" },
		"model":               func(h *heartbeat.Heartbeat) { h.UserAgent = "model-b/1 plugin-a/1" },
		"full user agent":     func(h *heartbeat.Heartbeat) { h.UserAgent = "model-a/1 plugin-b/1" },
		"subscription":        func(h *heartbeat.Heartbeat) { h.AISubscriptionPlan = "team" },
		"input tokens":        func(h *heartbeat.Heartbeat) { h.AIInputTokens++ },
		"cached input tokens": func(h *heartbeat.Heartbeat) { h.AICachedInputTokens++ },
		"output tokens":       func(h *heartbeat.Heartbeat) { h.AIOutputTokens++ },
		"prompt length":       func(h *heartbeat.Heartbeat) { h.AIPromptLength++ },
		"ai line changes":     func(h *heartbeat.Heartbeat) { h.AILineChanges = heartbeat.PointerTo(3) },
		"human line changes":  func(h *heartbeat.Heartbeat) { h.HumanLineChanges = heartbeat.PointerTo(5) },
		"ai line presence":    func(h *heartbeat.Heartbeat) { h.AILineChanges = nil },
		"human line presence": func(h *heartbeat.Heartbeat) { h.HumanLineChanges = nil },
		"project":             func(h *heartbeat.Heartbeat) { h.Project = heartbeat.PointerTo("other-app") },
		"submicrosecond time": func(h *heartbeat.Heartbeat) { h.Time += 0.0000001 },
	}

	for _, session := range []string{"session-a", ""} {
		for name, change := range tests {
			t.Run(fmt.Sprintf("session=%q/%s", session, name), func(t *testing.T) {
				original := heartbeat.Heartbeat{
					AILineChanges:       heartbeat.PointerTo(0),
					AICachedInputTokens: 11,
					AISession:           session,
					AISubscriptionPlan:  "individual",
					AIInputTokens:       13,
					AIOutputTokens:      17,
					AIPromptLength:      19,
					Branch:              heartbeat.PointerTo("main"),
					Category:            heartbeat.AICodingCategory.String(),
					Entity:              "/home/u/src/app/main.go",
					EntityType:          heartbeat.FileType,
					HumanLineChanges:    heartbeat.PointerTo(0),
					Language:            heartbeat.PointerTo("Go"),
					Project:             heartbeat.PointerTo("app"),
					Time:                100.25,
					UserAgent:           "model-a/1 plugin-a/1",
				}
				changed := original
				change(&changed)

				assert.NotEqual(t, original.ID(), changed.ID())
			})
		}
	}
}

func TestHeartbeat_ID_AICodingUsesLogicalWireData(t *testing.T) {
	original := heartbeat.Heartbeat{
		AILineChanges:       heartbeat.PointerTo(0),
		AISession:           "session-a",
		AIInputTokens:       13,
		Category:            heartbeat.AICodingCategory.String(),
		Entity:              "/home/u/src/app/main.go",
		EntityType:          heartbeat.FileType,
		HumanLineChanges:    heartbeat.PointerTo(0),
		Project:             heartbeat.PointerTo("app"),
		ProjectPathOverride: "/home/u/src/app",
		Time:                100.25,
		UserAgent:           "model-a/1 plugin-a/1",
	}
	routed := original
	routed.APIKey = "example-key"
	routed.APIURL = "https://example.com/api/v1"

	encoded, err := json.Marshal(routed)
	require.NoError(t, err)

	var decoded heartbeat.Heartbeat
	require.NoError(t, json.Unmarshal(encoded, &decoded))

	assert.Equal(t, original.ID(), routed.ID())
	assert.Equal(t, original.ID(), decoded.ID())
	assert.Equal(t, original.AILineChanges, decoded.AILineChanges)
	assert.Equal(t, original.HumanLineChanges, decoded.HumanLineChanges)
}

func TestHeartbeat_JSON(t *testing.T) {
	h := heartbeat.Heartbeat{
		AILineChanges:      heartbeat.PointerTo(5),
		AISubscriptionPlan: "plus",
		Branch:             heartbeat.PointerTo("heartbeat"),
		Category:           heartbeat.DebuggingCategory.String(),
		CursorPosition:     heartbeat.PointerTo(12),
		Dependencies:       []string{"dep1", "dep2"},
		Entity:             "/tmp/main.go",
		EntityType:         heartbeat.FileType,
		HumanLineChanges:   heartbeat.PointerTo(2),
		IsWrite:            heartbeat.PointerTo(true),
		Language:           heartbeat.PointerTo("Go"),
		LineNumber:         heartbeat.PointerTo(42),
		Lines:              heartbeat.PointerTo(100),
		Project:            heartbeat.PointerTo("wakatime"),
		Time:               1585598060.1,
		UserAgent:          "wakatime/13.0.7",
	}

	jsonEncoded, err := json.Marshal(h)
	require.NoError(t, err)

	f, err := os.Open("testdata/heartbeat.json")
	require.NoError(t, err)

	defer f.Close()

	expected, err := io.ReadAll(f)
	require.NoError(t, err)

	assert.JSONEq(t, string(expected), string(jsonEncoded))
}

func TestHeartbeat_JSON_NilFields(t *testing.T) {
	h := heartbeat.Heartbeat{
		Entity:     "/tmp/main.go",
		EntityType: heartbeat.FileType,
		Time:       1585598060,
		UserAgent:  "wakatime/13.0.7",
	}

	jsonEncoded, err := json.Marshal(h)
	require.NoError(t, err)

	f, err := os.Open("testdata/heartbeat_null_fields.json")
	require.NoError(t, err)

	defer f.Close()

	expected, err := io.ReadAll(f)
	require.NoError(t, err)

	assert.JSONEq(t, string(expected), string(jsonEncoded))
}

func TestNewHandle(t *testing.T) {
	sender := mockSender{
		SendHeartbeatsFn: func(_ context.Context, hh []heartbeat.Heartbeat) ([]heartbeat.Result, error) {
			assert.Equal(t, []heartbeat.Heartbeat{
				{
					Branch:     heartbeat.PointerTo("test"),
					Entity:     "/tmp/main.go",
					EntityType: heartbeat.FileType,
					Time:       1585598060,
					UserAgent:  "wakatime/13.0.7",
				},
			}, hh)

			return []heartbeat.Result{
				{
					Status: 201,
					ID:     "D45A3607-CCC6-450E-AC05-7B73E5CC9023",
				},
			}, nil
		},
	}

	opts := []heartbeat.HandleOption{
		func(next heartbeat.Handle) heartbeat.Handle {
			return func(ctx context.Context, hh []heartbeat.Heartbeat) ([]heartbeat.Result, error) {
				for i := range hh {
					hh[i].Branch = heartbeat.PointerTo("test")
				}

				return next(ctx, hh)
			}
		},
	}

	handle := heartbeat.NewHandle(&sender, opts...)
	_, err := handle(t.Context(), []heartbeat.Heartbeat{
		{
			Entity:     "/tmp/main.go",
			EntityType: heartbeat.FileType,
			Time:       1585598060,
			UserAgent:  "wakatime/13.0.7",
		},
	})
	require.NoError(t, err)
}

func TestUserAgentUnknownPlugin(t *testing.T) {
	info, err := goInfo.GetInfo()
	require.NoError(t, err)

	expected := fmt.Sprintf(
		"wakatime/%s (%s-%s-%s) %s Unknown/0",
		version.Version,
		runtime.GOOS,
		info.Core,
		info.Platform,
		runtime.Version(),
	)

	assert.Equal(t, expected, heartbeat.UserAgent(t.Context(), ""))
}

func TestUserAgent(t *testing.T) {
	info, err := goInfo.GetInfo()
	require.NoError(t, err)

	expected := fmt.Sprintf(
		"wakatime/%s (%s-%s-%s) %s testplugin",
		version.Version,
		runtime.GOOS,
		info.Core,
		info.Platform,
		runtime.Version(),
	)

	assert.Equal(t, expected, heartbeat.UserAgent(t.Context(), "testplugin"))
}

func TestUserAgentMissingPluginVersion(t *testing.T) {
	info, err := goInfo.GetInfo()
	require.NoError(t, err)

	expected := fmt.Sprintf(
		"wakatime/%s (%s-%s-%s) %s Claude/unknown macos-wakatime/5.28.3",
		version.Version,
		runtime.GOOS,
		info.Core,
		info.Platform,
		runtime.Version(),
	)

	assert.Equal(
		t,
		expected,
		heartbeat.UserAgent(t.Context(), "Claude/ macos-wakatime/5.28.3"),
	)
}

func TestRemoteAddressRegex(t *testing.T) {
	tests := map[string]struct {
		Heartbeat heartbeat.Heartbeat
		Expected  bool
	}{
		"ssh full path": {
			Heartbeat: heartbeat.Heartbeat{Entity: "ssh://user:1234@192.168.1.2/home/pi/unicorn-hat/examples/ascii_pic.py"},
			Expected:  true,
		},
		"sftp full path": {
			Heartbeat: heartbeat.Heartbeat{Entity: "sftp://user:1234@192.168.1.2/home/pi/unicorn-hat/examples/ascii_pic.py"},
			Expected:  true,
		},
		"without path": {
			Heartbeat: heartbeat.Heartbeat{Entity: "ssh://user:1234@192.168.1.2"},
			Expected:  true,
		},
		"invalid ftp": {
			Heartbeat: heartbeat.Heartbeat{Entity: "ftp://user:1234@192.168.1.2"},
			Expected:  false,
		},
		"invalid": {
			Heartbeat: heartbeat.Heartbeat{Entity: "http://192.168.1.2"},
			Expected:  false,
		},
		"non-file": {
			Heartbeat: heartbeat.Heartbeat{Entity: "ssh://user@192.168.1.2/main.go", EntityType: heartbeat.AppType},
			Expected:  false,
		},
		"unsaved file": {
			Heartbeat: heartbeat.Heartbeat{
				Entity:          "ssh://user@192.168.1.2/main.go",
				EntityType:      heartbeat.FileType,
				IsUnsavedEntity: true,
			},
			Expected: false,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			result := test.Heartbeat.IsRemote()

			assert.Equal(t, test.Expected, result)
		})
	}
}

type mockSender struct {
	SendHeartbeatsFn        func(context.Context, []heartbeat.Heartbeat) ([]heartbeat.Result, error)
	SendHeartbeatsFnInvoked bool
}

func (m *mockSender) SendHeartbeats(ctx context.Context, hh []heartbeat.Heartbeat) ([]heartbeat.Result, error) {
	m.SendHeartbeatsFnInvoked = true
	return m.SendHeartbeatsFn(ctx, hh)
}
