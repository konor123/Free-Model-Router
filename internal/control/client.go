package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client is a small HTTP client for the local control API. It is also usable by
// the desktop bridge, so it contains no CLI-specific process behavior.
type Client struct {
	BaseURL    string
	Token      string
	HTTPClient *http.Client
}

// HTTPError reports a non-2xx control response without exposing request data.
type HTTPError struct {
	StatusCode int
	Body       string
}

func (e *HTTPError) Error() string {
	if e == nil {
		return "control API request failed"
	}
	if e.Body == "" {
		return fmt.Sprintf("control API returned HTTP %d", e.StatusCode)
	}
	return fmt.Sprintf("control API returned HTTP %d: %s", e.StatusCode, e.Body)
}

// NewClient builds a client for an address such as 127.0.0.1:8788 or a full URL.
func NewClient(address, token string) *Client {
	address = strings.TrimSpace(address)
	if !strings.Contains(address, "://") {
		address = "http://" + address
	}
	return &Client{
		BaseURL:    strings.TrimRight(address, "/"),
		Token:      token,
		HTTPClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// Do sends one JSON request and returns the response body.
func (c *Client) Do(ctx context.Context, method, path string, body any) ([]byte, error) {
	if c == nil {
		return nil, errors.New("control client must not be nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	endpoint, err := url.Parse(strings.TrimRight(c.BaseURL, "/") + "/" + strings.TrimLeft(path, "/"))
	if err != nil {
		return nil, fmt.Errorf("parse control URL: %w", err)
	}
	var payload io.Reader
	if body != nil {
		data, marshalErr := json.Marshal(body)
		if marshalErr != nil {
			return nil, fmt.Errorf("marshal control request: %w", marshalErr)
		}
		payload = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), payload)
	if err != nil {
		return nil, fmt.Errorf("create control request: %w", err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token := strings.TrimSpace(c.Token); token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 16<<20))
	if err != nil {
		return nil, fmt.Errorf("read control response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, &HTTPError{StatusCode: response.StatusCode, Body: strings.TrimSpace(string(data))}
	}
	return data, nil
}

// Get sends an authenticated GET request.
func (c *Client) Get(ctx context.Context, path string) ([]byte, error) {
	return c.Do(ctx, http.MethodGet, path, nil)
}

// Post sends an authenticated POST request.
func (c *Client) Post(ctx context.Context, path string, body any) ([]byte, error) {
	return c.Do(ctx, http.MethodPost, path, body)
}

// Put sends an authenticated PUT request.
func (c *Client) Put(ctx context.Context, path string, body any) ([]byte, error) {
	return c.Do(ctx, http.MethodPut, path, body)
}

// Patch sends an authenticated PATCH request.
func (c *Client) Patch(ctx context.Context, path string, body any) ([]byte, error) {
	return c.Do(ctx, http.MethodPatch, path, body)
}
