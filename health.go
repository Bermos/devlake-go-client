package devlake

import (
	"context"
	"errors"
)

// Ping checks that the DevLake API is reachable.
func (c *Client) Ping(ctx context.Context) error {
	resp, err := c.api.GetPingWithResponse(ctx)
	return CheckResponse(resp, err)
}

// Ready reports whether DevLake finished starting up, including database
// migrations. It returns nil when DevLake is ready to accept work.
func (c *Client) Ready(ctx context.Context) error {
	resp, err := c.api.GetReadyWithResponse(ctx)
	return CheckResponse(resp, err)
}

// Version returns the version string reported by the DevLake server, e.g.
// "v1.0.3-beta19@abc1234".
func (c *Client) Version(ctx context.Context) (string, error) {
	resp, err := c.api.GetVersionWithResponse(ctx)
	if err := CheckResponse(resp, err); err != nil {
		return "", err
	}
	if resp.JSON200 == nil || resp.JSON200.Version == nil {
		return "", errors.New("devlake: version missing from response")
	}
	return *resp.JSON200.Version, nil
}
