package api

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Azure/go-ntlmssp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRepeatedAuthOptionsKeepFirstCredentials(t *testing.T) {
	client := NewClient("https://example.com")

	var user, password string

	client.doFunc = func(_ *Client, req *http.Request) (*http.Response, error) {
		user, password, _ = req.BasicAuth()

		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(""))}, nil
	}

	for _, name := range []string{"first", "second"} {
		option, err := WithAuth(BasicAuth{User: name})
		require.NoError(t, err)
		option(client)
	}

	req, err := http.NewRequest(http.MethodGet, "https://example.com", nil)
	require.NoError(t, err)

	err = doAndClose(t, client, req)
	require.NoError(t, err)

	assert.Equal(t, "first", user)
	assert.Empty(t, password)
}

func TestNTLMRetryPreservesAuthenticationThroughProxyFallback(t *testing.T) {
	apiHeader, err := (BasicAuth{Secret: "test-key"}).HeaderValue()
	require.NoError(t, err)
	ntlmHeader, err := (BasicAuth{User: `EXAMPLE\\user`, Secret: "password"}).HeaderValue()
	require.NoError(t, err)

	client := NewClient("https://example.com")

	var credentials []string

	client.doFunc = func(c *Client, req *http.Request) (*http.Response, error) {
		credentials = append(credentials, req.Header.Get("Authorization"))
		switch len(credentials) {
		case 1:
			return nil, errors.New("connection closed")
		case 2:
			return nil, errors.New("proxyconnect tcp: connection refused")
		}

		negotiator, ok := c.client.Transport.(ntlmssp.Negotiator)
		require.True(t, ok)
		transport, ok := negotiator.RoundTripper.(*http.Transport)
		require.True(t, ok)

		proxy, err := transport.Proxy(req)
		require.NoError(t, err)
		assert.Equal(t, "http://proxy.example.com:3128", proxy.String())

		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(""))}, nil
	}
	withAuth, err := WithAuth(BasicAuth{Secret: "test-key"})
	require.NoError(t, err)
	withAuth(client)

	withProxy, err := WithProxy("https://proxy.example.com:3128")
	require.NoError(t, err)
	withProxy(client)

	withRetry, err := WithNTLMRequestRetry(t.Context(), `EXAMPLE\\user:password`)
	require.NoError(t, err)
	withRetry(client)
	originalClient := client.client
	originalTransport := client.client.Transport
	req, err := http.NewRequest(http.MethodGet, "https://example.com", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", apiHeader)
	originalHeaders := req.Header.Clone()

	assert.NotPanics(t, func() {
		err = doAndClose(t, client, req)
	})
	require.NoError(t, err)

	assert.Equal(t, []string{apiHeader, ntlmHeader, ntlmHeader}, credentials)
	assert.Equal(t, originalHeaders, req.Header)
	assert.Same(t, originalClient, client.client)
	assert.Same(t, originalTransport, client.client.Transport)
	proxy, err := originalTransport.(*http.Transport).Proxy(req)
	require.NoError(t, err)
	assert.Equal(t, "https://proxy.example.com:3128", proxy.String())
}

func TestNTLMRetryOverridesAPIKeyOnlyForRetry(t *testing.T) {
	apiHeader, err := (BasicAuth{Secret: "test-key"}).HeaderValue()
	require.NoError(t, err)
	ntlmHeader, err := (BasicAuth{User: `EXAMPLE\\user`, Secret: "password"}).HeaderValue()
	require.NoError(t, err)

	client := NewClient("https://example.com")
	originalClient := client.client
	originalTransport := client.client.Transport

	var credentials []string

	client.doFunc = func(_ *Client, req *http.Request) (*http.Response, error) {
		credentials = append(credentials, req.Header.Get("Authorization"))
		if len(credentials) == 1 {
			return nil, errors.New("connection closed")
		}

		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(""))}, nil
	}
	withAuth, err := WithAuth(BasicAuth{Secret: "test-key"})
	require.NoError(t, err)
	withAuth(client)

	withRetry, err := WithNTLMRequestRetry(t.Context(), `EXAMPLE\\user:password`)
	require.NoError(t, err)
	withRetry(client)

	req, err := http.NewRequest(http.MethodGet, "https://example.com", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", apiHeader)
	originalHeaders := req.Header.Clone()

	err = doAndClose(t, client, req)
	require.NoError(t, err)

	assert.Equal(t, []string{apiHeader, ntlmHeader}, credentials)
	assert.Equal(t, originalHeaders, req.Header)
	assert.Same(t, originalClient, client.client)
	assert.Same(t, originalTransport, client.client.Transport)
}
