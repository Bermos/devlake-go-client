package devlake

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/Bermos/devlake-go-client/devlakeapi"
)

// Project is a DevLake project together with its blueprint.
type Project = devlakeapi.ModelsApiOutputProject

// ProjectSpec describes the desired state of a project for EnsureProject.
type ProjectSpec struct {
	Name        string
	Description string
	// DORA enables DORA metric calculation (deployments, incidents,
	// change lead time). Defaults to true when nil.
	DORA *bool
	// Cron overrides the blueprint's sync schedule, e.g. "0 0 * * *".
	// Empty keeps DevLake's default (daily).
	Cron string
}

// GetProject returns a project by name, or an error matching ErrNotFound.
func (c *Client) GetProject(ctx context.Context, name string) (*Project, error) {
	resp, err := c.api.GetProjectsProjectNameWithResponse(ctx, name)
	if err := CheckResponse(resp, err); err != nil {
		return nil, err
	}
	return resp.JSON200, nil
}

// ListProjects returns all projects, following pagination.
func (c *Client) ListProjects(ctx context.Context) ([]Project, error) {
	var all []Project
	const pageSize = 100
	for page := 1; ; page++ {
		resp, err := c.api.GetProjectsWithResponse(ctx, &devlakeapi.GetProjectsParams{Page: &page, PageSize: ptr(pageSize)})
		if err := CheckResponse(resp, err); err != nil {
			return nil, err
		}
		if resp.JSON200 == nil || resp.JSON200.Projects == nil {
			return all, nil
		}
		all = append(all, *resp.JSON200.Projects...)
		total := 0
		if resp.JSON200.Count != nil {
			total = *resp.JSON200.Count
		}
		if len(*resp.JSON200.Projects) < pageSize || len(all) >= total {
			return all, nil
		}
	}
}

// EnsureProject creates the project if it does not exist yet, otherwise it
// updates the description, metrics and schedule to match spec. It is safe to
// call repeatedly (e.g. from provisioning code).
func (c *Client) EnsureProject(ctx context.Context, spec ProjectSpec) (*Project, error) {
	if spec.Name == "" {
		return nil, errors.New("devlake: project name is required")
	}
	dora := spec.DORA == nil || *spec.DORA
	input := devlakeapi.ModelsApiInputProject{
		Name:        spec.Name,
		Description: &spec.Description,
		Enable:      ptr(true),
		Metrics: &[]devlakeapi.ModelsBaseMetric{
			{PluginName: "dora", Enable: &dora, PluginOption: &map[string]any{}},
		},
	}

	existing, err := c.GetProject(ctx, spec.Name)
	switch {
	case errors.Is(err, ErrNotFound):
		resp, err := c.api.PostProjectsWithResponse(ctx, input)
		if err := CheckResponse(resp, err); err != nil {
			return nil, err
		}
		c.logger.InfoContext(ctx, "created devlake project", "project", spec.Name)
	case err != nil:
		return nil, err
	default:
		input.Blueprint = existing.Blueprint
		resp, err := c.api.PatchProjectsProjectNameWithResponse(ctx, spec.Name, input)
		if err := CheckResponse(resp, err); err != nil {
			return nil, err
		}
	}

	if spec.Cron != "" {
		p, err := c.GetProject(ctx, spec.Name)
		if err != nil {
			return nil, err
		}
		if id, err := blueprintID(p); err == nil && (p.Blueprint.CronConfig == nil || *p.Blueprint.CronConfig != spec.Cron) {
			if err := c.patchBlueprint(ctx, id, map[string]any{"cronConfig": spec.Cron, "isManual": false}); err != nil {
				return nil, err
			}
		}
	}
	return c.GetProject(ctx, spec.Name)
}

// DeleteProject deletes a project and its blueprint. Collected data stays.
func (c *Client) DeleteProject(ctx context.Context, name string) error {
	resp, err := c.api.DeleteProjectsProjectNameWithResponse(ctx, name)
	return CheckResponse(resp, err)
}

