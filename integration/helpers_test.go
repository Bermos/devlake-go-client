//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	devlake "github.com/Bermos/devlake-go-client"
)

// TestConvenienceProjectFlow exercises the high level helpers end to end:
// webhook connection, project provisioning, pushing DORA data, syncing and
// the operator views.
func TestConvenienceProjectFlow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	c := newClient(t)
	name := uniq("it-helpers")

	hook, err := c.CreateWebhookConnection(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	if hook.APIKey == "" || hook.DeploymentsEndpoint == "" {
		t.Errorf("incomplete webhook connection: %+v", hook)
	}
	if found, err := c.FindConnection(ctx, devlake.PluginWebhook, name); err != nil || found.ID != hook.ID {
		t.Fatalf("FindConnection: %+v, %v", found, err)
	}

	spec := devlake.ProjectSpec{Name: name, Description: "helpers", Cron: "0 3 * * *"}
	if _, err := c.EnsureProject(ctx, spec); err != nil {
		t.Fatal(err)
	}
	spec.Description = "helpers v2"
	proj, err := c.EnsureProject(ctx, spec) // idempotent update
	if err != nil {
		t.Fatal(err)
	}
	if proj.Description == nil || *proj.Description != "helpers v2" || proj.Blueprint == nil || *proj.Blueprint.CronConfig != "0 3 * * *" {
		t.Errorf("EnsureProject did not converge: %+v", proj)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_ = c.DeleteProject(ctx, name)
		_ = c.DeleteConnection(ctx, devlake.PluginWebhook, hook.ID)
	})

	if err := c.ConnectProject(ctx, name, devlake.PluginWebhook, hook.ID); err != nil {
		t.Fatal(err)
	}
	if err := c.ConnectProject(ctx, name, devlake.PluginWebhook, hook.ID); err != nil { // no duplicates
		t.Fatal(err)
	}
	proj, err = c.GetProject(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(*proj.Blueprint.Connections); n != 1 {
		t.Errorf("blueprint has %d connections, want 1", n)
	}

	// Push data the way a CI system would, authenticated with the hook's key.
	sender := newClient(t, devlake.WithAPIKey(hook.APIKey)).Webhook(hook.ID)
	now := time.Now()
	if err := sender.Deployment(ctx, devlake.Deployment{
		ID:         uniq("deploy"),
		StartedAt:  now.Add(-10 * time.Minute),
		FinishedAt: now.Add(-8 * time.Minute),
		Commits:    []devlake.DeploymentCommit{{SHA: "89abcdef0123456789abcdef0123456789abcdef", RepoURL: "https://gitlab.example.com/g/r"}},
	}); err != nil {
		t.Fatal(err)
	}
	incident := uniq("INC")
	if err := sender.Incident(ctx, devlake.Incident{Key: incident, Title: "db down", CreatedAt: now.Add(-5 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err := sender.CloseIncident(ctx, incident); err != nil {
		t.Fatal(err)
	}

	pl, err := c.SyncProject(ctx, name, devlake.SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := c.PipelineTasks(ctx, *pl.Id)
	if err != nil || len(tasks) == 0 {
		t.Fatalf("PipelineTasks: %d tasks, %v", len(tasks), err)
	}
	if failed, err := c.FailedTasks(ctx, *pl.Id); err != nil || len(failed) != 0 {
		t.Errorf("FailedTasks: %v, %v", failed, err)
	}
	var logs bytes.Buffer
	if err := c.DownloadPipelineLogs(ctx, *pl.Id, &logs); err != nil {
		t.Logf("DownloadPipelineLogs: %v (log archive may be unavailable in this setup)", err)
	}

	statuses, err := c.ProjectStatuses(ctx, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	var mine *devlake.ProjectStatus
	for i := range statuses {
		if statuses[i].Project == name {
			mine = &statuses[i]
		}
	}
	if mine == nil || !mine.Healthy() || mine.LastSuccess.IsZero() || mine.Connections != 1 {
		t.Errorf("unexpected project status: %+v", mine)
	}

	st, err := c.Status(ctx)
	if err != nil || !st.Ready || st.Version == "" {
		t.Errorf("Status: %+v, %v", st, err)
	}

	if err := c.DisconnectProject(ctx, name, devlake.PluginWebhook, hook.ID); err != nil {
		t.Fatal(err)
	}
	bad, err := c.UnhealthyProjects(ctx, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	flagged := false
	for _, s := range bad {
		flagged = flagged || (s.Project == name && s.Problem == "no data connections")
	}
	if !flagged {
		t.Errorf("project without connections not flagged: %+v", bad)
	}
}

// TestScopesByNameWithFakeGitLab points a GitLab connection at a fake GitLab
// API served by the test, so scope search and registration run for real
// inside DevLake without a GitLab instance.
func TestScopesByNameWithFakeGitLab(t *testing.T) {
	ctx := context.Background()
	c := newClient(t)

	fake := startFakeGitLab(t)
	conn, err := c.CreateGitLabConnection(ctx, devlake.GitLabConnection{
		Name: uniq("it-fake-gitlab"), Endpoint: fake + "/api/v4/", Token: "glpat-fake",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.DeleteConnection(context.Background(), devlake.PluginGitLab, conn.ID) })

	results, err := c.SearchRemoteScopes(ctx, devlake.PluginGitLab, conn.ID, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("search returned %d scopes, want 2: %+v", len(results), results)
	}

	added, err := c.AddScopesByName(ctx, devlake.PluginGitLab, conn.ID, devlake.ScopeOptions{}, "platform/alpha")
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 1 || added[0].ID != "101" {
		t.Fatalf("added %+v, want project 101", added)
	}
	if _, err := c.AddScopesByName(ctx, devlake.PluginGitLab, conn.ID, devlake.ScopeOptions{}, "alpha"); !errors.Is(err, devlake.ErrAmbiguousScope) {
		t.Errorf("short name matching two projects: want ErrAmbiguousScope, got %v", err)
	}
	if _, err := c.AddScopesByName(ctx, devlake.PluginGitLab, conn.ID, devlake.ScopeOptions{}, "platform/missing"); !errors.Is(err, devlake.ErrNotFound) {
		t.Errorf("unknown project: want ErrNotFound, got %v", err)
	}

	scopes, err := c.ListScopes(ctx, devlake.PluginGitLab, conn.ID)
	if err != nil || len(scopes) != 1 {
		t.Fatalf("ListScopes: %d scopes, %v", len(scopes), err)
	}

	name := uniq("it-scopes")
	if _, err := c.EnsureProject(ctx, devlake.ProjectSpec{Name: name}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.DeleteProject(context.Background(), name) })
	if err := c.ConnectProject(ctx, name, devlake.PluginGitLab, conn.ID, added[0].ID); err != nil {
		t.Fatal(err)
	}
	proj, err := c.GetProject(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	bc := (*proj.Blueprint.Connections)[0]
	if bc.PluginName != "gitlab" || bc.Scopes == nil || (*bc.Scopes)[0].ScopeId != "101" {
		t.Errorf("blueprint connection = %+v", bc)
	}
}

// startFakeGitLab serves the subset of the GitLab API DevLake uses for
// project search. It listens on all interfaces so the DevLake container can
// reach it via FAKE_API_HOST (default host.docker.internal).
func startFakeGitLab(t *testing.T) string {
	t.Helper()
	projects := []map[string]any{
		{"id": 101, "name": "alpha", "path_with_namespace": "platform/alpha", "web_url": "https://gitlab.example.com/platform/alpha", "default_branch": "main", "http_url_to_repo": "https://gitlab.example.com/platform/alpha.git"},
		{"id": 102, "name": "alpha", "path_with_namespace": "sandbox/alpha", "web_url": "https://gitlab.example.com/sandbox/alpha", "default_branch": "main", "http_url_to_repo": "https://gitlab.example.com/sandbox/alpha.git"},
		{"id": 103, "name": "beta", "path_with_namespace": "platform/beta", "web_url": "https://gitlab.example.com/platform/beta", "default_branch": "main", "http_url_to_repo": "https://gitlab.example.com/platform/beta.git"},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects", func(w http.ResponseWriter, r *http.Request) {
		search := r.URL.Query().Get("search")
		var out []map[string]any
		for _, p := range projects {
			if strings.Contains(p["name"].(string), search) {
				out = append(out, p)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("/api/v4/version", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"17.5.0","revision":"fake"}`))
	})
	mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":1,"username":"fake"}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Logf("fake gitlab: unhandled %s %s", r.Method, r.URL)
		http.NotFound(w, r)
	})

	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return fmt.Sprintf("http://%s:%d", envOr("FAKE_API_HOST", "host.docker.internal"), ln.Addr().(*net.TCPAddr).Port)
}
