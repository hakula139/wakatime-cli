package heartbeat

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wakatime/wakatime-cli/pkg/api"
	heartbeatpkg "github.com/wakatime/wakatime-cli/pkg/heartbeat"
	offlinepkg "github.com/wakatime/wakatime-cli/pkg/offline"
	paramspkg "github.com/wakatime/wakatime-cli/pkg/params"
)

func TestLoadParamsInternalBranches(t *testing.T) {
	_, err := loadParams(t.Context(), nil, paramspkg.FlagReadOrderFlagPrecedence)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "viper instance unset")

	v := viper.New()
	v.Set("key", "00000000-0000-4000-8000-000000000000")

	_, err = loadParams(t.Context(), v, paramspkg.FlagReadOrderFlagPrecedence)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to load heartbeat params")

	_, err = loadAISyncParams(t.Context(), viper.New(), paramspkg.FlagReadOrderFlagPrecedence)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to load API params")
}

func TestApplyAIParsingDisabledPassesThrough(t *testing.T) {
	input := []heartbeatpkg.Heartbeat{{
		Entity:     "Terminal",
		EntityType: heartbeatpkg.AppType,
		Time:       1,
		UserAgent:  "plugin/0.0.1",
	}}

	got, err := applyAIParsing(t.Context(), viper.New(), paramspkg.Params{
		AI:  paramspkg.AIParams{SyncDisabled: true},
		API: paramspkg.API{Plugin: "plugin/0.0.1"},
	}, input)

	require.NoError(t, err)
	assert.Equal(t, input, got)
}

func TestRunAISyncActivityNoActivity(t *testing.T) {
	paramspkg.Once = sync.Once{}

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	v := viper.New()
	v.Set("key", "00000000-0000-4000-8000-000000000000")
	v.Set("offline-queue-file", "~missing-user/offline.bdb")

	code, err := RunAISyncActivity(t.Context(), v)

	require.NoError(t, err)
	assert.Equal(t, 0, code)
}

func TestRunAISyncActivityLoadParamsError(t *testing.T) {
	paramspkg.Once = sync.Once{}

	t.Setenv("WAKATIME_API_KEY", "")

	code, err := RunAISyncActivity(t.Context(), viper.New())

	require.Error(t, err)
	assert.Equal(t, 104, code)
	assert.Contains(t, err.Error(), "failed to load command params")
}

func TestSendPreparedHeartbeatsRateLimitedSavesOffline(t *testing.T) {
	queueFile := filepath.Join(t.TempDir(), "offline.bdb")
	params := internalCommandParams()
	params.Offline.LastSentAt = time.Now()
	params.Offline.RateLimit = time.Hour

	err := sendPreparedHeartbeats(
		t.Context(),
		viper.New(),
		params,
		queueFile,
		[]heartbeatpkg.Heartbeat{internalAppHeartbeat()},
		loadParams,
	)

	require.NoError(t, err)

	count, err := offlinepkg.CountHeartbeats(t.Context(), queueFile)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

func TestSendPreparedHeartbeatsBuildHandleErrorSavesOffline(t *testing.T) {
	queueFile := filepath.Join(t.TempDir(), "offline.bdb")
	params := internalCommandParams()
	params.API.SSLCertFilepath = filepath.Join(t.TempDir(), "missing.pem")

	err := sendPreparedHeartbeats(
		t.Context(),
		viper.New(),
		params,
		queueFile,
		[]heartbeatpkg.Heartbeat{internalAppHeartbeat()},
		loadParams,
	)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to initialize api client")

	count, countErr := offlinepkg.CountHeartbeats(t.Context(), queueFile)
	require.NoError(t, countErr)
	assert.Equal(t, 1, count)
}

func TestSendPreparedHeartbeatsBuildHandleErrorSavesProjectConfiguredOverflow(t *testing.T) {
	queueFile := filepath.Join(t.TempDir(), "offline.bdb")
	params := internalCommandParams()
	params.API.SSLCertFilepath = filepath.Join(t.TempDir(), "missing.pem")

	heartbeats := internalAppHeartbeats(offlinepkg.SendLimit + 1)
	extra := internalProjectHeartbeat(t, []byte("[projectmap]\nmain[.]go = example\n"))
	heartbeats[offlinepkg.SendLimit] = extra
	v := viper.New()
	v.Set("key", "00000000-0000-4000-8000-000000000000")

	err := sendPreparedHeartbeats(
		t.Context(),
		v,
		params,
		queueFile,
		heartbeats,
		loadAISyncParams,
	)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to initialize api client")

	stored, readErr := offlinepkg.ReadHeartbeats(t.Context(), queueFile, len(heartbeats)+1)
	require.NoError(t, readErr)
	require.Len(t, stored, len(heartbeats))
	assert.Equal(t, filepath.ToSlash(extra.Entity), stored[offlinepkg.SendLimit].Entity)
	assert.Equal(t, heartbeatpkg.PointerTo("example"), stored[offlinepkg.SendLimit].Project)
}

func TestSendPreparedHeartbeatsWithoutOfflineSendsEveryBatch(t *testing.T) {
	queueFile := filepath.Join(t.TempDir(), "offline.bdb")
	params := internalCommandParams()
	params.Offline.Disabled = true
	v := viper.New()
	v.Set("internal-config", filepath.Join(t.TempDir(), "internal.cfg"))

	var batches [][]float64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var sent []heartbeatpkg.Heartbeat
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&sent)) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		timestamps := make([]float64, len(sent))
		for i, h := range sent {
			timestamps[i] = h.Time
		}
		batches = append(batches, timestamps)
		writeHeartbeatResults(t, w, len(sent))
	}))
	defer server.Close()

	params.API.URL = server.URL
	heartbeats := internalAppHeartbeats(2*offlinepkg.SendLimit + 1)

	err := sendPreparedHeartbeats(t.Context(), v, params, queueFile, heartbeats, loadParams)
	require.NoError(t, err)
	server.Close()

	assert.Equal(t, [][]float64{
		{1, 2, 3, 4, 5, 6, 7, 8, 9, 10},
		{11, 12, 13, 14, 15, 16, 17, 18, 19, 20},
		{21},
	}, batches)
	assert.NoFileExists(t, queueFile)
}

