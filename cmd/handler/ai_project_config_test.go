package handler_test

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wakatime/wakatime-cli/cmd/handler"
	"github.com/wakatime/wakatime-cli/pkg/apikey"
	"github.com/wakatime/wakatime-cli/pkg/heartbeat"
	"github.com/wakatime/wakatime-cli/pkg/offline"
	"github.com/wakatime/wakatime-cli/pkg/params"
	"github.com/wakatime/wakatime-cli/pkg/regex"
	"github.com/wakatime/wakatime-cli/pkg/vipertools"
)

func TestProjectConfigurationBypassesAIBufferingWithoutLeaking(t *testing.T) {
	projectDir := t.TempDir()
	projectEntity := filepath.Join(projectDir, "main.go")
	require.NoError(t, os.WriteFile(projectEntity, nil, 0600))
	require.NoError(t, os.WriteFile(filepath.Join(projectDir, ".wakatime"), nil, 0600))
	plainEntity := filepath.Join(t.TempDir(), "main.go")
	require.NoError(t, os.WriteFile(plainEntity, nil, 0600))
	queuePath := filepath.Join(t.TempDir(), "queue.bdb")

	var (
		sent         []heartbeat.Heartbeat
		projectLoads int
	)

	buffer := offline.WithAIBuffering(queuePath)(func(
		_ context.Context,
		hh []heartbeat.Heartbeat,
	) ([]heartbeat.Result, error) {
		sent = append(sent, hh...)
		return nil, nil
	})
	handle := handler.New(vipertools.MustNew(), handler.Config{
		Params: params.Params{API: params.API{Key: "00000000-0000-4000-8000-000000000001"}},
		ParamsLoader: func(_ context.Context, _ *viper.Viper, _ params.FlagReadOrder) (params.Params, error) {
			projectLoads++
			return params.Params{API: params.API{Key: "00000000-0000-4000-8000-000000000002"}}, nil
		},
		Opts: []handler.Preprocessor{handler.WithAPIKeyReplacing()},
	})(buffer)
	projectHeartbeat := heartbeat.Heartbeat{
		AISession:     "session-a",
		AIInputTokens: 3,
		Category:      heartbeat.AICodingCategory.String(),
		Entity:        projectEntity,
		EntityType:    heartbeat.FileType,
		Time:          float64(time.Now().Unix()),
		UserAgent:     "model-a/1 plugin/1",
	}
	plainHeartbeat := projectHeartbeat
	plainHeartbeat.Entity = plainEntity

	_, err := handle(t.Context(), []heartbeat.Heartbeat{projectHeartbeat, plainHeartbeat})
	require.NoError(t, err)

	projectHeartbeat.APIKey = "00000000-0000-4000-8000-000000000002"
	projectHeartbeat.SkipAIBuffering = true
	assert.Equal(t, []heartbeat.Heartbeat{projectHeartbeat}, sent)
	assert.Equal(t, 1, projectLoads)
	stored, err := offline.ReadHeartbeats(t.Context(), queuePath, 10)
	require.NoError(t, err)
	assert.Equal(t, []heartbeat.Heartbeat{plainHeartbeat}, stored)
}

func TestCustomRoutingBypassesAIBufferingAfterHidingEntity(t *testing.T) {
	entity := filepath.Join(t.TempDir(), "main.go")
	require.NoError(t, os.WriteFile(entity, nil, 0600))
	match := regex.NewRegexpWrap(regexp.MustCompile("^" + regexp.QuoteMeta(entity) + "$"))

	for _, useURLs := range []bool{false, true} {
		name := "project api key"
		if useURLs {
			name = "api urls"
		}

		t.Run(name, func(t *testing.T) {
			p := params.Params{API: params.API{
				Key: "default-key",
				URL: "https://one.example.com/api/v1",
			}}

			p.Heartbeat.Sanitize.HideFileNames = []regex.Regex{match}
			if useURLs {
				p.API.URLPatterns = []apikey.URLPattern{
					{Regex: match, APIKey: "routed-key", APIURL: "https://two.example.com/api/v1"},
				}
			} else {
				p.API.KeyPatterns = []apikey.MapPattern{{Regex: match, APIKey: "routed-key"}}
			}

			var sent []heartbeat.Heartbeat

			queuePath := filepath.Join(t.TempDir(), "queue.bdb")
			handle := handler.New(vipertools.MustNew(), handler.Config{
				Params: p,
				Opts:   []handler.Preprocessor{handler.WithAPIKeyReplacing(), handler.WithHeartbeatSanitization()},
			})(offline.WithAIBuffering(queuePath)(func(
				_ context.Context,
				hh []heartbeat.Heartbeat,
			) ([]heartbeat.Result, error) {
				sent = append(sent, hh...)
				return nil, nil
			}))
			h := heartbeat.Heartbeat{
				AISession: "session-a", AIInputTokens: 3, Category: heartbeat.AICodingCategory.String(),
				Entity: entity, EntityType: heartbeat.FileType, Time: float64(time.Now().Unix()),
			}

			_, err := handle(t.Context(), []heartbeat.Heartbeat{h})
			require.NoError(t, err)

			h.Entity = "HIDDEN.go"
			h.SkipAIBuffering = true
			h.APIKey = "routed-key"
			h.APIURL = "https://one.example.com/api/v1"

			expected := []heartbeat.Heartbeat{h}
			if useURLs {
				expected[0].APIKey = "default-key"
				h.APIURL = "https://two.example.com/api/v1"
				expected = append(expected, h)
			}

			assert.ElementsMatch(t, expected, sent)
			count, err := offline.CountHeartbeats(t.Context(), queuePath)
			require.NoError(t, err)
			assert.Zero(t, count)
		})
	}
}
