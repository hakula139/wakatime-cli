package heartbeat

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wakatime/wakatime-cli/cmd/offlinesync"
	"github.com/wakatime/wakatime-cli/pkg/api"
	"github.com/wakatime/wakatime-cli/pkg/exitcode"
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
	v.Set("api-url", params.API.URL)
	params.API.Key = v.GetString("key")
	v.Set("project_api_key.unrelated", "00000000-0000-4000-8000-000000000001")
	apiParams, err := paramspkg.LoadAPIParams(t.Context(), v, paramspkg.FlagReadOrderFlagPrecedence)
	require.NoError(t, err)

	params.API.KeyPatterns = apiParams.KeyPatterns

	err = sendPreparedHeartbeats(
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

		writeHeartbeatResults(t, w, len(sent), http.StatusCreated)
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

func TestSendPreparedHeartbeatsWithoutOfflineChecksResults(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		incomplete bool
		wantError  string
	}{
		{name: "retryable rejection", status: http.StatusTooManyRequests, wantError: "status 429"},
		{name: "missing receipt", status: http.StatusCreated, incomplete: true, wantError: "received 9 results"},
		{name: "permanent rejection", status: http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			queueFile := filepath.Join(t.TempDir(), "offline.bdb")
			params := internalCommandParams()
			params.Offline.Disabled = true
			v := viper.New()
			v.Set("internal-config", filepath.Join(t.TempDir(), "internal.cfg"))

			var calls atomic.Int32

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var sent []heartbeatpkg.Heartbeat
				if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&sent)) {
					w.WriteHeader(http.StatusBadRequest)
					return
				}

				if calls.Add(1) == 1 {
					count := len(sent)
					if tt.incomplete {
						count--
					}

					writeHeartbeatResults(t, w, count, tt.status)

					return
				}

				writeHeartbeatResults(t, w, len(sent), http.StatusCreated)
			}))
			defer server.Close()

			params.API.URL = server.URL
			heartbeats := internalAppHeartbeats(offlinepkg.SendLimit + 1)

			err := sendPreparedHeartbeats(t.Context(), v, params, queueFile, heartbeats, loadParams)

			if tt.wantError != "" {
				var apiErr api.Err
				assert.ErrorAs(t, err, &apiErr)
				assert.ErrorContains(t, err, tt.wantError)
				assert.EqualValues(t, 1, calls.Load())
			} else {
				require.NoError(t, err)
				assert.EqualValues(t, 2, calls.Load())
			}

			assert.NoFileExists(t, queueFile)
		})
	}
}

func TestValidateDirectResultsRejectsUnassociatedReceipt(t *testing.T) {
	handle := validateDirectResults(func(_ context.Context, _ []heartbeatpkg.Heartbeat) ([]heartbeatpkg.Result, error) {
		return []heartbeatpkg.Result{{Status: http.StatusCreated}}, nil
	})

	_, err := handle(t.Context(), []heartbeatpkg.Heartbeat{internalAppHeartbeat()})
	assert.ErrorContains(t, err, "received a result without a matching heartbeat")
}

