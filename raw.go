package devlake

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Do sends a request to an arbitrary DevLake endpoint, using the client's
// HTTP client, auth and logging. body (if not nil) is encoded as JSON and the
// response is decoded into out (if not nil). Non-2xx answers become
// *APIError. It is the escape hatch for endpoints whose documented schema
// does not match reality, and powers the plugin-agnostic helpers.
func (c *Client) Do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	resp, err := c.send(ctx, method, path, query, body)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("devlake: read response: %w", err)
	}
	if err := checkStatus(resp.StatusCode, data); err != nil {
		return err
	}
	if out == nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("devlake: decode %s %s: %w", method, path, err)
	}
	return nil
}

// send builds and sends a request and returns the raw response.
func (c *Client) send(ctx context.Context, method, path string, query url.Values, body any) (*http.Response, error) {
	u := c.baseURL + "/" + strings.TrimLeft(path, "/")
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("devlake: encode request: %w", err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, fmt.Errorf("devlake: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, e := range c.editors {
		if err := e(ctx, req); err != nil {
			return nil, fmt.Errorf("devlake: request editor: %w", err)
		}
	}
	resp, err := c.doer.Do(req)
	if err != nil {
		return nil, fmt.Errorf("devlake: %w", err)
	}
	return resp, nil
}

// checkStatus converts a non-2xx status and body into an *APIError.
func checkStatus(code int, body []byte) error {
	if code >= 200 && code < 300 {
		return nil
	}
	apiErr := &APIError{StatusCode: code, Body: body}
	var env struct {
		Message string   `json:"message"`
		Causes  []string `json:"causes"`
	}
	if json.Unmarshal(body, &env) == nil {
		apiErr.Message = env.Message
		apiErr.Causes = env.Causes
	}
	return apiErr
}
