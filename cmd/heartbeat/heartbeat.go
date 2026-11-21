package heartbeat

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	apicmd "github.com/wakatime/wakatime-cli/cmd/api"
	"github.com/wakatime/wakatime-cli/cmd/handler"
	offlinecmd "github.com/wakatime/wakatime-cli/cmd/offline"
	"github.com/wakatime/wakatime-cli/pkg/ai"
	"github.com/wakatime/wakatime-cli/pkg/api"
	"github.com/wakatime/wakatime-cli/pkg/backoff"
	"github.com/wakatime/wakatime-cli/pkg/exitcode"
	"github.com/wakatime/wakatime-cli/pkg/filter"
	"github.com/wakatime/wakatime-cli/pkg/heartbeat"
	_ "github.com/wakatime/wakatime-cli/pkg/lexer" // force to load all lexers
	"github.com/wakatime/wakatime-cli/pkg/log"
	"github.com/wakatime/wakatime-cli/pkg/offline"
	"github.com/wakatime/wakatime-cli/pkg/params"
	"github.com/wakatime/wakatime-cli/pkg/ratelimit"
	"github.com/wakatime/wakatime-cli/pkg/wakaerror"

	"github.com/spf13/viper"
)

// Run executes the heartbeat command.
func Run(ctx context.Context, v *viper.Viper) (int, error) {
	logger := log.Extract(ctx)

	queueFilepath, err := offline.QueueFilepath(ctx, v)
	if err != nil {
		logger.Warnf("failed to load offline queue filepath: %s", err)
	}

	params, err := loadParams(ctx, v, params.FlagReadOrderFlagPrecedence)

	heartbeats := BuildHeartbeats(ctx, params.API.Plugin, params.Heartbeat)

	if err != nil {
		logger.Errorf("sending heartbeats failed: %s", err)

		if errSave := offlinecmd.SaveHeartbeats(ctx, v, queueFilepath, renderUserAgents(ctx, heartbeats)); errSave != nil {
			return exitcode.ErrConfigFileParse, fmt.Errorf("failed to save heartbeats to offline queue: %s", errSave)
		}

		return exitcode.ErrAuth, fmt.Errorf("failed to load heartbeat command parameters: %w", err)
	}

	err = SendHeartbeats(ctx, v, params, queueFilepath, heartbeats)
	if err != nil {
		var errwaka wakaerror.Error
		if errors.As(err, &errwaka) {
			return errwaka.ExitCode(), fmt.Errorf("sending heartbeat(s) failed: %w", err)
		}

		return exitcode.ErrGeneric, fmt.Errorf(
			"sending heartbeat(s) failed: %w",
			err,
		)
	}

	logger.Debugln("successfully sent heartbeat(s)")

	return exitcode.Success, nil
}

// SendHeartbeats sends heartbeats to the wakatime api and includes additional
// heartbeats from the offline queue, if available and offline sync is not
// explicitly disabled.
func SendHeartbeats(
	ctx context.Context,
	v *viper.Viper,
	params params.Params,
	queueFilepath string,
	heartbeats []heartbeat.Heartbeat,
) error {
	logger := log.Extract(ctx)

	setLogFields(ctx, params)
	logger.Debugf("params: %s", params)

	heartbeats, err := applyAIParsing(ctx, v, params, heartbeats)
	if err != nil {
		return err
	}

	return sendPreparedHeartbeats(ctx, v, params, queueFilepath, heartbeats, loadParams)
}

func loadParams(
	ctx context.Context,
	v *viper.Viper,
	order params.FlagReadOrder,
) (params.Params, error) {
	var err error

	if v == nil {
		return params.Params{}, errors.New("viper instance unset")
	}

	heartbeatParams, err := params.LoadHeartbeatParams(ctx, v, order)
	if err != nil {
		return params.Params{}, fmt.Errorf("failed to load heartbeat params: %s", err)
	}

	apiParams, err := params.LoadAPIParams(ctx, v, order)
	if err != nil {
		err = fmt.Errorf("failed to load API parameters: %w", err)
	}

	return params.Params{
		AI:        heartbeatParams.AIParams,
		API:       apiParams,
		Heartbeat: heartbeatParams,
		Offline:   params.LoadOfflineParams(ctx, v, order),
	}, err
}