func TestValidateDirectResultsPreservesTransportError(t *testing.T) {
	handle := validateDirectResults(func(_ context.Context, _ []heartbeatpkg.Heartbeat) ([]heartbeatpkg.Result, error) {
		return nil, assert.AnError
	})

	_, err := handle(t.Context(), []heartbeatpkg.Heartbeat{internalAppHeartbeat()})
	assert.ErrorIs(t, err, assert.AnError)
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
					writeHeartbeatResults(t, w, offlinepkg.SendLimit, http.StatusCreated)
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

func TestSendPreparedHeartbeatsDoesNotQueueUnreplayableProjectRoutes(t *testing.T) {
	const (
		defaultKey       = "00000000-0000-4000-8000-000000000000"
		projectKey       = "00000000-0000-4000-8000-000000000001"
		projectTimestamp = offlinepkg.SendLimit + 1
	)

	var defaultCount, projectCount, projectStatus atomic.Int32

	defaultAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Basic "+base64.StdEncoding.EncodeToString([]byte(defaultKey)), r.Header.Get("Authorization"))

		var sent []heartbeatpkg.Heartbeat
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&sent)) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		for _, h := range sent {
			assert.NotEqualValues(t, projectTimestamp, h.Time)
		}

		defaultCount.Add(int32(len(sent)))
		writeHeartbeatResults(t, w, len(sent), http.StatusCreated)
	}))
	defer defaultAPI.Close()

	projectAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Basic "+base64.StdEncoding.EncodeToString([]byte(projectKey)), r.Header.Get("Authorization"))

		var sent []heartbeatpkg.Heartbeat
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&sent)) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		for _, h := range sent {
			assert.EqualValues(t, projectTimestamp, h.Time)
			assert.Equal(t, "HIDDEN.go", h.Entity)
		}

		projectCount.Add(int32(len(sent)))

		switch status := int(projectStatus.Load()); status {
		case http.StatusNotFound:
			w.WriteHeader(status)
		case http.StatusTooManyRequests:
			writeHeartbeatResults(t, w, len(sent), status)
		default:
			writeHeartbeatResults(t, w, len(sent), http.StatusCreated)
		}
	}))
	defer projectAPI.Close()

	queueFile := filepath.Join(t.TempDir(), "offline.bdb")
	v := viper.New()
	v.Set("internal-config", filepath.Join(t.TempDir(), "internal.cfg"))
	v.Set("settings.api_url", defaultAPI.URL)
	v.Set("settings.api_key", defaultKey)

	loaded, err := loadAISyncParams(t.Context(), v, paramspkg.FlagReadOrderFlagPrecedence)
	require.NoError(t, err)

	config := fmt.Sprintf(
		"[settings]\napi_url = %s\napi_key = %s\nhide_file_names = true\n",
		projectAPI.URL,
		projectKey,
	)
	extra := internalProjectHeartbeat(t, []byte(config))
	heartbeats := internalAppHeartbeats(offlinepkg.SendLimit + 2)
	heartbeats[offlinepkg.SendLimit] = extra

	err = sendPreparedHeartbeats(t.Context(), v, loaded, queueFile, heartbeats, loadAISyncParams)
	require.ErrorContains(t, err, "offline queue cannot preserve API routing")

	assert.EqualValues(t, offlinepkg.SendLimit, defaultCount.Load())
	require.NoError(t, offlinesync.SyncOfflineActivity(t.Context(), v, queueFile))
	assert.EqualValues(t, offlinepkg.SendLimit+1, defaultCount.Load())
	assert.Zero(t, projectCount.Load())

	loaded.Offline.Disabled = true
	err = sendPreparedHeartbeats(t.Context(), v, loaded, queueFile, []heartbeatpkg.Heartbeat{extra}, loadAISyncParams)
	require.NoError(t, err)
	assert.EqualValues(t, offlinepkg.SendLimit+1, defaultCount.Load())
	assert.EqualValues(t, 1, projectCount.Load())

	loaded.Offline.Disabled = false

	for _, status := range []int{http.StatusNotFound, http.StatusTooManyRequests} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			projectStatus.Store(int32(status))
			projectCount.Store(0)

			err := sendPreparedHeartbeats(
				t.Context(), v, loaded, queueFile, []heartbeatpkg.Heartbeat{extra}, loadAISyncParams,
			)
			require.ErrorContains(t, err, "offline queue cannot preserve API routing")

			if status == http.StatusNotFound {
				var apiErr api.Err
				assert.ErrorAs(t, err, &apiErr)
			}

			require.NoError(t, offlinesync.SyncOfflineActivity(t.Context(), v, queueFile))
			assert.EqualValues(t, offlinepkg.SendLimit+1, defaultCount.Load())
			assert.EqualValues(t, 1, projectCount.Load())
		})
	}

	t.Run("rate limited", func(t *testing.T) {
		projectCount.Store(0)

		loaded.Offline.LastSentAt = time.Now()
		loaded.Offline.RateLimit = time.Hour
		batch := []heartbeatpkg.Heartbeat{extra, heartbeats[0]}

		err := sendPreparedHeartbeats(t.Context(), v, loaded, queueFile, batch, loadAISyncParams)
		require.ErrorIs(t, err, offlinepkg.ErrUnreplayableRoute)

		assert.Zero(t, projectCount.Load())
		assert.EqualValues(t, offlinepkg.SendLimit+1, defaultCount.Load())

		require.NoError(t, offlinesync.SyncOfflineActivity(t.Context(), v, queueFile))
		assert.EqualValues(t, offlinepkg.SendLimit+2, defaultCount.Load())
	})
}

