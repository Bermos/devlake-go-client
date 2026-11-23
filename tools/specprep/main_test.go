package main

import (
	"encoding/json"
	"testing"
)

func TestOperationID(t *testing.T) {
	cases := map[[2]string]string{
		{"get", "/plugins/jira/connections/{connectionId}"}: "GetPluginsJiraConnectionsConnectionId",
		{"post", "/blueprints/{blueprintId}/trigger"}:       "PostBlueprintsBlueprintIdTrigger",
		{"get", "/plugins/bitbucket_server/connections"}:    "GetPluginsBitbucketServerConnections",
		{"get", "/pipelines/{pipelineId}/logging.tar.gz"}:   "GetPipelinesPipelineIdLoggingTarGz",
	}
	for in, want := range cases {
		if got := operationID(in[0], in[1]); got != want {
			t.Errorf("operationID(%q, %q) = %q, want %q", in[0], in[1], got, want)
		}
	}
}

func TestMergePaths(t *testing.T) {
	paths := obj{
		"/projects/:projectName":             obj{"delete": obj{}, "patch": obj{}},
		"/projects/{projectName}":            obj{"get": obj{}},
		"/pipelines/jira/pipeline-task":      obj{"post": obj{}},
		"/x/{connectionId}/scopes/{scopeId}": obj{"get": obj{}, "put": obj{}},
		"/x/{connectionId}/scopes/{boardId}": obj{"delete": obj{"parameters": []any{obj{"in": "path", "name": "boardId"}}}},
	}
	got := mergePaths(paths)
	if _, ok := got["/pipelines/jira/pipeline-task"]; ok {
		t.Error("documentation-only path was kept")
	}
	if p := got["/projects/{projectName}"].(obj); len(p) != 3 {
		t.Errorf("projects item has %d operations, want 3", len(p))
	}
	scopes, ok := got["/x/{connectionId}/scopes/{scopeId}"].(obj)
	if !ok || len(scopes) != 3 {
		t.Fatalf("scope paths not merged: %v", got)
	}
	del := scopes["delete"].(obj)["parameters"].([]any)[0].(obj)
	if del["name"] != "scopeId" {
		t.Errorf("path param not renamed: %v", del["name"])
	}
}

func TestFixParams(t *testing.T) {
	op := obj{"parameters": []any{
		obj{"in": "path", "name": "connectionId", "type": "string"},
		obj{"in": "path", "name": "notInPath", "type": "string"},
		obj{"in": "body", "name": "a", "schema": obj{"$ref": "#/definitions/X"}},
		obj{"in": "body", "name": "b", "schema": obj{"$ref": "#/definitions/X"}},
	}}
	fixParams("/c/{connectionId}/s/{scopeId}", op)
	params := op["parameters"].([]any)
	byName := map[string]obj{}
	for _, p := range params {
		byName[p.(obj)["name"].(string)] = p.(obj)
	}
	if byName["connectionId"]["type"] != "integer" {
		t.Error("connectionId not forced to integer")
	}
	if _, ok := byName["notInPath"]; ok {
		t.Error("undeclared path param kept")
	}
	if byName["scopeId"]["required"] != true {
		t.Error("missing path param not added")
	}
	if _, ok := byName["body"]; !ok || len(params) != 3 {
		t.Errorf("bodies not merged: %v", params)
	}
}

func TestPrepareProducesValidOpenAPI3(t *testing.T) {
	sw := `{
	  "swagger": "2.0", "info": {"title": "x", "version": "0"},
	  "paths": {
	    "/version": {"get": {"responses": {"200": {"description": "OK", "schema": {"type": "string"}}}}},
	    "/projects/:projectName": {"delete": {"responses": {}}},
	    "/projects": {"post": {"parameters": [{"in": "body", "name": "body", "schema": {"$ref": "#/definitions/models.P"}}],
	      "responses": {"200": {"description": "OK", "schema": {"$ref": "#/definitions/models.P"}},
	                    "400": {"description": "bad", "schema": {"type": "string"}}}}}
	  },
	  "definitions": {"github_com_apache_devlake_plugins_x_api.P": {"type": "object", "required": ["n", "n"], "properties": {"n": {"type": "string"}}},
	                  "models.P": {"type": "object", "properties": {"q": {"$ref": "#/definitions/github_com_apache_devlake_plugins_x_api.P"}}}}
	}`
	out, err := Prepare([]byte(sw), "v9.9.9", map[string]obj{
		"get /version": {"responses": obj{"200": obj{"description": "OK", "schema": obj{"type": "object"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["openapi"] == nil {
		t.Error("not an OpenAPI 3 document")
	}
	schemas := doc["components"].(map[string]any)["schemas"].(map[string]any)
	if _, ok := schemas["x_api.P"]; !ok {
		t.Errorf("definition prefix not shortened: %v", schemas)
	}
	post := doc["paths"].(map[string]any)["/projects"].(map[string]any)["post"].(map[string]any)
	if post["operationId"] != "PostProjects" {
		t.Errorf("operationId = %v", post["operationId"])
	}
	resps := post["responses"].(map[string]any)
	if _, ok := resps["201"]; !ok {
		t.Error("201 not mirrored from 200")
	}
	if _, ok := resps["400"].(map[string]any)["content"]; ok {
		t.Error("error response still typed")
	}

	if _, err := Prepare([]byte(sw), "", map[string]obj{"get /nope": {}}); err == nil {
		t.Error("override for unknown operation accepted")
	}
}
