//go:build integration

// Package integration runs the client against a real DevLake instance.
//
// Start one with `make devlake-up` (DevLake + PostgreSQL via docker compose)
// and run `make test-integration`. DEVLAKE_URL overrides the default
// http://localhost:8080.
package integration

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	devlake "github.com/Bermos/devlake-go-client"
	"github.com/Bermos/devlake-go-client/devlakeapi"
)

var baseURL = envOr("DEVLAKE_URL", "http://localhost:8080")

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func TestMain(m *testing.M) {
	if err := waitReady(5 * time.Minute); err != nil {
		fmt.Fprintf(os.Stderr, "DevLake at %s not ready: %v\n", baseURL, err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// waitReady blocks until DevLake finished starting (incl. migrations).
func waitReady(timeout time.Duration) error {
	c, err := devlake.New(baseURL)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	for {
		if err = c.Ready(ctx); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(2 * time.Second):
		}
	}
}

func newClient(t *testing.T, opts ...devlake.Option) *devlake.Client {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(testWriter{t}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	c, err := devlake.New(baseURL, append([]devlake.Option{
		devlake.WithLogger(logger),
		devlake.WithPollInterval(time.Second),
	}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

type response interface{ StatusCode() int }

func must(t *testing.T, resp response, err error) {
	t.Helper()
	if err := devlake.CheckResponse(resp, err); err != nil {
		t.Fatal(err)
	}
}

func uniq(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

func ptr[T any](v T) *T { return &v }

func TestHealth(t *testing.T) {
	ctx := context.Background()
	c := newClient(t)
	if err := c.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	v, err := c.Version(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("DevLake version %s", v)
	if want := os.Getenv("DEVLAKE_VERSION"); want != "" && !strings.HasPrefix(v, want) {
		t.Errorf("server version %q, want prefix %q", v, want)
	}
}

func TestProjectLifecycle(t *testing.T) {
	ctx := context.Background()
	c := newClient(t)
	api := c.API()
	name := uniq("it-project")

	created, err := api.PostProjectsWithResponse(ctx, devlakeapi.ModelsApiInputProject{
		Name:        name,
		Description: ptr("created by integration tests"),
		Enable:      ptr(true),
		Metrics: &[]devlakeapi.ModelsBaseMetric{
			{PluginName: "dora", Enable: ptr(true), PluginOption: &map[string]any{}},
		},
	})
	must(t, created, err)
	if created.JSON201 == nil || created.JSON201.Name != name {
		t.Fatalf("unexpected create response: %s", created.Body)
	}
	if created.JSON201.Blueprint == nil || created.JSON201.Blueprint.Id == nil {
		t.Fatal("project was created without a blueprint")
	}

	got, err := api.GetProjectsProjectNameWithResponse(ctx, name)
	must(t, got, err)
	if got.JSON200 == nil || got.JSON200.Description == nil || *got.JSON200.Description != "created by integration tests" {
		t.Errorf("unexpected project: %s", got.Body)
	}

	list, err := api.GetProjectsWithResponse(ctx, &devlakeapi.GetProjectsParams{PageSize: ptr(100)})
	must(t, list, err)
	found := false
	for _, p := range *list.JSON200.Projects {
		found = found || p.Name == name
	}
	if !found {
		t.Errorf("project %q not in list", name)
	}

	patched, err := api.PatchProjectsProjectNameWithResponse(ctx, name, devlakeapi.ModelsApiInputProject{
		Name:        name,
		Description: ptr("patched"),
	})
	must(t, patched, err)

	got, err = api.GetProjectsProjectNameWithResponse(ctx, name)
	must(t, got, err)
	if *got.JSON200.Description != "patched" {
		t.Errorf("description after patch = %q", *got.JSON200.Description)
	}

	del, err := api.DeleteProjectsProjectNameWithResponse(ctx, name)
	must(t, del, err)

	gone, err := api.GetProjectsProjectNameWithResponse(ctx, name)
	if err := devlake.CheckResponse(gone, err); !errors.Is(err, devlake.ErrNotFound) {
		t.Errorf("get after delete: want ErrNotFound, got %v", err)
	}
}

// connectionCase exercises create/get/list/patch/delete for one plugin.
type connectionCase struct {
	plugin string
	create func(ctx context.Context, api *devlakeapi.ClientWithResponses, name string) (id int, err error)
	get    func(ctx context.Context, api *devlakeapi.ClientWithResponses, id int) (name string, err error)
	list   func(ctx context.Context, api *devlakeapi.ClientWithResponses) (names []string, err error)
	rename func(ctx context.Context, api *devlakeapi.ClientWithResponses, id int, name string) error
	delete func(ctx context.Context, api *devlakeapi.ClientWithResponses, id int) error
}

func TestConnectionsCRUD(t *testing.T) {
	cases := []connectionCase{
		{
			plugin: "gitlab",
			create: func(ctx context.Context, api *devlakeapi.ClientWithResponses, name string) (int, error) {
				r, err := api.PostPluginsGitlabConnectionsWithResponse(ctx, devlakeapi.ModelsGitlabConnection{
					Name: name, Endpoint: "https://gitlab.example.com/api/v4/", Token: "glpat-test",
				})
				if err := devlake.CheckResponse(r, err); err != nil {
					return 0, err
				}
				return *firstNonNil(r.JSON200, r.JSON201).Id, nil
			},
			get: func(ctx context.Context, api *devlakeapi.ClientWithResponses, id int) (string, error) {
				r, err := api.GetPluginsGitlabConnectionsConnectionIdWithResponse(ctx, id)
				if err := devlake.CheckResponse(r, err); err != nil {
					return "", err
				}
				return r.JSON200.Name, nil
			},
			list: func(ctx context.Context, api *devlakeapi.ClientWithResponses) ([]string, error) {
				r, err := api.GetPluginsGitlabConnectionsWithResponse(ctx)
				if err := devlake.CheckResponse(r, err); err != nil {
					return nil, err
				}
				var names []string
				for _, c := range *r.JSON200 {
					names = append(names, c.Name)
				}
				return names, nil
			},
			rename: func(ctx context.Context, api *devlakeapi.ClientWithResponses, id int, name string) error {
				r, err := api.PatchPluginsGitlabConnectionsConnectionIdWithResponse(ctx, id, devlakeapi.ModelsGitlabConnection{
					Name: name, Endpoint: "https://gitlab.example.com/api/v4/", Token: "glpat-test",
				})
				return devlake.CheckResponse(r, err)
			},
			delete: func(ctx context.Context, api *devlakeapi.ClientWithResponses, id int) error {
				r, err := api.DeletePluginsGitlabConnectionsConnectionIdWithResponse(ctx, id)
				return devlake.CheckResponse(r, err)
			},
		},
		{
			plugin: "jira",
			create: func(ctx context.Context, api *devlakeapi.ClientWithResponses, name string) (int, error) {
				r, err := api.PostPluginsJiraConnectionsWithResponse(ctx, devlakeapi.ModelsJiraConnection{
					Name: name, Endpoint: "https://jira.example.com/rest/", AuthMethod: "AccessToken", Token: "jira-test",
				})
				if err := devlake.CheckResponse(r, err); err != nil {
					return 0, err
				}
				return *firstNonNil(r.JSON200, r.JSON201).Id, nil
			},
			get: func(ctx context.Context, api *devlakeapi.ClientWithResponses, id int) (string, error) {
				r, err := api.GetPluginsJiraConnectionsConnectionIdWithResponse(ctx, id)
				if err := devlake.CheckResponse(r, err); err != nil {
					return "", err
				}
				return r.JSON200.Name, nil
			},
			list: func(ctx context.Context, api *devlakeapi.ClientWithResponses) ([]string, error) {
				r, err := api.GetPluginsJiraConnectionsWithResponse(ctx)
				if err := devlake.CheckResponse(r, err); err != nil {
					return nil, err
				}
				var names []string
				for _, c := range *r.JSON200 {
					names = append(names, c.Name)
				}
				return names, nil
			},
			rename: func(ctx context.Context, api *devlakeapi.ClientWithResponses, id int, name string) error {
				r, err := api.PatchPluginsJiraConnectionsConnectionIdWithResponse(ctx, id, devlakeapi.ModelsJiraConnection{
					Name: name, Endpoint: "https://jira.example.com/rest/", AuthMethod: "AccessToken", Token: "jira-test",
				})
				return devlake.CheckResponse(r, err)
			},
			delete: func(ctx context.Context, api *devlakeapi.ClientWithResponses, id int) error {
				r, err := api.DeletePluginsJiraConnectionsConnectionIdWithResponse(ctx, id)
				return devlake.CheckResponse(r, err)
			},
		},
		{
			plugin: "sonarqube",
			create: func(ctx context.Context, api *devlakeapi.ClientWithResponses, name string) (int, error) {
				r, err := api.PostPluginsSonarqubeConnectionsWithResponse(ctx, devlakeapi.ModelsSonarqubeConnection{
					Name: name, Endpoint: "https://sonar.example.com/api/", Token: "squ_test",
				})
				if err := devlake.CheckResponse(r, err); err != nil {
					return 0, err
				}
				return *firstNonNil(r.JSON200, r.JSON201).Id, nil
			},
			get: func(ctx context.Context, api *devlakeapi.ClientWithResponses, id int) (string, error) {
				r, err := api.GetPluginsSonarqubeConnectionsConnectionIdWithResponse(ctx, id)
				if err := devlake.CheckResponse(r, err); err != nil {
					return "", err
				}
				return r.JSON200.Name, nil
			},
			list: func(ctx context.Context, api *devlakeapi.ClientWithResponses) ([]string, error) {
				r, err := api.GetPluginsSonarqubeConnectionsWithResponse(ctx)
				if err := devlake.CheckResponse(r, err); err != nil {
					return nil, err
				}
				var names []string
				for _, c := range *r.JSON200 {
					names = append(names, c.Name)
				}
				return names, nil
			},
			rename: func(ctx context.Context, api *devlakeapi.ClientWithResponses, id int, name string) error {
				r, err := api.PatchPluginsSonarqubeConnectionsConnectionIdWithResponse(ctx, id, devlakeapi.ModelsSonarqubeConnection{
					Name: name, Endpoint: "https://sonar.example.com/api/", Token: "squ_test",
				})
				return devlake.CheckResponse(r, err)
			},
			delete: func(ctx context.Context, api *devlakeapi.ClientWithResponses, id int) error {
				r, err := api.DeletePluginsSonarqubeConnectionsConnectionIdWithResponse(ctx, id)
				return devlake.CheckResponse(r, err)
			},
		},
		{
			plugin: "github",
			create: func(ctx context.Context, api *devlakeapi.ClientWithResponses, name string) (int, error) {
				r, err := api.PostPluginsGithubConnectionsWithResponse(ctx, devlakeapi.ModelsGithubConnection{
					Name: name, Endpoint: "https://api.github.com/", AuthMethod: "AccessToken", Token: fakeGitHubToken,
				})
				if err := devlake.CheckResponse(r, err); err != nil {
					return 0, err
				}
				return *firstNonNil(r.JSON200, r.JSON201).Id, nil
			},
			get: func(ctx context.Context, api *devlakeapi.ClientWithResponses, id int) (string, error) {
				r, err := api.GetPluginsGithubConnectionsConnectionIdWithResponse(ctx, id)
				if err := devlake.CheckResponse(r, err); err != nil {
					return "", err
				}
				return r.JSON200.Name, nil
			},
			list: func(ctx context.Context, api *devlakeapi.ClientWithResponses) ([]string, error) {
				r, err := api.GetPluginsGithubConnectionsWithResponse(ctx)
				if err := devlake.CheckResponse(r, err); err != nil {
					return nil, err
				}
				var names []string
				for _, c := range *r.JSON200 {
					names = append(names, c.Name)
				}
				return names, nil
			},
			rename: func(ctx context.Context, api *devlakeapi.ClientWithResponses, id int, name string) error {
				r, err := api.PatchPluginsGithubConnectionsConnectionIdWithResponse(ctx, id, devlakeapi.ModelsGithubConnection{
					Name: name, Endpoint: "https://api.github.com/", AuthMethod: "AccessToken", Token: fakeGitHubToken,
				})
				return devlake.CheckResponse(r, err)
			},
			delete: func(ctx context.Context, api *devlakeapi.ClientWithResponses, id int) error {
				r, err := api.DeletePluginsGithubConnectionsConnectionIdWithResponse(ctx, id)
				return devlake.CheckResponse(r, err)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.plugin, func(t *testing.T) {
			ctx := context.Background()
			api := newClient(t).API()
			name := uniq("it-" + tc.plugin)

			id, err := tc.create(ctx, api, name)
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			if got, err := tc.get(ctx, api, id); err != nil || got != name {
				t.Fatalf("get: name=%q err=%v", got, err)
			}
			names, err := tc.list(ctx, api)
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if !contains(names, name) {
				t.Errorf("list does not contain %q", name)
			}
			renamed := name + "-renamed"
			if err := tc.rename(ctx, api, id, renamed); err != nil {
				t.Fatalf("patch: %v", err)
			}
			if got, err := tc.get(ctx, api, id); err != nil || got != renamed {
				t.Errorf("get after patch: name=%q err=%v", got, err)
			}
			if err := tc.delete(ctx, api, id); err != nil {
				t.Fatalf("delete: %v", err)
			}
			if _, err := tc.get(ctx, api, id); !errors.Is(err, devlake.ErrNotFound) {
				t.Errorf("get after delete: want ErrNotFound, got %v", err)
			}
		})
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func firstNonNil[T any](vs ...*T) *T {
	for _, v := range vs {
		if v != nil {
			return v
		}
	}
	var zero T
	return &zero
}

// fakeGitHubToken must be at least 32 characters: DevLake's GitHub plugin
// panics while sanitizing shorter tokens (v1.0.3-beta19).
const fakeGitHubToken = "ghp_000000000000000000000000000000000000"
