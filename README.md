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

## Versioning

The client targets the latest DevLake release, including pre-releases, since
DevLake ships many fixes there long before a stable release. The DevLake
version a build was generated from is recorded in
[`spec/DEVLAKE_VERSION`](spec/DEVLAKE_VERSION). Once the setup is verified,
client versions will follow DevLake's version numbers.

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
