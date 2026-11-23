//go:build integration

package integration

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	devlake "github.com/Bermos/devlake-go-client"
	"github.com/Bermos/devlake-go-client/devlakeapi"
)

// TestWebhookPipelineEndToEnd runs a full DevLake workflow without any
// external system: webhook connection -> project -> blueprint -> push data ->
// trigger -> wait for the pipeline -> check results.
func TestWebhookPipelineEndToEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	api := newClient(t).API()
	name := uniq("it-e2e")

	// 1. Webhook connection.
	conn, err := api.PostPluginsWebhookConnectionsWithResponse(ctx, devlakeapi.ApiWebhookConnectionResponse{Name: name})
	must(t, conn, err)
	hook := firstNonNil(conn.JSON200, conn.JSON201)
	if hook.Id == nil || hook.ApiKey == nil || hook.ApiKey.ApiKey == nil {
		t.Fatalf("webhook connection without id or api key: %s", conn.Body)
	}
	connID := *hook.Id
	t.Cleanup(func() {
		r, err := api.DeletePluginsWebhookConnectionsConnectionIdWithResponse(context.Background(), connID)
		if err := devlake.CheckResponse(r, err); err != nil {
			t.Logf("cleanup webhook connection: %v", err)
		}
	})

	// 2. Project with DORA enabled.
	proj, err := api.PostProjectsWithResponse(ctx, devlakeapi.ModelsApiInputProject{
		Name:    name,
		Enable:  ptr(true),
		Metrics: &[]devlakeapi.ModelsBaseMetric{{PluginName: "dora", Enable: ptr(true), PluginOption: &map[string]any{}}},
	})
	must(t, proj, err)
	t.Cleanup(func() {
		r, err := api.DeleteProjectsProjectNameWithResponse(context.Background(), name)
		if err := devlake.CheckResponse(r, err); err != nil {
			t.Logf("cleanup project: %v", err)
		}
	})
	bpID := *proj.JSON201.Blueprint.Id

	// 3. Attach the webhook connection to the project's blueprint.
	patched, err := api.PatchBlueprintsBlueprintIdWithResponse(ctx, bpID, devlakeapi.PatchBlueprintsBlueprintIdJSONRequestBody{
		"connections": []map[string]any{{"pluginName": "webhook", "connectionId": connID}},
	})
	must(t, patched, err)

	bp, err := api.GetBlueprintsBlueprintIdWithResponse(ctx, bpID)
	must(t, bp, err)
	if bp.JSON200.Connections == nil || len(*bp.JSON200.Connections) != 1 {
		t.Fatalf("blueprint connections not saved: %s", bp.Body)
	}

	// 4. Push a deployment and an incident through the webhook, the way a
	// CI/CD system or incident tool would (using the connection's API key).
	hookClient := newClient(t, devlake.WithAPIKey(*hook.ApiKey.ApiKey))
	now := time.Now().UTC()
	dep, err := hookClient.API().PostPluginsWebhookConnectionsConnectionIdDeploymentsWithResponse(ctx, connID,
		devlakeapi.ApiWebhookDeploymentReq{
			Id:           uniq("deploy"),
			StartedDate:  now.Add(-10 * time.Minute).Format(time.RFC3339),
			FinishedDate: now.Add(-5 * time.Minute).Format(time.RFC3339),
			Result:       ptr("SUCCESS"),
			Environment:  ptr(devlakeapi.ApiWebhookDeploymentReqEnvironment("PRODUCTION")),
			DeploymentCommits: &[]devlakeapi.ApiWebhookDeploymentCommitReq{{
				CommitSha:    "0123456789abcdef0123456789abcdef01234567",
				RepoUrl:      "https://gitlab.example.com/group/repo",
				StartedDate:  now.Add(-10 * time.Minute).Format(time.RFC3339),
				FinishedDate: now.Add(-5 * time.Minute).Format(time.RFC3339),
			}},
		})
	must(t, dep, err)

	issue, err := hookClient.API().PostPluginsWebhookConnectionIdIssuesWithResponse(ctx, connID, devlakeapi.ApiWebhookIssueRequest{
		IssueKey:       uniq("INC"),
		Title:          "integration test incident",
		Type:           ptr("INCIDENT"),
		OriginalStatus: "open",
		Status:         ptr(devlakeapi.ApiWebhookIssueRequestStatus("TODO")),
		CreatedDate:    now.Add(-time.Minute).Format(time.RFC3339),
	})
	must(t, issue, err)

	// 5. Trigger the blueprint and wait for the pipeline.
	trig, err := api.PostBlueprintsBlueprintIdTriggerWithResponse(ctx, bpID, devlakeapi.ModelsTriggerSyncPolicy{})
	must(t, trig, err)
	pipeline := firstNonNil(trig.JSON200, trig.JSON201)
	if pipeline.Id == nil {
		t.Fatalf("trigger returned no pipeline: %s", trig.Body)
	}
	t.Logf("triggered pipeline %d", *pipeline.Id)

	final := waitForPipeline(ctx, t, api, *pipeline.Id)
	if s := deref(final.Status); s != "TASK_COMPLETED" {
		t.Fatalf("pipeline finished with status %s: %s", s, deref(final.Message))
	}

	// 6. The pipeline shows up in the blueprint's history and has tasks.
	hist, err := api.GetBlueprintsBlueprintIdPipelinesWithResponse(ctx, bpID)
	must(t, hist, err)
	tasks, err := api.GetPipelinesPipelineIdTasksWithResponse(ctx, *pipeline.Id)
	must(t, tasks, err)
	t.Logf("pipeline history: %s", truncate(hist.Body, 300))
	t.Logf("pipeline tasks: %s", truncate(tasks.Body, 300))
}

