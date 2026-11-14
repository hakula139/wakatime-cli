package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/wakatime/wakatime-cli/pkg/log"
)

const (
	// BaseURL is the base url of the wakatime api.
	BaseURL = "https://api.wakatime.com/api/v1"
	// BaseIPAddrv4 is the base ip address v4 of the wakatime api.
	BaseIPAddrv4 = "143.244.210.202"
	// BaseIPAddrv6 is the base ip address v6 of the wakatime api.
	BaseIPAddrv6 = "2604:a880:4:1d0::2a7:b000"
	// DefaultTimeoutSecs is the default timeout used for requests to the wakatime api.
	DefaultTimeoutSecs = 120
)

// Client communicates with the wakatime api.
type Client struct {
	baseURL        string
	client         *http.Client
	ntlmAuthHeader string
	// doFunc allows api client options to manipulate request/response handling.
	// default function will be set in constructor.
	//
	// wrapping by api options should be performed as follows:
	//
	//	next := c.doFunc
	//	c.doFunc = func(c *Client, req *http.Request) (*http.Response, error) {
	//		// do something
	//		resp, err := next(c, req)
	//		// do more
	//		return resp, err
	//	}
	doFunc func(c *Client, req *http.Request) (*http.Response, error)
}

// NewClient creates a new Client. Any number of Options can be provided.
func NewClient(baseURL string, opts ...Option) *Client {
	c := &Client{
		baseURL: baseURL,
		client: &http.Client{
			Transport: NewTransport(),
		},
		doFunc: func(c *Client, req *http.Request) (*http.Response, error) {
			req.Header.Set("Accept", "application/json")
			//nolint:gosec // Request target comes from configured API URL and internal request construction.
			return c.client.Do(req)
		},
	}

	for _, option := range opts {
		option(c)
	}

	return c
}

// Do executes c.doFunc(), which in turn allows wrapping c.client.Do() and manipulating
// the request behavior of the api client.
func (c *Client) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	resp, err := c.doFunc(c, req)
	if err == nil {
		return resp, nil
	}

	if req.URL.Scheme != "https" || !strings.EqualFold(req.URL.Hostname(), serverName) ||
		(req.URL.Port() != "" && req.URL.Port() != "443") {
		return nil, err
	}

	var dnsError *net.DNSError
	if !errors.As(err, &dnsError) {
		return nil, err
	}

	transport := dnsFallbackTransport(ctx, c.client.Transport)
	if transport == nil {
		return nil, err
	}

	reqRetry, cloneErr := cloneRequest(req)
	if cloneErr != nil {
		return nil, err
	}

	clientRetry := *c
	httpClientRetry := *c.client
	httpClientRetry.Transport = transport
	clientRetry.client = &httpClientRetry

	reqRetry.URL.Host = BaseIPAddrv4
	if isLocalIPv6(ctx) {
		reqRetry.URL.Host = "[" + BaseIPAddrv6 + "]"
	}

	logger := log.Extract(ctx)
	logger.Debugf("dns error, will retry with host ip '%s': %s", reqRetry.URL.Host, err)

	resp, errRetry := clientRetry.doFunc(&clientRetry, reqRetry)
	if errRetry != nil {
		return nil, fmt.Errorf("retry request failed: %s. original error: %s", errRetry, err)
	}

	return resp, nil
}

func isLocalIPv6(ctx context.Context) bool {
	logger := log.Extract(ctx)

	conn, err := net.Dial("udp", fmt.Sprintf("%s:80", BaseIPAddrv4))
	if err != nil {
		logger.Debugf("failed dialing to detect default local ip address, falling back to ipv4: %s", err)
		return false
	}

	defer func() {
		if err := conn.Close(); err != nil {
			logger.Debugf("failed to close connection to api wakatime: %s", err)
		}
	}()

	localAddr := conn.LocalAddr().(*net.UDPAddr)

	return localAddr.IP.To4() == nil
}
