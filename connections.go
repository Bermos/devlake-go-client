package devlake

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/Bermos/devlake-go-client/devlakeapi"
)

// Plugin is a DevLake data source plugin name as used in API paths and
// blueprints. Any plugin name works with the plugin-agnostic helpers; the
// constants cover the most common ones.
type Plugin string

// Well known plugins.
const (
	PluginJira      Plugin = "jira"
	PluginGitLab    Plugin = "gitlab"
	PluginGitHub    Plugin = "github"
	PluginSonarQube Plugin = "sonarqube"
	PluginWebhook   Plugin = "webhook"
	PluginBitbucket Plugin = "bitbucket"
	PluginAzure     Plugin = "azuredevops_go"
	PluginJenkins   Plugin = "jenkins"
	PluginPagerDuty Plugin = "pagerduty"
)

// Connection is the plugin-independent view of a data source connection.
// Raw holds the full JSON object as returned by DevLake (secrets are
// sanitized by DevLake).
type Connection struct {
	ID       int             `json:"id"`
	Name     string          `json:"name"`
	Endpoint string          `json:"endpoint,omitempty"`
	Raw      json.RawMessage `json:"-"`
}

// UnmarshalJSON decodes the common fields and keeps the raw object.
func (c *Connection) UnmarshalJSON(b []byte) error {
	type plain Connection
	if err := json.Unmarshal(b, (*plain)(c)); err != nil {
		return err
	}
	c.Raw = append(json.RawMessage(nil), b...)
	return nil
}

func connectionsPath(p Plugin) string { return "plugins/" + string(p) + "/connections" }

func connectionPath(p Plugin, id int) string {
	return connectionsPath(p) + "/" + strconv.Itoa(id)
}

// ListConnections returns all connections of a plugin.
func (c *Client) ListConnections(ctx context.Context, p Plugin) ([]Connection, error) {
	var out []Connection
	err := c.Do(ctx, http.MethodGet, connectionsPath(p), nil, nil, &out)
	return out, err
}

// GetConnection returns one connection. Missing connections yield an error
// matching ErrNotFound.
func (c *Client) GetConnection(ctx context.Context, p Plugin, id int) (*Connection, error) {
	var out Connection
	if err := c.Do(ctx, http.MethodGet, connectionPath(p, id), nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// FindConnection returns the connection of a plugin with the given name, or
// an error matching ErrNotFound.
func (c *Client) FindConnection(ctx context.Context, p Plugin, name string) (*Connection, error) {
	all, err := c.ListConnections(ctx, p)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].Name == name {
			return &all[i], nil
		}
	}
	return nil, fmt.Errorf("%s connection %q: %w", p, name, ErrNotFound)
}