func buildHandle(ctx context.Context, v *viper.Viper, params params.Params, queueFilepath string) (heartbeat.Handle, error) {
	apiClient, err := apicmd.NewClientWithoutAuth(ctx, params.API)
	if err != nil {
		return nil, err
	}

	handleOpts := []heartbeat.HandleOption{
		filter.WithLengthValidator(),
	}

	if !params.Offline.Disabled {
		handleOpts = append(handleOpts,
			offline.WithAIBuffering(queueFilepath),
			offline.WithQueue(queueFilepath, params.API.Routing()),
		)
	}

	handleOpts = append(handleOpts, backoff.WithBackoff(backoff.Config{
		V:        v,
		At:       params.API.BackoffAt,
		Retries:  params.API.BackoffRetries,
		HasProxy: params.API.ProxyURL != "",
	}))

	if params.Offline.Disabled {
		handleOpts = append(handleOpts, validateDirectResults)
	}

	return heartbeat.NewHandle(apiClient, handleOpts...), nil
}

func validateDirectResults(next heartbeat.Handle) heartbeat.Handle {
	return func(ctx context.Context, heartbeats []heartbeat.Heartbeat) ([]heartbeat.Result, error) {
		results, err := next(ctx, heartbeats)
		if err != nil {
			return nil, err
		}

		if len(results) != len(heartbeats) {
			return nil, api.Err{Err: fmt.Errorf(
				"expected %d heartbeat results, received %d results", len(heartbeats), len(results),
			)}
		}

		for _, result := range results {
			if result.Heartbeat.Entity == "" && result.Heartbeat.Time == 0 {
				return nil, api.Err{Err: errors.New("received a result without a matching heartbeat")}
			}

			if result.Status == http.StatusBadRequest || (result.Status >= http.StatusOK && result.Status <= 299) {
				continue
			}

			return nil, api.Err{Err: fmt.Errorf(
				"heartbeat rejected with status %d: %s", result.Status, strings.Join(result.Errors, " "),
			)}
		}

		return results, nil
	}
}

// BuildHeartbeats builds the command line heartbeat then appends any extra stdin heartbeats.
func BuildHeartbeats(ctx context.Context, plugin string, heartbeatParams params.Heartbeat) []heartbeat.Heartbeat {
	var heartbeats = make([]heartbeat.Heartbeat, 0, 1+len(heartbeatParams.ExtraHeartbeats))

	heartbeats = append(heartbeats, heartbeat.New(
		heartbeatParams.AILineChanges,
		heartbeatParams.Project.BranchAlternate,
		heartbeatParams.Category.String(),
		heartbeatParams.CursorPosition,
		heartbeatParams.Entity,
		heartbeatParams.EntityType,
		heartbeatParams.HumanLineChanges,
		heartbeatParams.IsUnsavedEntity,
		heartbeatParams.IsWrite,
		heartbeatParams.Language,
		heartbeatParams.LanguageAlternate,
		heartbeatParams.LineNumber,
		heartbeatParams.LinesInFile,
		heartbeatParams.LocalFile,
		heartbeatParams.Project.Alternate,
		heartbeatParams.Project.ProjectFromGitRemote,
		heartbeatParams.Project.Override,
		heartbeatParams.Sanitize.ProjectPathOverride,
		heartbeatParams.Time,
		plugin,
	))

	if len(heartbeatParams.ExtraHeartbeats) > 0 {
		logger := log.Extract(ctx)
		logger.Debugf("include %d extra heartbeat(s) from stdin", len(heartbeatParams.ExtraHeartbeats))

		for _, h := range heartbeatParams.ExtraHeartbeats {
			h.UserAgent = plugin

			heartbeats = append(heartbeats, h)
		}
	}

	return heartbeats
}

func renderUserAgents(ctx context.Context, heartbeats []heartbeat.Heartbeat) []heartbeat.Heartbeat {
	for i := range heartbeats {
		heartbeats[i].UserAgent = heartbeat.UserAgent(ctx, heartbeats[i].UserAgent)
	}

	return heartbeats
}

func initHandleOptions() []handler.Preprocessor {
	return []handler.Preprocessor{
		handler.WithFormatting(),
		handler.WithEntityModifier(),
		handler.WithHeartbeatFiltering(),
		handler.WithAIFiltering(),
		handler.WithRemoteDetection(),
		handler.WithAPIKeyReplacing(),
		handler.WithFileStatsDetection(),
		handler.WithLanguageDetection(),
		handler.WithDependencyDetection(),
		handler.WithCategoryDetection(),
		handler.WithProjectDetection(),
		handler.WithProjectFiltering(),
		handler.WithHeartbeatSanitization(),
		handler.WithRemoteCleanup(),
	}
}

