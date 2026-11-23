package devlake_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	devlake "github.com/Bermos/devlake-go-client"
)

// fakeDevLake is a tiny in-memory stand-in for the DevLake endpoints the
// helpers use. Handlers are registered per test.
type fakeDevLake struct {
	mu       sync.Mutex
	mux      *http.ServeMux
	requests []string
	bodies   map[string]string
}

func newFake(t *testing.T) (*fakeDevLake, *devlake.Client) {
	t.Helper()
	f := &fakeDevLake{mux: http.NewServeMux(), bodies: map[string]string{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		key := r.Method + " " + r.URL.Path
		f.requests = append(f.requests, key)
		f.bodies[key] = string(b)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		f.mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	c, err := devlake.New(srv.URL, devlake.WithPollInterval(time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	return f, c
}

func (f *fakeDevLake) body(key string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bodies[key]
}

func jsonHandler(v any) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(v) }
}

const projectJSON = `{"name":"p","blueprint":{"id":7,"name":"p-Blueprint","mode":"NORMAL",
 "connections":[{"pluginName":"gitlab","connectionId":1,"scopes":[{"scopeId":"101"}]}]}}`

func TestConnectProjectMergesScopes(t *testing.T) {
	f, c := newFake(t)
	f.mux.HandleFunc("GET /projects/p", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(projectJSON)) })
	f.mux.HandleFunc("PATCH /blueprints/7", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{}`)) })

	if err := c.ConnectProject(context.Background(), "p", devlake.PluginGitLab, 1, "101", "102"); err != nil {
		t.Fatal(err)
	}
	if err := c.ConnectProject(context.Background(), "p", devlake.PluginJira, 2, "55"); err != nil {
		t.Fatal(err)
	}
	var patch struct {
		Connections []struct {
			PluginName   string
			ConnectionID int `json:"connectionId"`
			Scopes       []struct {
				ScopeID string `json:"scopeId"`
			}
		}
	}
	// The second call starts from the (unchanged) fake project again, so it
	// must contain the original gitlab connection plus the new jira one.
	if err := json.Unmarshal([]byte(f.body("PATCH /blueprints/7")), &patch); err != nil {
		t.Fatal(err)
	}
	if len(patch.Connections) != 2 || patch.Connections[1].PluginName != "jira" || patch.Connections[1].Scopes[0].ScopeID != "55" {
		t.Errorf("unexpected patch: %s", f.body("PATCH /blueprints/7"))
	}
	if len(patch.Connections[0].Scopes) != 1 {
		t.Errorf("existing scopes changed: %s", f.body("PATCH /blueprints/7"))
	}
}

func TestWaitForPipeline(t *testing.T) {
	f, c := newFake(t)
	var calls int
	f.mux.HandleFunc("GET /pipelines/{id}", func(w http.ResponseWriter, r *http.Request) {
		calls++
		status := devlake.StatusRunning
		if calls >= 3 {
			status = devlake.StatusCompleted
			if r.PathValue("id") == "2" {
				status = devlake.StatusFailed
			}
		}
		_, _ = w.Write([]byte(`{"id":` + r.PathValue("id") + `,"status":"` + status + `","message":"boom"}`))
	})

	p, err := c.WaitForPipeline(context.Background(), 1)
	if err != nil || *p.Status != devlake.StatusCompleted {
		t.Fatalf("pipeline 1: %v, %v", p, err)
	}
	calls = 0
	_, err = c.WaitForPipeline(context.Background(), 2)
	var pe *devlake.PipelineError
	if !errors.As(err, &pe) || *pe.Pipeline.Id != 2 || !strings.Contains(err.Error(), "boom") {
		t.Errorf("pipeline 2: want PipelineError, got %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls = -100
	if _, err := c.WaitForPipeline(ctx, 3); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled wait: got %v", err)
	}
}

func TestAddScopesByName(t *testing.T) {
	f, c := newFake(t)
	f.mux.HandleFunc("GET /plugins/gitlab/connections/1/search-remote-scopes", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("search"); !strings.EqualFold(got, "alpha") {
			t.Errorf("search term = %q, want last path segment", got)
		}
		_, _ = w.Write([]byte(`{"children":[
			{"type":"scope","id":"101","name":"alpha","fullName":"platform/alpha","data":{"gitlabId":101,"name":"alpha"}},
			{"type":"scope","id":"102","name":"alpha","fullName":"sandbox/alpha","data":{"gitlabId":102,"name":"alpha"}},
			{"type":"group","id":"9","name":"alpha","fullName":"alpha"}]}`))
	})
	f.mux.HandleFunc("PUT /plugins/gitlab/connections/1/scopes", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`[]`)) })

	got, err := c.AddScopesByName(context.Background(), devlake.PluginGitLab, 1, devlake.ScopeOptions{ScopeConfigID: 4}, "Platform/Alpha")
	if err != nil || len(got) != 1 || got[0].ID != "101" {
		t.Fatalf("got %+v, %v", got, err)
	}
	var put struct{ Data []map[string]any }
	_ = json.Unmarshal([]byte(f.body("PUT /plugins/gitlab/connections/1/scopes")), &put)
	if len(put.Data) != 1 || put.Data[0]["gitlabId"] != float64(101) || put.Data[0]["connectionId"] != float64(1) || put.Data[0]["scopeConfigId"] != float64(4) {
		t.Errorf("PUT body = %s", f.body("PUT /plugins/gitlab/connections/1/scopes"))
	}

	if _, err := c.AddScopesByName(context.Background(), devlake.PluginGitLab, 1, devlake.ScopeOptions{}, "alpha"); !errors.Is(err, devlake.ErrAmbiguousScope) {
		t.Errorf("ambiguous: got %v", err)
	}
	if _, err := c.AddScopesByName(context.Background(), devlake.PluginGitLab, 1, devlake.ScopeOptions{}, "x/alpha"); !errors.Is(err, devlake.ErrNotFound) {
		t.Errorf("missing: got %v", err)
	}
}

func TestProjectStatusesFlagsProblems(t *testing.T) {
	f, c := newFake(t)
	old := time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339)
	f.mux.HandleFunc("GET /projects", jsonHandler(map[string]any{"count": 4, "projects": []any{
		map[string]any{"name": "ok", "blueprint": map[string]any{"id": 1, "name": "a", "mode": "NORMAL", "enable": true, "connections": []any{map[string]any{"pluginName": "jira", "connectionId": 1}}}},
		map[string]any{"name": "stale", "blueprint": map[string]any{"id": 2, "name": "b", "mode": "NORMAL", "enable": true, "connections": []any{map[string]any{"pluginName": "jira", "connectionId": 1}}}},
		map[string]any{"name": "failed", "blueprint": map[string]any{"id": 3, "name": "c", "mode": "NORMAL", "enable": true, "connections": []any{map[string]any{"pluginName": "jira", "connectionId": 1}}}},
		map[string]any{"name": "empty", "blueprint": map[string]any{"id": 4, "name": "d", "mode": "NORMAL", "enable": true}},
	}}))
	f.mux.HandleFunc("GET /pipelines", func(w http.ResponseWriter, r *http.Request) {
		now := time.Now().UTC().Format(time.RFC3339)
		var pls []any
		switch r.URL.Query().Get("blueprint_id") {
		case "1":
			pls = []any{map[string]any{"id": 10, "status": "TASK_COMPLETED", "finishedAt": now}}
		case "2":
			pls = []any{map[string]any{"id": 20, "status": "TASK_COMPLETED", "finishedAt": old}}
		case "3":
			pls = []any{map[string]any{"id": 31, "status": "TASK_FAILED", "message": "401 from jira"}, map[string]any{"id": 30, "status": "TASK_COMPLETED", "finishedAt": now}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"count": len(pls), "pipelines": pls})
	})

	st, err := c.ProjectStatuses(context.Background(), 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	problems := map[string]string{}
	for _, s := range st {
		problems[s.Project] = s.Problem
	}
	if problems["ok"] != "" {
		t.Errorf("ok flagged: %q", problems["ok"])
	}
	if !strings.HasPrefix(problems["stale"], "last successful sync") {
		t.Errorf("stale: %q", problems["stale"])
	}
	if !strings.Contains(problems["failed"], "401 from jira") {
		t.Errorf("failed: %q", problems["failed"])
	}
	if problems["empty"] != "no data connections" {
		t.Errorf("empty: %q", problems["empty"])
	}
}

func TestEnsureProjectCreatesWhenMissing(t *testing.T) {
	f, c := newFake(t)
	created := false
	f.mux.HandleFunc("GET /projects/new", func(w http.ResponseWriter, _ *http.Request) {
		if !created {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"success":false,"message":"not found"}`))
			return
		}
		_, _ = w.Write([]byte(`{"name":"new","blueprint":{"id":3,"name":"x","mode":"NORMAL","cronConfig":"0 0 * * *"}}`))
	})
	f.mux.HandleFunc("POST /projects", func(w http.ResponseWriter, _ *http.Request) {
		created = true
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"name":"new"}`))
	})
	f.mux.HandleFunc("PATCH /blueprints/3", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{}`)) })

	if _, err := c.EnsureProject(context.Background(), devlake.ProjectSpec{Name: "new", Cron: "@hourly"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.body("POST /projects"), `"pluginName":"dora"`) {
		t.Errorf("DORA not enabled by default: %s", f.body("POST /projects"))
	}
	if !strings.Contains(f.body("PATCH /blueprints/3"), `"cronConfig":"@hourly"`) {
		t.Errorf("cron not applied: %s", f.body("PATCH /blueprints/3"))
	}
}