func TestSendPreparedHeartbeatsReportsOverflowSaveFailure(t *testing.T) {
	tests := []struct {
		name   string
		status int
	}{
		{name: "foreground succeeds", status: http.StatusCreated},
		{name: "foreground authentication fails", status: http.StatusUnauthorized},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			queueFile := filepath.Join(t.TempDir(), "offline.bdb")
			params := internalCommandParams()
			v := viper.New()
			v.Set("internal-config", filepath.Join(t.TempDir(), "internal.cfg"))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tt.status == http.StatusCreated {
					writeHeartbeatResults(t, w, offlinepkg.SendLimit)
					return
				}

				w.WriteHeader(tt.status)
			}))
			defer server.Close()

			params.API.URL = server.URL
			heartbeats := internalAppHeartbeats(offlinepkg.SendLimit + 1)
			heartbeats[offlinepkg.SendLimit] = internalProjectHeartbeat(t, []byte("\"bad = true\n"))

			err := sendPreparedHeartbeats(t.Context(), v, params, queueFile, heartbeats, loadAISyncParams)

			require.ErrorContains(t, err, "failed to save extra heartbeats to offline queue")
			assert.ErrorContains(t, err, "failed to load project-level configuration file")
			if tt.status == http.StatusUnauthorized {
				var authErr api.ErrAuth
				assert.ErrorAs(t, err, &authErr)
			}
		})
	}
}

func writeHeartbeatResults(t *testing.T, w http.ResponseWriter, count int) {
	t.Helper()

	responses := make([]json.RawMessage, count)
	for i := range responses {
		responses[i] = json.RawMessage(`[{"data":{"id":"example"}},201]`)
	}

	w.WriteHeader(http.StatusCreated)
	assert.NoError(t, json.NewEncoder(w).Encode(map[string][]json.RawMessage{"responses": responses}))
}

func internalCommandParams() paramspkg.Params {
	return paramspkg.Params{
		API: paramspkg.API{
			Plugin:  "plugin/0.0.1",
			Timeout: time.Second,
			URL:     "http://127.0.0.1",
		},
		Heartbeat: paramspkg.Heartbeat{
			Category:   heartbeatpkg.CodingCategory,
			Entity:     "Terminal",
			EntityType: heartbeatpkg.AppType,
			Time:       1,
		},
	}
}

func internalAppHeartbeat() heartbeatpkg.Heartbeat {
	return heartbeatpkg.Heartbeat{
		Entity:     "Terminal",
		EntityType: heartbeatpkg.AppType,
		Time:       1,
		UserAgent:  "plugin/0.0.1",
	}
}

func internalAppHeartbeats(count int) []heartbeatpkg.Heartbeat {
	heartbeats := make([]heartbeatpkg.Heartbeat, count)
	for i := range heartbeats {
		heartbeats[i] = internalAppHeartbeat()
		heartbeats[i].Time = float64(i + 1)
	}

	return heartbeats
}

func internalProjectHeartbeat(t *testing.T, config []byte) heartbeatpkg.Heartbeat {
	t.Helper()

	project := t.TempDir()
	path := filepath.Join(project, "main.go")
	require.NoError(t, os.WriteFile(path, []byte("package main\n"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(project, ".wakatime"), config, 0600))

	h := internalAppHeartbeat()
	h.AISession = "session"
	h.Category = heartbeatpkg.AICodingCategory.String()
	h.Entity = path
	h.EntityType = heartbeatpkg.FileType
	h.Time = float64(offlinepkg.SendLimit + 1)

	return h
}
