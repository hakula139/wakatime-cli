package api

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Azure/go-ntlmssp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDNSFallbackPreservesTransportConfiguration(t *testing.T) {
	for _, test := range []struct {
		name     string
		ntlm     bool
		insecure bool
	}{
		{name: "standard verified"},
		{name: "standard insecure", insecure: true},
		{name: "ntlm verified", ntlm: true},
		{name: "ntlm insecure", ntlm: true, insecure: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			proxy, err := url.Parse("http://proxy.example.com:3128")
			require.NoError(t, err)

			roots := x509.NewCertPool()
			transport := NewTransport()
			transport.Proxy = http.ProxyURL(proxy)
			transport.TLSClientConfig = &tls.Config{
				MinVersion:         tls.VersionTLS13,
				RootCAs:            roots,
				ServerName:         "configured.example.com",
				InsecureSkipVerify: test.insecure,
			}
			client := NewClient(BaseURL)
			client.client.Timeout = 5 * time.Second

			client.client.Transport = transport
			if test.ntlm {
				client.client.Transport = ntlmssp.Negotiator{AllowBasicAuth: true, RoundTripper: transport}
			}

			original := client.client

			var calls int

			client.doFunc = func(c *Client, req *http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					return nil, &net.DNSError{Err: "no such host", Name: "api.wakatime.com"}
				}

				assert.Equal(t, 5*time.Second, c.client.Timeout)

				observed := c.client.Transport
				if test.ntlm {
					negotiator, ok := observed.(ntlmssp.Negotiator)
					require.True(t, ok)
					assert.True(t, negotiator.AllowBasicAuth)
					observed = negotiator.RoundTripper
				}

				retry, ok := observed.(*http.Transport)
				require.True(t, ok)
				require.NotNil(t, retry.Proxy)
				gotProxy, err := retry.Proxy(req)
				require.NoError(t, err)
				assert.Equal(t, proxy.String(), gotProxy.String())
				require.NotNil(t, retry.TLSClientConfig)
				assert.Same(t, roots, retry.TLSClientConfig.RootCAs)
				assert.Equal(t, uint16(tls.VersionTLS13), retry.TLSClientConfig.MinVersion)
				assert.Equal(t, test.insecure, retry.TLSClientConfig.InsecureSkipVerify)
				assert.Equal(t, "api.wakatime.com", retry.TLSClientConfig.ServerName)

				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(""))}, nil
			}
			req, err := http.NewRequest(http.MethodGet, BaseURL+"/users/current", nil)
			require.NoError(t, err)

			err = doAndClose(t, client, req)
			require.NoError(t, err)

			assert.Equal(t, 2, calls)
			assert.Same(t, original, client.client)
			assert.Equal(t, "configured.example.com", transport.TLSClientConfig.ServerName)
			assert.Equal(t, test.insecure, transport.TLSClientConfig.InsecureSkipVerify)
		})
	}
}

func TestDNSFallbackKeepsDefaultCertificateRoots(t *testing.T) {
	client := NewClient(BaseURL)

	var calls int

	client.doFunc = func(c *Client, _ *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return nil, &net.DNSError{Err: "no such host", Name: "api.wakatime.com"}
		}

		transport, ok := c.client.Transport.(*http.Transport)
		require.True(t, ok)
		require.NotNil(t, transport.TLSClientConfig)
		assert.NotNil(t, transport.TLSClientConfig.RootCAs)

		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(""))}, nil
	}
	req, err := http.NewRequest(http.MethodGet, BaseURL+"/users/current", nil)
	require.NoError(t, err)

	err = doAndClose(t, client, req)
	require.NoError(t, err)

	assert.Equal(t, 2, calls)
}

func TestDNSFallbackLeavesUnsupportedTransportUntouched(t *testing.T) {
	client := NewClient(BaseURL)
	transport := &dnsTestRoundTripper{}
	client.client.Transport = transport
	dnsErr := &net.DNSError{Err: "no such host", Name: "api.wakatime.com"}

	var calls int

	client.doFunc = func(*Client, *http.Request) (*http.Response, error) {
		calls++

		return nil, dnsErr
	}
	req, err := http.NewRequest(http.MethodGet, BaseURL+"/users/current", nil)
	require.NoError(t, err)

	err = doAndClose(t, client, req)

	assert.ErrorIs(t, err, dnsErr)
	assert.Equal(t, 1, calls)
	assert.Same(t, transport, client.client.Transport)
}

type dnsTestRoundTripper struct{}

func (*dnsTestRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, &net.DNSError{Err: "no such host", Name: "api.wakatime.com"}
}