func applyAIParsing(
	ctx context.Context,
	v *viper.Viper,
	params params.Params,
	heartbeats []heartbeat.Heartbeat,
) ([]heartbeat.Heartbeat, error) {
	handle := ai.WithAISync(ai.Config{
		SyncDisabled: params.AI.SyncDisabled,
		Exclude:      params.AI.Exclude,
		Plugin:       params.API.Plugin,
		Project:      params.Heartbeat.Project,
		Sanitize:     params.Heartbeat.Sanitize,
		V:            v,
	})(func(_ context.Context, hh []heartbeat.Heartbeat) ([]heartbeat.Result, error) {
		results := make([]heartbeat.Result, len(hh))

		for i := range hh {
			results[i] = heartbeat.Result{Heartbeat: hh[i]}
		}

		return results, nil
	})

	results, err := handle(ctx, heartbeats)
	if err != nil {
		return nil, err
	}

	parsed := make([]heartbeat.Heartbeat, 0, len(results))
	for _, result := range results {
		parsed = append(parsed, result.Heartbeat)
	}

	return parsed, nil
}

func sendPreparedHeartbeats(
	ctx context.Context,
	v *viper.Viper,
	params params.Params,
	queueFilepath string,
	heartbeats []heartbeat.Heartbeat,
	paramsLoader func(context.Context, *viper.Viper, params.FlagReadOrder) (params.Params, error),
) (err error) {
	logger := log.Extract(ctx)

	heartbeats = renderUserAgents(ctx, heartbeats)

	setLogFields(ctx, params)
	logger.Debugf("params: %s", params)

	if ratelimit.IsRateLimited(ratelimit.Params{
		Disabled:   params.Offline.Disabled,
		LastSentAt: params.Offline.LastSentAt,
		Timeout:    params.Offline.RateLimit,
	}) {
		err := offlinecmd.SaveHeartbeatsWithParams(ctx, v, queueFilepath, heartbeats, params)
		if err == nil {
			return nil
		}

		if errors.Is(err, offline.ErrUnreplayableRoute) {
			return fmt.Errorf("failed to save rate limited heartbeats: %w", err)
		}

		logger.Errorf("failed to save rate limited heartbeats: %s", err)
	}

	if !params.Offline.Disabled && len(heartbeats) > offline.SendLimit {
		extraHeartbeats := heartbeats[offline.SendLimit:]
		heartbeats = heartbeats[:offline.SendLimit]

		logger.Debugf("save %d extra heartbeat(s) to offline queue", len(extraHeartbeats))

		// Copy the logger before starting the goroutine so offline log fields do
		// not mutate the logger used concurrently by the foreground handler.
		offlineLogger := *logger
		offlineCtx := log.ToContext(ctx, &offlineLogger)

		saved := make(chan error, 1)
		go func() {
			saved <- offlinecmd.SaveHeartbeatsWithParams(offlineCtx, v, queueFilepath, extraHeartbeats, params)
		}()

		defer func() {
			if saveErr := <-saved; saveErr != nil {
				err = errors.Join(err, fmt.Errorf("failed to save extra heartbeats to offline queue: %w", saveErr))
			}
		}()
	}

	sender, err := buildHandle(ctx, v, params, queueFilepath)
	if err != nil {
		err = fmt.Errorf("failed to initialize api client: %w", err)

		if !params.Offline.Disabled {
			if saveErr := offlinecmd.SaveHeartbeatsWithParams(ctx, v, queueFilepath, heartbeats, params); saveErr != nil {
				err = errors.Join(err, fmt.Errorf("failed to save heartbeats to offline queue: %w", saveErr))
			}
		}

		return err
	}

	handle := handler.New(v, handler.Config{
		Params:       params,
		ParamsLoader: paramsLoader,
		Opts:         initHandleOptions(),
	})(sender)

	for batch := range slices.Chunk(heartbeats, offline.SendLimit) {
		results, err := handle(ctx, batch)
		if err != nil {
			return err
		}

		for _, result := range results {
			if len(result.Errors) > 0 {
				logger.Warnln(strings.Join(result.Errors, " "))
			}
		}
	}

	if err := ratelimit.Reset(ctx, v); err != nil {
		logger.Errorf("failed to reset rate limit: %s", err)
	}

	return nil
}

func setLogFields(ctx context.Context, params params.Params) {
	log.AddField(ctx, "file", params.Heartbeat.Entity)
	log.AddField(ctx, "time", params.Heartbeat.Time)

	if params.API.Plugin != "" {
		log.AddField(ctx, "plugin", params.API.Plugin)
	}

	if params.Heartbeat.LineNumber != nil {
		log.AddField(ctx, "lineno", params.Heartbeat.LineNumber)
	}

	if params.Heartbeat.IsWrite != nil {
		log.AddField(ctx, "is_write", params.Heartbeat.IsWrite)
	}
}