func TestOfflineHeartbeatMirrorRouting(t *testing.T) {
	const (
		primaryKey     = "00000000-0000-4000-8000-000000000000"
		mirrorKey      = "00000000-0000-4000-8000-000000000001"
		missingReceipt = -1
	)

	h := internalProjectHeartbeat(t, []byte("[projectmap]\nmain[.]go = mirrored\n"))
	entity := filepath.ToSlash(h.Entity)
	receiver := func(key string, received, status *atomic.Int32) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "Basic "+base64.StdEncoding.EncodeToString([]byte(key)), r.Header.Get("Authorization"))

			var sent []heartbeatpkg.Heartbeat
			if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&sent)) || !assert.Len(t, sent, 1) {
				w.WriteHeader(http.StatusBadRequest)
				return
			}

			assert.Equal(t, entity, sent[0].Entity)
			assert.Equal(t, h.Time, sent[0].Time)
			assert.Equal(t, heartbeatpkg.PointerTo("mirrored"), sent[0].Project)
			received.Add(1)

			switch status.Load() {
			case http.StatusUnauthorized:
				w.WriteHeader(http.StatusUnauthorized)
			case missingReceipt:
				writeHeartbeatResults(t, w, 0, http.StatusCreated)
			default:
				writeHeartbeatResults(t, w, len(sent), http.StatusCreated)
			}
		}))
	}

	var primary, mirror, primaryStatus, mirrorStatus atomic.Int32

	primaryAPI := receiver(primaryKey, &primary, &primaryStatus)
	defer primaryAPI.Close()

	mirrorAPI := receiver(mirrorKey, &mirror, &mirrorStatus)
	defer mirrorAPI.Close()

	queueFile := filepath.Join(t.TempDir(), "offline.bdb")
	v := viper.New()
	v.Set("internal-config", filepath.Join(t.TempDir(), "internal.cfg"))
	v.Set("settings.api_url", primaryAPI.URL)
	v.Set("settings.api_key", primaryKey)
	v.Set("api_urls.main", mirrorAPI.URL+"|"+mirrorKey)

	loaded, err := loadAISyncParams(t.Context(), v, paramspkg.FlagReadOrderFlagPrecedence)
	require.NoError(t, err)

	loaded.Offline.LastSentAt = time.Now()
	loaded.Offline.RateLimit = time.Hour

	err = sendPreparedHeartbeats(t.Context(), v, loaded, queueFile, []heartbeatpkg.Heartbeat{h}, loadAISyncParams)
	require.NoError(t, err)

	assert.Zero(t, primary.Load())
	assert.Zero(t, mirror.Load())

	stored, err := offlinepkg.ReadHeartbeats(t.Context(), queueFile, 10)
	require.NoError(t, err)
	require.Len(t, stored, 1)
	assert.Empty(t, stored[0].APIURL)
	assert.Empty(t, stored[0].APIKey)

	require.NoError(t, offlinesync.SyncOfflineActivity(t.Context(), v, queueFile))
	assert.EqualValues(t, 1, primary.Load())
	assert.EqualValues(t, 1, mirror.Load())

	// API destination groups are sent in URL order.
	secondStatus := &primaryStatus
	if primaryAPI.URL < mirrorAPI.URL {
		secondStatus = &mirrorStatus
	}

	t.Run("authentication failure after confirmed sibling", func(t *testing.T) {
		secondStatus.Store(http.StatusUnauthorized)
		t.Cleanup(func() { secondStatus.Store(0) })

		v.Set("entity", h.Entity)
		v.Set("entity-type", "file")
		v.Set("time", h.Time)
		v.Set("sync-ai-disabled", true)
		v.Set("offline-queue-file", queueFile)
		t.Cleanup(func() { v.Set("sync-ai-disabled", false) })

		code, err := Run(t.Context(), v)
		require.ErrorIs(t, err, offlinepkg.ErrUnreplayableRoute)

		var authErr api.ErrAuth
		assert.ErrorAs(t, err, &authErr)
		assert.Equal(t, exitcode.ErrAuth, code)
		assert.EqualValues(t, 2, primary.Load())
		assert.EqualValues(t, 2, mirror.Load())

		count, err := offlinepkg.CountHeartbeats(t.Context(), queueFile)
		require.NoError(t, err)
		assert.Zero(t, count)
	})

	for _, tt := range []struct {
		name   string
		status int32
	}{
		{name: "unconfirmed sync destination", status: missingReceipt},
		{name: "unauthorized sync destination", status: http.StatusUnauthorized},
	} {
		t.Run(tt.name, func(t *testing.T) {
			primaryBefore, mirrorBefore := primary.Load(), mirror.Load()

			secondStatus.Store(tt.status)
			t.Cleanup(func() { secondStatus.Store(0) })

			err := sendPreparedHeartbeats(t.Context(), v, loaded, queueFile, []heartbeatpkg.Heartbeat{h}, loadAISyncParams)
			require.NoError(t, err)

			err = offlinesync.SyncOfflineActivity(t.Context(), v, queueFile)
			require.ErrorIs(t, err, offlinepkg.ErrUnreplayableRoute)

			if tt.status == http.StatusUnauthorized {
				var authErr api.ErrAuth
				assert.ErrorAs(t, err, &authErr)
			}

			assert.Equal(t, primaryBefore+1, primary.Load())
			assert.Equal(t, mirrorBefore+1, mirror.Load())

			secondStatus.Store(0)

			require.NoError(t, offlinesync.SyncOfflineActivity(t.Context(), v, queueFile))
			assert.Equal(t, primaryBefore+1, primary.Load())
			assert.Equal(t, mirrorBefore+1, mirror.Load())
		})
	}

	t.Run("active backoff stages unsent mirror", func(t *testing.T) {
		primaryBefore, mirrorBefore := primary.Load(), mirror.Load()
		loaded.Offline.LastSentAt = time.Time{}
		loaded.API.BackoffAt = time.Now()
		loaded.API.BackoffRetries = 1

		err := sendPreparedHeartbeats(t.Context(), v, loaded, queueFile, []heartbeatpkg.Heartbeat{h}, loadAISyncParams)

		var backoffErr api.ErrBackoff
		require.ErrorAs(t, err, &backoffErr)
		assert.NotErrorIs(t, err, offlinepkg.ErrUnreplayableRoute)
		assert.Equal(t, primaryBefore, primary.Load())
		assert.Equal(t, mirrorBefore, mirror.Load())

		count, err := offlinepkg.CountHeartbeats(t.Context(), queueFile)
		require.NoError(t, err)
		assert.Equal(t, 1, count)
	})
}

func writeHeartbeatResults(t *testing.T, w http.ResponseWriter, count, firstStatus int) {
	t.Helper()

	responses := make([]json.RawMessage, count)
	for i := range responses {
		responses[i] = json.RawMessage(`[{"data":{"id":"example"}},201]`)
	}

	if count > 0 && firstStatus != http.StatusCreated {
		responses[0] = json.RawMessage(fmt.Sprintf(`[{"error":"rejected"},%d]`, firstStatus))
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