// ConnectProject adds a connection, and the given scopes of it, to the
// project's blueprint. Scopes already present are kept; calling it again is
// harmless. Scope IDs are those from RemoteScope.ID.
func (c *Client) ConnectProject(ctx context.Context, project string, p Plugin, connectionID int, scopeIDs ...string) error {
	proj, err := c.GetProject(ctx, project)
	if err != nil {
		return err
	}
	bpID, err := blueprintID(proj)
	if err != nil {
		return err
	}
	var conns []devlakeapi.ModelsBlueprintConnection
	if proj.Blueprint.Connections != nil {
		conns = *proj.Blueprint.Connections
	}
	idx := slices.IndexFunc(conns, func(bc devlakeapi.ModelsBlueprintConnection) bool {
		return bc.PluginName == string(p) && bc.ConnectionId == connectionID
	})
	if idx < 0 {
		conns = append(conns, devlakeapi.ModelsBlueprintConnection{PluginName: string(p), ConnectionId: connectionID})
		idx = len(conns) - 1
	}
	var scopes []devlakeapi.ModelsBlueprintScope
	if conns[idx].Scopes != nil {
		scopes = *conns[idx].Scopes
	}
	for _, id := range scopeIDs {
		if !slices.ContainsFunc(scopes, func(s devlakeapi.ModelsBlueprintScope) bool { return s.ScopeId == id }) {
			scopes = append(scopes, devlakeapi.ModelsBlueprintScope{ScopeId: id})
		}
	}
	conns[idx].Scopes = &scopes
	return c.patchBlueprint(ctx, bpID, map[string]any{"connections": conns})
}

// DisconnectProject removes a connection (with all its scopes) from the
// project's blueprint.
func (c *Client) DisconnectProject(ctx context.Context, project string, p Plugin, connectionID int) error {
	proj, err := c.GetProject(ctx, project)
	if err != nil {
		return err
	}
	bpID, err := blueprintID(proj)
	if err != nil {
		return err
	}
	conns := []devlakeapi.ModelsBlueprintConnection{}
	if proj.Blueprint.Connections != nil {
		for _, bc := range *proj.Blueprint.Connections {
			if bc.PluginName != string(p) || bc.ConnectionId != connectionID {
				conns = append(conns, bc)
			}
		}
	}
	return c.patchBlueprint(ctx, bpID, map[string]any{"connections": conns})
}

// SyncOptions controls a project sync.
type SyncOptions struct {
	// FullSync re-collects everything instead of incrementally.
	FullSync bool
	// SkipCollectors only re-runs extraction/conversion on already collected
	// data, e.g. after changing scope configs.
	SkipCollectors bool
}

// TriggerProject starts the project's blueprint now and returns the new
// pipeline without waiting for it.
func (c *Client) TriggerProject(ctx context.Context, project string, opts SyncOptions) (*Pipeline, error) {
	proj, err := c.GetProject(ctx, project)
	if err != nil {
		return nil, err
	}
	bpID, err := blueprintID(proj)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.PostBlueprintsBlueprintIdTriggerWithResponse(ctx, bpID, devlakeapi.ModelsTriggerSyncPolicy{
		FullSync:       &opts.FullSync,
		SkipCollectors: &opts.SkipCollectors,
	})
	if err := CheckResponse(resp, err); err != nil {
		return nil, err
	}
	pl := resp.JSON200
	if pl == nil {
		pl = resp.JSON201
	}
	if pl == nil || pl.Id == nil {
		return nil, errors.New("devlake: trigger returned no pipeline")
	}
	c.logger.InfoContext(ctx, "triggered devlake project", "project", project, "pipeline", *pl.Id)
	return pl, nil
}

// SyncProject triggers the project's blueprint and waits for the pipeline
// to finish. It returns a *PipelineError if the pipeline did not succeed.
func (c *Client) SyncProject(ctx context.Context, project string, opts SyncOptions) (*Pipeline, error) {
	pl, err := c.TriggerProject(ctx, project, opts)
	if err != nil {
		return nil, err
	}
	return c.WaitForPipeline(ctx, *pl.Id)
}

func (c *Client) patchBlueprint(ctx context.Context, id int, fields map[string]any) error {
	resp, err := c.api.PatchBlueprintsBlueprintIdWithResponse(ctx, id, fields)
	return CheckResponse(resp, err)
}

func blueprintID(p *Project) (int, error) {
	if p == nil || p.Blueprint == nil || p.Blueprint.Id == nil {
		name := ""
		if p != nil {
			name = p.Name
		}
		return 0, fmt.Errorf("devlake: project %q has no blueprint", name)
	}
	return *p.Blueprint.Id, nil
}

func ptr[T any](v T) *T { return &v }

func itoa(i int) string { return strconv.Itoa(i) }
