package devlake

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// RemoteScope is something DevLake can collect from a connection: a GitLab
// project, GitHub repository, Jira board, SonarQube project, ...
type RemoteScope struct {
	// ID is the scope ID used in blueprints (e.g. GitLab project ID, Jira
	// board ID, SonarQube project key).
	ID       string `json:"id"`
	Name     string `json:"name"`
	FullName string `json:"fullName"`
	// Type is "scope" for collectable items and "group" for containers.
	Type     string          `json:"type"`
	ParentID string          `json:"parentId"`
	Data     json.RawMessage `json:"data"`
}

// ErrAmbiguousScope is returned by AddScopesByName when a name matches more
// than one remote scope.
var ErrAmbiguousScope = errors.New("devlake: ambiguous scope name")

// SearchRemoteScopes searches the remote system behind a connection, e.g.
// GitLab projects or Jira boards whose name contains query.
func (c *Client) SearchRemoteScopes(ctx context.Context, p Plugin, connectionID int, query string) ([]RemoteScope, error) {
	var all []RemoteScope
	const pageSize = 100
	for page := 1; ; page++ {
		q := url.Values{
			"search":   {query},
			"page":     {strconv.Itoa(page)},
			"pageSize": {strconv.Itoa(pageSize)},
		}
		var out struct {
			Children []RemoteScope `json:"children"`
		}
		if err := c.Do(ctx, http.MethodGet, connectionPath(p, connectionID)+"/search-remote-scopes", q, nil, &out); err != nil {
			return nil, err
		}
		all = append(all, out.Children...)
		if len(out.Children) < pageSize || page >= 20 {
			return all, nil
		}
	}
}

// ScopeOptions configures scopes added with AddScopes.
type ScopeOptions struct {
	// ScopeConfigID applies a scope config (transformation rules) to every
	// added scope. 0 leaves it unset.
	ScopeConfigID int
}

// AddScopes registers remote scopes (as returned by SearchRemoteScopes) on a
// connection so they can be added to blueprints. Existing scopes are updated.
func (c *Client) AddScopes(ctx context.Context, p Plugin, connectionID int, scopes []RemoteScope, opts ScopeOptions) error {
	data := make([]map[string]any, 0, len(scopes))
	for _, s := range scopes {
		if s.Type != "" && s.Type != "scope" {
			return fmt.Errorf("devlake: %q is a %s, not a scope", s.FullName, s.Type)
		}
		var m map[string]any
		if err := json.Unmarshal(s.Data, &m); err != nil || m == nil {
			return fmt.Errorf("devlake: scope %q has no data: %w", s.FullName, err)
		}
		m["connectionId"] = connectionID
		if opts.ScopeConfigID > 0 {
			m["scopeConfigId"] = opts.ScopeConfigID
		}
		data = append(data, m)
	}
	return c.Do(ctx, http.MethodPut, connectionPath(p, connectionID)+"/scopes", nil, map[string]any{"data": data}, nil)
}

// AddScopesByName finds scopes by name and registers them on the
// connection. A name matches a scope's full name (e.g. "group/project" for
// GitLab, "owner/repo" for GitHub) or, if no full name matches, its short
// name; matching is case insensitive. It returns the added scopes in input
// order. Unknown names yield ErrNotFound, ambiguous ones ErrAmbiguousScope.
func (c *Client) AddScopesByName(ctx context.Context, p Plugin, connectionID int, opts ScopeOptions, names ...string) ([]RemoteScope, error) {
	found := make([]RemoteScope, 0, len(names))
	for _, name := range names {
		s, err := c.findRemoteScope(ctx, p, connectionID, name)
		if err != nil {
			return nil, err
		}
		found = append(found, *s)
	}
	if len(found) == 0 {
		return nil, nil
	}
	if err := c.AddScopes(ctx, p, connectionID, found, opts); err != nil {
		return nil, err
	}
	return found, nil
}

func (c *Client) findRemoteScope(ctx context.Context, p Plugin, connectionID int, name string) (*RemoteScope, error) {
	// Search by the last path segment: remote searches match names, not paths.
	term := name
	if i := strings.LastIndex(term, "/"); i >= 0 {
		term = term[i+1:]
	}
	candidates, err := c.SearchRemoteScopes(ctx, p, connectionID, term)
	if err != nil {
		return nil, err
	}
	var byFull, byName []RemoteScope
	for _, s := range candidates {
		if s.Type != "" && s.Type != "scope" {
			continue
		}
		switch {
		case strings.EqualFold(s.FullName, name):
			byFull = append(byFull, s)
		case strings.EqualFold(s.Name, name):
			byName = append(byName, s)
		}
	}
	matches := byFull
	if len(matches) == 0 {
		matches = byName
	}
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("%s scope %q: %w", p, name, ErrNotFound)
	case 1:
		return &matches[0], nil
	default:
		return nil, fmt.Errorf("%s scope %q matches %d scopes: %w", p, name, len(matches), ErrAmbiguousScope)
	}
}

// Scope is a scope registered on a connection.
type Scope struct {
	// Raw is the plugin specific scope object (e.g. a GitLab project).
	Raw json.RawMessage
	// Blueprints lists the names of blueprints using this scope.
	Blueprints []string
}

// ListScopes returns the scopes registered on a connection.
func (c *Client) ListScopes(ctx context.Context, p Plugin, connectionID int) ([]Scope, error) {
	var all []Scope
	const pageSize = 100
	for page := 1; ; page++ {
		q := url.Values{"page": {strconv.Itoa(page)}, "pageSize": {strconv.Itoa(pageSize)}, "blueprints": {"true"}}
		var out struct {
			Count  int `json:"count"`
			Scopes []struct {
				Scope      json.RawMessage `json:"scope"`
				Blueprints []struct {
					Name string `json:"name"`
				} `json:"blueprints"`
			} `json:"scopes"`
		}
		if err := c.Do(ctx, http.MethodGet, connectionPath(p, connectionID)+"/scopes", q, nil, &out); err != nil {
			return nil, err
		}
		for _, s := range out.Scopes {
			sc := Scope{Raw: s.Scope}
			for _, b := range s.Blueprints {
				sc.Blueprints = append(sc.Blueprints, b.Name)
			}
			all = append(all, sc)
		}
		if len(out.Scopes) < pageSize || len(all) >= out.Count {
			return all, nil
		}
	}
}