// CreateConnection creates a connection from a plugin specific body, e.g. a
// devlakeapi.ModelsGitlabConnection or a map. Prefer the typed helpers
// (CreateGitLabConnection, ...) for the common plugins.
func (c *Client) CreateConnection(ctx context.Context, p Plugin, body any) (*Connection, error) {
	var out Connection
	if err := c.Do(ctx, http.MethodPost, connectionsPath(p), nil, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateConnection patches a connection with the given fields.
func (c *Client) UpdateConnection(ctx context.Context, p Plugin, id int, fields any) (*Connection, error) {
	var out Connection
	if err := c.Do(ctx, http.MethodPatch, connectionPath(p, id), nil, fields, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteConnection deletes a connection. DevLake refuses (HTTP 409/400) while
// the connection is still referenced by a blueprint.
func (c *Client) DeleteConnection(ctx context.Context, p Plugin, id int) error {
	return c.Do(ctx, http.MethodDelete, connectionPath(p, id), nil, nil, nil)
}

// TestConnection asks DevLake to verify an existing connection against the
// remote system (credentials, permissions, reachability).
func (c *Client) TestConnection(ctx context.Context, p Plugin, id int) error {
	return c.Do(ctx, http.MethodPost, connectionPath(p, id)+"/test", nil, nil, nil)
}

// GitLabConnection holds the settings for a GitLab connection.
type GitLabConnection struct {
	Name string
	// Endpoint defaults to https://gitlab.com/api/v4/.
	Endpoint string
	// Token is a personal/group access token with read_api scope.
	Token            string
	Proxy            string
	RateLimitPerHour int
}

// CreateGitLabConnection creates a GitLab connection.
func (c *Client) CreateGitLabConnection(ctx context.Context, in GitLabConnection) (*Connection, error) {
	body := devlakeapi.ModelsGitlabConnection{
		Name:     in.Name,
		Endpoint: withDefault(in.Endpoint, "https://gitlab.com/api/v4/"),
		Token:    in.Token,
		Proxy:    optional(in.Proxy),
	}
	if in.RateLimitPerHour > 0 {
		body.RateLimitPerHour = &in.RateLimitPerHour
	}
	return c.CreateConnection(ctx, PluginGitLab, body)
}

// JiraConnection holds the settings for a Jira connection. Set either Token
// (personal access token / Jira Server) or Username+Password (Jira Cloud:
// email + API token).
type JiraConnection struct {
	Name string
	// Endpoint is the REST base, e.g. https://example.atlassian.net/rest/.
	Endpoint         string
	Token            string
	Username         string
	Password         string
	Proxy            string
	RateLimitPerHour int
}

// CreateJiraConnection creates a Jira connection.
func (c *Client) CreateJiraConnection(ctx context.Context, in JiraConnection) (*Connection, error) {
	body := devlakeapi.ModelsJiraConnection{
		Name:     in.Name,
		Endpoint: in.Endpoint,
		Proxy:    optional(in.Proxy),
	}
	if in.Token != "" {
		body.AuthMethod = "AccessToken"
		body.Token = in.Token
	} else {
		body.AuthMethod = "BasicAuth"
		body.Username = in.Username
		body.Password = in.Password
	}
	if in.RateLimitPerHour > 0 {
		body.RateLimitPerHour = &in.RateLimitPerHour
	}
	return c.CreateConnection(ctx, PluginJira, body)
}

// SonarQubeConnection holds the settings for a SonarQube connection.
type SonarQubeConnection struct {
	Name string
	// Endpoint is the API base, e.g. https://sonar.example.com/api/.
	Endpoint string
	Token    string
	// Org is required for SonarCloud only.
	Org   string
	Proxy string
}

// CreateSonarQubeConnection creates a SonarQube or SonarCloud connection.
func (c *Client) CreateSonarQubeConnection(ctx context.Context, in SonarQubeConnection) (*Connection, error) {
	return c.CreateConnection(ctx, PluginSonarQube, devlakeapi.ModelsSonarqubeConnection{
		Name:     in.Name,
		Endpoint: in.Endpoint,
		Token:    in.Token,
		Org:      optional(in.Org),
		Proxy:    optional(in.Proxy),
	})
}

// GitHubConnection holds the settings for a GitHub connection using a
// personal access token. Several tokens can be given comma separated.
type GitHubConnection struct {
	Name string
	// Endpoint defaults to https://api.github.com/.
	Endpoint      string
	Token         string
	EnableGraphQL bool
	Proxy         string
}

// CreateGitHubConnection creates a GitHub connection (token auth).
func (c *Client) CreateGitHubConnection(ctx context.Context, in GitHubConnection) (*Connection, error) {
	return c.CreateConnection(ctx, PluginGitHub, devlakeapi.ModelsGithubConnection{
		Name:          in.Name,
		Endpoint:      withDefault(in.Endpoint, "https://api.github.com/"),
		AuthMethod:    "AccessToken",
		Token:         in.Token,
		EnableGraphql: &in.EnableGraphQL,
		Proxy:         optional(in.Proxy),
	})
}

func withDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
