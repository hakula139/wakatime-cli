package apikey

import (
	"context"

	"github.com/wakatime/wakatime-cli/pkg/heartbeat"
	"github.com/wakatime/wakatime-cli/pkg/log"
	"github.com/wakatime/wakatime-cli/pkg/regex"
)

type (
	// Config contains apikey project detection configurations.
	Config struct {
		// DefaultAPIKey contains the default api key.
		DefaultAPIKey string
		// DefaultAPIURL contains the default api url.
		DefaultAPIURL string
		// Patterns contains the overridden api key per path.
		MapPatterns []MapPattern
		// URLPatterns contains the overridden api url and key per path.
		URLPatterns []URLPattern
	}

	// MapPattern contains [project_api_key] data.
	MapPattern struct {
		// APIKey is the project related api key.
		APIKey string
		// Regex is the regular expression for a specific path.
		Regex regex.Regex
	}

	// URLPattern contains [api_urls] data mapping paths to API URL and key combinations.
	URLPattern struct {
		// APIURL is the API URL for matching paths.
		APIURL string
		// APIKey is the API key for matching paths.
		APIKey string
		// Regex is the regular expression for a specific path.
		Regex regex.Regex
	}

	// destination represents a unique API URL and key combination.
	destination struct {
		url string
		key string
	}
)

// WithReplacing initializes and returns a heartbeat handle option, which
// can be used in a heartbeat processing pipeline to replace default api key
// and api url for a heartbeat following the provided configurations.
//
// The behavior is:
//  1. Always send to api_url (default WakaTime) with api_key (or project_api_key if matched)
//  2. Also send to ALL matching api_urls patterns
//  3. Send once per unique URL and key pair
func WithReplacing(config Config) heartbeat.HandleOption {
	return func(next heartbeat.Handle) heartbeat.Handle {
		return func(ctx context.Context, hh []heartbeat.Heartbeat) ([]heartbeat.Result, error) {
			// logger.Debugln("execute api key replacing")
			var result []heartbeat.Heartbeat

			for _, h := range hh {
				for dest := range config.destinations(ctx, h.Entity) {
					copy := h
					copy.APIURL = dest.url
					copy.APIKey = dest.key
					result = append(result, copy)
				}
			}

			return next(ctx, result)
		}
	}
}

// CanReplay reports whether a queued heartbeat would go to its original API destination.
func (config Config) CanReplay(ctx context.Context, h heartbeat.Heartbeat) bool {
	destinations := config.destinations(ctx, h.Entity)
	_, found := destinations[destination{url: h.APIURL, key: h.APIKey}]

	return found && len(destinations) == 1
}

func (config Config) destinations(ctx context.Context, entity string) map[destination]struct{} {
	apiKey := config.DefaultAPIKey
	if matchedKey, ok := MatchPattern(ctx, entity, config.MapPatterns); ok {
		apiKey = matchedKey
	}

	destinations := map[destination]struct{}{{url: config.DefaultAPIURL, key: apiKey}: {}}
	for _, pattern := range MatchAllURLPatterns(ctx, entity, config.URLPatterns) {
		destinations[destination{url: pattern.APIURL, key: pattern.APIKey}] = struct{}{}
	}

	return destinations
}

// MatchPattern matches regex against entity's path to find alternate api key.
func MatchPattern(ctx context.Context, fp string, patterns []MapPattern) (string, bool) {
	logger := log.Extract(ctx)

	for _, pattern := range patterns {
		if pattern.Regex.MatchString(ctx, fp) {
			logger.Debugf("api key pattern %q matched path %q", pattern.Regex.String(), fp)
			return pattern.APIKey, true
		}

		logger.Debugf("api key pattern %q did not match path %q", pattern.Regex.String(), fp)
	}

	return "", false
}

// MatchAllURLPatterns matches regex against entity's path and returns ALL matching url patterns.
func MatchAllURLPatterns(ctx context.Context, fp string, patterns []URLPattern) []URLPattern {
	logger := log.Extract(ctx)

	var matched []URLPattern

	for _, pattern := range patterns {
		if pattern.Regex.MatchString(ctx, fp) {
			logger.Debugf("api url pattern %q matched path %q", pattern.Regex.String(), fp)
			matched = append(matched, pattern)
		} else {
			logger.Debugf("api url pattern %q did not match path %q", pattern.Regex.String(), fp)
		}
	}

	return matched
}
