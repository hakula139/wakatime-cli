package api

import (
	"bytes"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wakatime/wakatime-cli/pkg/heartbeat"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func doAndClose(t *testing.T, client *Client, req *http.Request) error {
	t.Helper()

	resp, err := client.Do(t.Context(), req)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}

	return err
}

func TestClientDoDNSFallback(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, BaseURL+"/users/current", nil)
	require.NoError(t, err)

	var calls int

	client := &Client{
		baseURL: BaseURL,
		client:  &http.Client{Transport: NewTransport()},
		doFunc: func(_ *Client, req *http.Request) (*http.Response, error) {
			calls++
			if calls == 1 {
				return nil, &net.DNSError{Err: "no such host", Name: "api.wakatime.com"}
			}

			assert.Contains(t, []string{BaseIPAddrv4, "[" + BaseIPAddrv6 + "]"}, req.URL.Host)

			return &http.Response{
				StatusCode: http.StatusAccepted,
				Body:       io.NopCloser(strings.NewReader("")),
			}, nil
		},
	}

	resp, err := client.Do(t.Context(), req)
	require.NoError(t, err)

	defer resp.Body.Close()

	assert.Equal(t, http.StatusAccepted, resp.StatusCode)
	assert.Equal(t, 2, calls)
}

func TestSendHeartbeatsCustomDestinationDoesNotFallback(t *testing.T) {
	for _, test := range []struct {
		name string
		url  string
		host string
	}{
		{name: "custom host", url: "https://custom.example.com/api", host: "custom.example.com"},
		{name: "lookalike host", url: "https://api.wakatime.com.example.com/api", host: "api.wakatime.com.example.com"},
		{name: "insecure scheme", url: "http://api.wakatime.com/api", host: "api.wakatime.com"},
		{name: "custom port", url: "https://api.wakatime.com:8443/api", host: "api.wakatime.com:8443"},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := NewClient(BaseURL)

			var destinations []string

			client.doFunc = func(_ *Client, req *http.Request) (*http.Response, error) {
				destinations = append(destinations, req.URL.Host)

				return nil, &net.DNSError{Err: "no such host", Name: test.host}
			}

			_, err := client.SendHeartbeats(t.Context(), []heartbeat.Heartbeat{{
				APIURL: test.url, APIKey: "test-key", Entity: "/home/u/src/example.go",
			}})

			require.Error(t, err)
			assert.Equal(t, []string{test.host}, destinations)
		})
	}
}

func TestNTLMRetryStopsAfterSecondFailure(t *testing.T) {
	client := NewClient("https://example.com")
	originalTransport := client.client.Transport

	var calls int

	client.doFunc = func(*Client, *http.Request) (*http.Response, error) {
		calls++
		if calls > 2 {
			panic("unexpected third attempt")
		}

		return nil, errors.New("network unavailable")
	}
	option, err := WithNTLMRequestRetry(t.Context(), `EXAMPLE\\user:password`)
	require.NoError(t, err)
	option(client)

	req, err := http.NewRequest(http.MethodGet, "https://example.com", nil)
	require.NoError(t, err)

	assert.NotPanics(t, func() {
		err = doAndClose(t, client, req)
	})

	require.Error(t, err)
	assert.Equal(t, 2, calls)
	assert.Same(t, originalTransport, client.client.Transport)
}

func TestClientDoDNSFallbackRetryError(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, BaseURL+"/users/current", nil)
	require.NoError(t, err)

	var calls int

	client := &Client{
		baseURL: BaseURL,
		client:  &http.Client{Transport: NewTransport()},
		doFunc: func(_ *Client, _ *http.Request) (*http.Response, error) {
			calls++
			if calls == 1 {
				return nil, &net.DNSError{Err: "no such host", Name: "api.wakatime.com"}
			}

			return nil, errors.New("retry failed")
		},
	}

	err = doAndClose(t, client, req)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "retry request failed: retry failed")
	assert.Equal(t, 2, calls)
}

func TestClientDoDNSFallbackPreservesRequestAndClient(t *testing.T) {
	client := NewClient(BaseURL)
	client.client.Timeout = 5 * time.Second
	originalClient := client.client
	url := BaseURL + "/users/current/heartbeats.bulk"
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader("payload"))
	require.NoError(t, err)

	var bodies []string

	client.doFunc = func(c *Client, req *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(req.Body)
		require.NoError(t, err)

		bodies = append(bodies, string(body))

		assert.Equal(t, 5*time.Second, c.client.Timeout)

		if len(bodies) == 1 {
			return nil, &net.DNSError{Err: "no such host", Name: "api.wakatime.com"}
		}

		return &http.Response{StatusCode: http.StatusAccepted, Body: io.NopCloser(strings.NewReader(""))}, nil
	}

	err = doAndClose(t, client, req)
	require.NoError(t, err)

	assert.Equal(t, []string{"payload", "payload"}, bodies)
	assert.Equal(t, url, req.URL.String())
	assert.Same(t, originalClient, client.client)
}

