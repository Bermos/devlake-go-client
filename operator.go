package devlake

import (
	"context"
	"errors"
	"sort"
	"time"
)

// SystemStatus is a point-in-time health summary of a DevLake instance.
type SystemStatus struct {
	Version string
	// Ready is false while DevLake is starting or migrating; ReadyError
	// explains why.
	Ready      bool
	ReadyError error
	// RunningPipelines are pipelines that have not finished yet.
	RunningPipelines []Pipeline
	// RecentFailures are failed pipelines among the newest 100.
	RecentFailures []Pipeline
}

// Status gathers a SystemStatus. Errors reaching DevLake at all are
// returned; a not-ready server is reported in the result instead.
func (c *Client) Status(ctx context.Context) (*SystemStatus, error) {
	if err := c.Ping(ctx); err != nil {
		return nil, err
	}
	st := &SystemStatus{}
	st.ReadyError = c.Ready(ctx)
	st.Ready = st.ReadyError == nil
	var err error
	if st.Version, err = c.Version(ctx); err != nil {
		return nil, err
	}
	if !st.Ready {
		return st, nil
	}
	if st.RunningPipelines, err = c.ListPipelines(ctx, PipelineFilter{Pending: true}); err != nil {
		return nil, err
	}
	recent, err := c.ListPipelines(ctx, PipelineFilter{Limit: 100})
	if err != nil {
		return nil, err
	}
	for _, p := range recent {
		if s := deref(p.Status); s == StatusFailed || s == StatusPartial {
			st.RecentFailures = append(st.RecentFailures, p)
		}
	}
	return st, nil
}

// ProjectStatus summarizes the sync state of one project.
type ProjectStatus struct {
	Project string
	// Enabled reports whether the blueprint runs on its schedule.
	Enabled     bool
	Cron        string
	Connections int
	// LastPipeline is the most recent pipeline, nil if it never ran.
	LastPipeline *Pipeline
	// LastSuccess is when the most recent successful sync finished (zero if
	// none among the recent pipelines).
	LastSuccess time.Time
	// Problem is a short human readable description of what needs
	// attention, empty when healthy.
	Problem string
}

// Healthy reports whether the project needs no attention.
func (s ProjectStatus) Healthy() bool { return s.Problem == "" }

// ProjectStatuses reports the sync state of every project. A project is
// flagged when it has no connections, never synced, its last pipeline
// failed, or its last successful sync is older than maxAge (0 disables the
// age check).
func (c *Client) ProjectStatuses(ctx context.Context, maxAge time.Duration) ([]ProjectStatus, error) {
	projects, err := c.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ProjectStatus, 0, len(projects))
	for i := range projects {
		st, err := c.projectStatus(ctx, &projects[i], maxAge)
		if err != nil {
			return nil, err
		}
		out = append(out, *st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Project < out[j].Project })
	return out, nil
}

// UnhealthyProjects returns only the projects that need attention.
func (c *Client) UnhealthyProjects(ctx context.Context, maxAge time.Duration) ([]ProjectStatus, error) {
	all, err := c.ProjectStatuses(ctx, maxAge)
	if err != nil {
		return nil, err
	}
	var bad []ProjectStatus
	for _, s := range all {
		if !s.Healthy() {
			bad = append(bad, s)
		}
	}
	return bad, nil
}

func (c *Client) projectStatus(ctx context.Context, p *Project, maxAge time.Duration) (*ProjectStatus, error) {
	st := &ProjectStatus{Project: p.Name}
	bp := p.Blueprint
	if bp == nil {
		st.Problem = "no blueprint"
		return st, nil
	}
	st.Enabled = deref(bp.Enable) && !deref(bp.IsManual)
	st.Cron = deref(bp.CronConfig)
	if bp.Connections != nil {
		st.Connections = len(*bp.Connections)
	}
	if bp.Id != nil {
		history, err := c.ListPipelines(ctx, PipelineFilter{BlueprintID: *bp.Id, Limit: 20})
		if err != nil && !errors.Is(err, ErrNotFound) {
			return nil, err
		}
		if len(history) > 0 {
			st.LastPipeline = &history[0]
		}
		for _, pl := range history {
			if deref(pl.Status) == StatusCompleted {
				if t, err := ParseTime(deref(pl.FinishedAt)); err == nil {
					st.LastSuccess = t
				}
				break
			}
		}
	}

	switch {
	case st.Connections == 0:
		st.Problem = "no data connections"
	case st.LastPipeline == nil:
		st.Problem = "never synced"
	case deref(st.LastPipeline.Status) == StatusFailed || deref(st.LastPipeline.Status) == StatusPartial:
		st.Problem = "last sync " + deref(st.LastPipeline.Status) + ": " + deref(st.LastPipeline.Message)
	case maxAge > 0 && st.LastSuccess.IsZero():
		st.Problem = "no successful sync"
	case maxAge > 0 && time.Since(st.LastSuccess) > maxAge:
		st.Problem = "last successful sync " + time.Since(st.LastSuccess).Round(time.Minute).String() + " ago"
	}
	return st, nil
}

// RetryFailedPipelines re-runs every failed pipeline among the newest 100
// and returns their IDs. Use it after fixing credentials or a remote outage.
func (c *Client) RetryFailedPipelines(ctx context.Context) ([]int, error) {
	failed, err := c.ListPipelines(ctx, PipelineFilter{Status: StatusFailed, Limit: 100})
	if err != nil {
		return nil, err
	}
	var ids []int
	for _, p := range failed {
		if p.Id == nil {
			continue
		}
		if err := c.RerunPipeline(ctx, *p.Id); err != nil {
			return ids, err
		}
		ids = append(ids, *p.Id)
	}
	return ids, nil
}
