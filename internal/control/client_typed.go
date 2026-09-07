package control

import (
	"context"
	"encoding/json"
	"net/url"

	"github.com/konor123/Free-Model-Router/internal/usage"
)

// ModelsResponse is the typed model-manager read response.
type ModelsResponse struct {
	CatalogRevision int64           `json:"catalogRevision"`
	Data            []ModelResponse `json:"data"`
}

// ProvidersResponse is the typed provider-manager read response.
type ProvidersResponse struct {
	Data []ProviderResponse `json:"data"`
}

// LogsResponse is the typed usage-window read response.
type LogsResponse struct {
	Data []usage.RequestRecord `json:"data"`
}

// Typed mutation aliases keep request fields in JSON bodies and out of paths.
type ModelPoolPutRequest = poolPutRequest
type ModelPoolPatchRequest = poolPatchRequest
type PinRequest = pinRequest
type RevisionRequest = revisionRequest

func (c *Client) Config(ctx context.Context) (ConfigResponse, error) {
	var response ConfigResponse
	err := c.getJSON(ctx, "/_fmr/config", &response)
	return response, err
}

func (c *Client) UpdateConfig(ctx context.Context, request ConfigUpdateRequest) (ConfigUpdateResponse, error) {
	var response ConfigUpdateResponse
	err := c.doJSON(ctx, "PUT", "/_fmr/config", request, &response)
	return response, err
}

func (c *Client) Status(ctx context.Context) (StatusResponse, error) {
	var response StatusResponse
	err := c.getJSON(ctx, "/_fmr/status", &response)
	return response, err
}

func (c *Client) Providers(ctx context.Context) (ProvidersResponse, error) {
	var response ProvidersResponse
	err := c.getJSON(ctx, "/_fmr/providers", &response)
	return response, err
}

func (c *Client) Models(ctx context.Context) (ModelsResponse, error) {
	var response ModelsResponse
	err := c.getJSON(ctx, "/_fmr/models", &response)
	return response, err
}

func (c *Client) RefreshCatalog(ctx context.Context) error {
	var response struct {
		Status string `json:"status"`
	}
	return c.doJSON(ctx, "POST", "/_fmr/catalog/refresh", struct{}{}, &response)
}

func (c *Client) ModelPool(ctx context.Context) (PoolResponse, error) {
	var response PoolResponse
	err := c.getJSON(ctx, "/_fmr/model-pool", &response)
	return response, err
}

func (c *Client) Logs(ctx context.Context, values url.Values) (LogsResponse, error) {
	path := "/_fmr/logs"
	if len(values) > 0 {
		path += "?" + values.Encode()
	}
	var response LogsResponse
	err := c.getJSON(ctx, path, &response)
	return response, err
}

func (c *Client) ReplaceModelPool(ctx context.Context, request ModelPoolPutRequest) (PoolResponse, error) {
	var response PoolResponse
	err := c.doJSON(ctx, "PUT", "/_fmr/model-pool", request, &response)
	return response, err
}

func (c *Client) PatchModelPool(ctx context.Context, request ModelPoolPatchRequest) (PoolResponse, error) {
	var response PoolResponse
	err := c.doJSON(ctx, "PATCH", "/_fmr/model-pool", request, &response)
	return response, err
}

func (c *Client) Pin(ctx context.Context, request PinRequest) (PoolResponse, error) {
	var response PoolResponse
	err := c.doJSON(ctx, "POST", "/_fmr/pin", request, &response)
	return response, err
}

func (c *Client) Auto(ctx context.Context, request RevisionRequest) (PoolResponse, error) {
	var response PoolResponse
	err := c.doJSON(ctx, "POST", "/_fmr/auto", request, &response)
	return response, err
}

func (c *Client) getJSON(ctx context.Context, path string, dst any) error {
	body, err := c.Get(ctx, path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return err
	}
	return nil
}

func (c *Client) doJSON(ctx context.Context, method, path string, request any, dst any) error {
	body, err := c.Do(ctx, method, path, request)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return err
	}
	return nil
}