func TestClientDoDoesNotFallback(t *testing.T) {
	dnsErr := &net.DNSError{Err: "no such host", Name: "custom.example.com"}
	tests := map[string]struct {
		BaseURL string
		Err     error
	}{
		"custom base url": {
			BaseURL: "https://custom.example.com/api",
			Err:     dnsErr,
		},
		"non dns error": {
			BaseURL: BaseURL,
			Err:     errors.New("connection refused"),
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, test.BaseURL, nil)
			require.NoError(t, err)

			var calls int

			client := &Client{
				baseURL: test.BaseURL,
				doFunc: func(_ *Client, _ *http.Request) (*http.Response, error) {
					calls++

					return nil, test.Err
				},
			}

			err = doAndClose(t, client, req)

			require.Error(t, err)
			assert.ErrorIs(t, err, test.Err)
			assert.Equal(t, 1, calls)
		})
	}
}

func TestRequestCloneHelpers(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "https://example.com", nil)
	require.NoError(t, err)

	cloned, err := cloneRequest(req)
	require.NoError(t, err)
	assert.NotSame(t, req, cloned)

	req, err = http.NewRequest(http.MethodPost, "https://example.com", io.NopCloser(strings.NewReader("body")))
	require.NoError(t, err)

	_, err = cloneRequest(req)
	require.EqualError(t, err, "request body is not replayable")

	req, err = http.NewRequest(http.MethodPost, "https://example.com", bytes.NewReader([]byte("body")))
	require.NoError(t, err)

	cloned, err = cloneRequest(req)
	require.NoError(t, err)

	defer cloned.Body.Close()

	contents, err := io.ReadAll(cloned.Body)
	require.NoError(t, err)
	assert.Equal(t, "body", string(contents))

	req.GetBody = func() (io.ReadCloser, error) {
		return nil, errors.New("get body failed")
	}

	_, err = cloneRequest(req)
	require.EqualError(t, err, "get body failed")
}

func TestShouldRetryProxyWithHTTP(t *testing.T) {
	assert.True(t, shouldRetryProxyWithHTTP(errors.New("proxyconnect tcp: server gave HTTP response to HTTPS client")))
	assert.True(t, shouldRetryProxyWithHTTP(errors.New("server gave HTTP response to HTTPS client")))
	assert.False(t, shouldRetryProxyWithHTTP(errors.New("tls handshake timeout")))
}

func TestTLSOptions(t *testing.T) {
	client := NewClient("https://example.com", WithDisableSSLVerify())
	transport := client.client.Transport.(*http.Transport)
	require.NotNil(t, transport.TLSClientConfig)
	assert.True(t, transport.TLSClientConfig.InsecureSkipVerify)

	pool := CACerts(t.Context())
	client = NewClient("https://example.com", WithSSLCertPool(pool))
	transport = client.client.Transport.(*http.Transport)
	require.NotNil(t, transport.TLSClientConfig)
	assert.Same(t, pool, transport.TLSClientConfig.RootCAs)

	transport = NewTransportWithHostVerificationDisabled(t.Context())
	require.NotNil(t, transport.TLSClientConfig)
	assert.Equal(t, serverName, transport.TLSClientConfig.ServerName)
	assert.NotNil(t, transport.TLSClientConfig.RootCAs)
}

func TestWithSSLCertFile(t *testing.T) {
	certFile := filepath.Join(t.TempDir(), "ca.pem")
	require.NoError(t, os.WriteFile(certFile, []byte("not a cert"), 0600))

	opt, err := WithSSLCertFile(t.Context(), certFile)
	require.NoError(t, err)
	require.NotNil(t, opt)

	client := NewClient("https://example.com", opt)
	transport := client.client.Transport.(*http.Transport)
	require.NotNil(t, transport.TLSClientConfig)
	assert.NotNil(t, transport.TLSClientConfig.RootCAs)

	_, err = WithSSLCertFile(t.Context(), certFile+".missing")
	require.Error(t, err)
}

func TestOptionErrorBranches(t *testing.T) {
	_, err := WithAuth(BasicAuth{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to retrieve auth header value")

	noop, err := WithNTLM("invalid")
	require.Error(t, err)
	assert.NotNil(t, noop)
	assert.Contains(t, err.Error(), "invalid ntlm credentials format")

	noop, err = WithNTLMRequestRetry(t.Context(), "invalid")
	require.Error(t, err)
	assert.NotNil(t, noop)
	assert.Contains(t, err.Error(), "invalid ntlm credentials format")

	_, err = WithProxy("%")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse proxy url")

	client := NewClient("https://example.com")
	transport := LazyCreateNewTransport(nil)
	require.NotNil(t, transport)
	assert.NotSame(t, client.client.Transport, transport)

	opt, err := WithProxy("http://proxy.example.com")
	require.NoError(t, err)
	opt(client)
}

func TestWithProxyHTTPSFallbackNonReplayableBody(t *testing.T) {
	client := NewClient("https://example.com")
	client.doFunc = func(*Client, *http.Request) (*http.Response, error) {
		return nil, errors.New("proxyconnect tcp: server gave HTTP response to HTTPS client")
	}

	opt, err := WithProxy("https://proxy.example.com")
	require.NoError(t, err)
	opt(client)

	req, err := http.NewRequest(
		http.MethodPost,
		"https://example.com",
		io.NopCloser(strings.NewReader("body")),
	)
	require.NoError(t, err)

	err = doAndClose(t, client, req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "proxyconnect tcp")
}
