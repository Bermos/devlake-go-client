# devlake-go-client

A Go client for [Apache DevLake](https://github.com/apache/devlake), generated
from the OpenAPI document DevLake itself produces and wrapped in a small,
dependency injection friendly API.

```sh
go get github.com/Bermos/devlake-go-client
```

## Usage

```go
c, err := devlake.New("https://devlake.example.com/api",
	devlake.WithHTTPClient(otelhttp.DefaultClient), // any HTTPDoer
	devlake.WithLogger(slog.Default()),
	devlake.WithBasicAuth("admin", "secret"),
)
if err != nil {
	return err
}

version, err := c.Version(ctx)
```

Every endpoint DevLake documents, including all plugins, is available on the
generated low-level client:

```go
resp, err := c.API().GetPluginsJiraConnectionsWithResponse(ctx)
if err := devlake.CheckResponse(resp, err); err != nil {
	return err
}
for _, conn := range *resp.JSON200 {
	fmt.Println(conn.Name)
}
```

### Convenience helpers

The `Client` wraps DevLake's core workflows so you don't have to string
together blueprints, scopes and pipelines by hand.

**For users (setting up data sources and projects)**

| Helper | What it does |
| --- | --- |
| `CreateGitLabConnection`, `CreateJiraConnection`, `CreateSonarQubeConnection`, `CreateGitHubConnection`, `CreateWebhookConnection` | Typed connection setup with sensible defaults |
| `ListConnections`, `FindConnection`, `GetConnection`, `UpdateConnection`, `DeleteConnection`, `TestConnection`, `CreateConnection` | Work with any plugin's connections |
| `SearchRemoteScopes`, `AddScopes`, `AddScopesByName`, `ListScopes` | Find repos, boards and projects on the remote system and register them, e.g. `AddScopesByName(ctx, PluginGitLab, id, opts, "group/repo")` |
| `EnsureProject` | Idempotent create or update of a project (DORA on by default, schedule) |
| `ConnectProject`, `DisconnectProject` | Add or remove connections and scopes on the project's blueprint without clobbering existing ones |
| `TriggerProject`, `SyncProject` | Start a sync, optionally full or without collectors, and wait for it |
| `Webhook(id).Deployment / Incident / CloseIncident` | Push DORA data from CI/CD and incident tooling |

**For operators**

| Helper | What it does |
| --- | --- |
| `Status` | Version, readiness, running pipelines, recent failures |
| `ProjectStatuses`, `UnhealthyProjects` | Flags projects with no connections, no successful sync, a failed last sync, or stale data |
| `ListPipelines`, `GetPipeline`, `WaitForPipeline`, `CancelPipeline` | Pipeline control |
| `RerunPipeline`, `RetryFailedPipelines`, `FailedTasks`, `RerunTask` | Recover after outages or credential fixes |
| `DownloadPipelineLogs` | Fetch the log archive of a pipeline |
| `Do` | Raw call to any endpoint, using the client's auth, logging and errors |

See [`example_test.go`](example_test.go) for complete onboarding,
deployment reporting and operator report examples.

### Base URL and authentication

| Setup | Base URL | Option |
| --- | --- | --- |
| Through config-ui | `https://host/api` | `WithBasicAuth` if `ADMIN_USER`/`ADMIN_PASS` are set |
| Backend directly | `http://devlake:8080` | none |
| API key | `https://host/api/rest` (or `http://devlake:8080/rest`) | `WithAPIKey` |

DevLake only checks API keys on the `/rest` prefix, so include it when using
`WithAPIKey`.

### Options

| Option | Purpose |
| --- | --- |
| `WithHTTPClient(HTTPDoer)` | Inject an instrumented / retrying / custom TLS client |
| `WithLogger(*slog.Logger)` | Structured request logging (debug), failures at warn |
| `WithAPIKey(key)` | Bearer token auth |
| `WithBasicAuth(user, pass)` | Basic auth for config-ui |
| `WithUserAgent(ua)` | Override the User-Agent |
| `WithRequestEditor(fn)` | Mutate every request (custom headers, auth proxies) |
| `WithPollInterval(d)` | Poll interval for wait helpers |

Non-2xx responses become `*devlake.APIError`, which carries DevLake's error
message and causes; `errors.Is(err, devlake.ErrNotFound)` works for 404s.

Timestamps in generated models are strings (DevLake mixes formats); use
`devlake.ParseTime` to turn them into `time.Time`.

## Testing

```sh
make test                                   # unit tests, no DevLake needed
make devlake-up && make test-integration    # DevLake + PostgreSQL in docker compose
make devlake-down
```

The integration suite (`integration/`, build tag `integration`) runs against
the DevLake version in `spec/DEVLAKE_VERSION` on PostgreSQL. It covers health
endpoints, project lifecycle, connection CRUD for Jira, GitLab, SonarQube and
GitHub, API key auth on `/rest`, and a full webhook pipeline: connection,
project, blueprint, pushed deployment and incident, trigger, wait for the
pipeline to complete. CI runs it on every PR.

## Versioning and releases

Client versions equal the DevLake version they were generated from and
tested against, e.g. `v1.0.3-beta19`. The current one is in
[`spec/DEVLAKE_VERSION`](spec/DEVLAKE_VERSION).

The client follows the newest DevLake tag, **including pre-releases**,
because DevLake ships many fixes there long before a stable release. Because
DevLake keeps cutting `v1.0.3-betaN` after `v1.0.3`, `go get ...@latest`
resolves to the newest *stable* client; pin a pre-release explicitly:

```sh
go get github.com/Bermos/devlake-go-client@v1.0.3-beta19
```

Client-only fixes for a pre-release target are published as
`<devlake-version>.N` (e.g. `v1.0.3-beta19.1`).

### Automation

| Workflow | What it does |
| --- | --- |
| `DevLake update` (daily) | Finds the newest DevLake tag. If it is new and its Docker image is published, regenerates spec and client, runs unit and integration tests against that version, and opens a PR with an API changelog (oasdiff), including a breaking change count. |
| `Release` (on merge + every 6h) | When `spec/DEVLAKE_VERSION` has no tag yet, runs the integration suite and publishes a tag and GitHub release (pre-release for `-alpha/-beta/-rc`). |
| `CI` | Build, vet, unit tests, integration tests, generated code drift check, lint. |

Repository settings:

- `AUTO_RELEASE` variable = `true` enables automatic releases (manual
  "Run workflow" on `Release` always works).
- `AUTO_MERGE_UPDATES` variable = `true` enables auto-merge on update PRs
  (also enable "Allow auto-merge").
- `DEVLAKE_BOT_TOKEN` secret (optional): a fine-grained PAT with contents and
  pull request write access. PRs opened with the default token do not trigger
  CI (tests still run inside the update workflow).

## Development

```sh
make spec DEVLAKE_VERSION=v1.0.3-beta19  # regenerate spec/swagger.json from DevLake
make generate                            # normalize spec + regenerate devlakeapi
make lint
```

How the client is built:

1. `scripts/generate-spec.sh` checks out DevLake at a tag and runs `swag`
   exactly as DevLake's `make swag` does, producing `spec/swagger.json`.
2. `tools/specprep` fixes the quirks in that Swagger 2.0 document (gin style
   path params, missing operation IDs, duplicate body params,
   documentation-only paths, ...) and converts it to OpenAPI 3
   (`spec/openapi.json`). Annotations that don't match DevLake's real
   responses are corrected in `spec/overrides.json`.
3. [oapi-codegen](https://github.com/oapi-codegen/oapi-codegen) generates
   `devlakeapi/devlakeapi.gen.go`.

Generated files are committed; CI fails if they drift from the spec.