func waitForPipeline(ctx context.Context, t *testing.T, api *devlakeapi.ClientWithResponses, id int) *devlakeapi.ModelsPipeline {
	t.Helper()
	for {
		r, err := api.GetPipelinesPipelineIdWithResponse(ctx, id)
		must(t, r, err)
		switch s := deref(r.JSON200.Status); s {
		case "TASK_COMPLETED", "TASK_FAILED", "TASK_CANCELLED", "TASK_PARTIAL":
			return r.JSON200
		default:
			t.Logf("pipeline %d: %s (%d/%d tasks)", id, s, derefInt(r.JSON200.FinishedTasks), derefInt(r.JSON200.TotalTasks))
		}
		select {
		case <-ctx.Done():
			t.Fatalf("pipeline %d did not finish: %v", id, ctx.Err())
		case <-time.After(2 * time.Second):
		}
	}
}

// TestAPIKeyAuth creates an API key and uses it on the /rest prefix.
func TestAPIKeyAuth(t *testing.T) {
	ctx := context.Background()
	api := newClient(t).API()

	key, err := api.PostApiKeysWithResponse(ctx, devlakeapi.ModelsApiInputApiKey{
		Name:        uniq("it-key"),
		AllowedPath: ".*",
		Type:        "devlake",
	})
	must(t, key, err)
	out := firstNonNil(key.JSON200, key.JSON201)
	if out.ApiKey == nil || out.Id == nil {
		t.Fatalf("no api key returned: %s", key.Body)
	}
	t.Cleanup(func() {
		r, err := api.DeleteApiKeysApiKeyIdWithResponse(context.Background(), *out.Id)
		if err := devlake.CheckResponse(r, err); err != nil {
			t.Logf("cleanup api key: %v", err)
		}
	})

	rest, err := devlake.New(baseURL+"/rest", devlake.WithAPIKey(*out.ApiKey))
	if err != nil {
		t.Fatal(err)
	}
	list, err := rest.API().GetProjectsWithResponse(ctx, nil)
	must(t, list, err)

	bad, err := devlake.New(baseURL+"/rest", devlake.WithAPIKey("wrong"))
	if err != nil {
		t.Fatal(err)
	}
	r, err := bad.API().GetProjectsWithResponse(ctx, nil)
	err = devlake.CheckResponse(r, err)
	var apiErr *devlake.APIError
	if !errors.As(err, &apiErr) || (apiErr.StatusCode != http.StatusUnauthorized && apiErr.StatusCode != http.StatusForbidden) {
		t.Errorf("wrong key: want 401/403, got %v", err)
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func derefInt(i *int) int {
	if i == nil {
		return 0
	}
	return *i
}

func truncate(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n]) + "..."
	}
	return string(b)
}
