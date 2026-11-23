package devlake

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/Bermos/devlake-go-client/devlakeapi"
)

// Pipeline is a DevLake pipeline run.
type Pipeline = devlakeapi.ModelsPipeline

// Pipeline and task statuses used by DevLake.
const (
	StatusCreated   = "TASK_CREATED"
	StatusRerun     = "TASK_RERUN"
	StatusRunning   = "TASK_RUNNING"
	StatusCompleted = "TASK_COMPLETED"
	StatusFailed    = "TASK_FAILED"
	StatusCancelled = "TASK_CANCELLED"
	StatusPartial   = "TASK_PARTIAL"
)

// IsFinished reports whether a pipeline or task status is terminal.
func IsFinished(status string) bool {
	switch status {
	case StatusCompleted, StatusFailed, StatusCancelled, StatusPartial:
		return true
	}
	return false
}

// PipelineError is returned by WaitForPipeline when a pipeline finishes
// without succeeding. Pipeline holds the final state.
type PipelineError struct {
	Pipeline *Pipeline
}

func (e *PipelineError) Error() string {
	return fmt.Sprintf("devlake: pipeline %d finished with %s: %s",
		deref(e.Pipeline.Id), deref(e.Pipeline.Status), deref(e.Pipeline.Message))
}

// GetPipeline returns one pipeline.
func (c *Client) GetPipeline(ctx context.Context, id int) (*Pipeline, error) {
	resp, err := c.api.GetPipelinesPipelineIdWithResponse(ctx, id)
	if err := CheckResponse(resp, err); err != nil {
		return nil, err
	}
	return resp.JSON200, nil
}

// WaitForPipeline polls a pipeline (see WithPollInterval) until it finishes
// or ctx is done. It returns the final pipeline, plus a *PipelineError if it
// did not complete successfully.
func (c *Client) WaitForPipeline(ctx context.Context, id int) (*Pipeline, error) {
	t := time.NewTicker(c.poll)
	defer t.Stop()
	for {
		p, err := c.GetPipeline(ctx, id)
		if err != nil {
			return nil, err
		}
		status := deref(p.Status)
		if IsFinished(status) {
			if status != StatusCompleted {
				return p, &PipelineError{Pipeline: p}
			}
			return p, nil
		}
		c.logger.DebugContext(ctx, "waiting for devlake pipeline", "pipeline", id, "status", status,
			"finishedTasks", deref(p.FinishedTasks), "totalTasks", deref(p.TotalTasks))
		select {
		case <-ctx.Done():
			return p, fmt.Errorf("devlake: waiting for pipeline %d: %w", id, ctx.Err())
		case <-t.C:
		}
	}
}

// PipelineFilter narrows ListPipelines.
type PipelineFilter struct {
	// Status filters by pipeline status, e.g. StatusFailed.
	Status string
	// BlueprintID filters by blueprint (0 = all).
	BlueprintID int
	// Pending lists only pipelines that have not finished.
	Pending bool
	// Limit caps the number of returned pipelines (newest first); 0 = 100.
	Limit int
}

// ListPipelines returns pipelines, newest first.
func (c *Client) ListPipelines(ctx context.Context, f PipelineFilter) ([]Pipeline, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	pageSize := min(limit, 100)
	var all []Pipeline
	for page := 1; len(all) < limit; page++ {
		params := &devlakeapi.GetPipelinesParams{Page: ptr(page), Pagesize: ptr(pageSize)}
		if f.Status != "" {
			params.Status = &f.Status
		}
		if f.BlueprintID > 0 {
			params.BlueprintId = &f.BlueprintID
		}
		if f.Pending {
			params.Pending = ptr(1)
		}
		resp, err := c.api.GetPipelinesWithResponse(ctx, params)
		if err := CheckResponse(resp, err); err != nil {
			return nil, err
		}
		if resp.JSON200 == nil || resp.JSON200.Pipelines == nil {
			break
		}
		got := *resp.JSON200.Pipelines
		all = append(all, got...)
		if len(got) < pageSize {
			break
		}
	}
	if len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

// CancelPipeline cancels a running pipeline.
func (c *Client) CancelPipeline(ctx context.Context, id int) error {
	resp, err := c.api.DeletePipelinesPipelineIdWithResponse(ctx, id)
	return CheckResponse(resp, err)
}

// RerunPipeline re-runs the failed tasks of a finished pipeline. DevLake
// reuses the pipeline ID, so WaitForPipeline(ctx, id) follows the rerun.
func (c *Client) RerunPipeline(ctx context.Context, id int) error {
	resp, err := c.api.PostPipelinesPipelineIdRerunWithResponse(ctx, id)
	return CheckResponse(resp, err)
}

// Task is a single plugin task within a pipeline.
type Task struct {
	ID            int             `json:"id"`
	PipelineID    int             `json:"pipelineId"`
	PipelineRow   int             `json:"pipelineRow"`
	PipelineCol   int             `json:"pipelineCol"`
	Plugin        string          `json:"plugin"`
	Status        string          `json:"status"`
	Message       string          `json:"message"`
	ErrorName     string          `json:"errorName"`
	Progress      float64         `json:"progress"`
	SpentSeconds  int             `json:"spentSeconds"`
	BeganAt       string          `json:"beganAt"`
	FinishedAt    string          `json:"finishedAt"`
	Options       json.RawMessage `json:"options"`
	FailedSubTask string          `json:"failedSubTask"`
}

// PipelineTasks returns the tasks of a pipeline.
func (c *Client) PipelineTasks(ctx context.Context, pipelineID int) ([]Task, error) {
	var out struct {
		Tasks []Task `json:"tasks"`
	}
	err := c.Do(ctx, http.MethodGet, "pipelines/"+itoa(pipelineID)+"/tasks", nil, nil, &out)
	return out.Tasks, err
}

// FailedTasks returns the tasks of a pipeline that failed.
func (c *Client) FailedTasks(ctx context.Context, pipelineID int) ([]Task, error) {
	tasks, err := c.PipelineTasks(ctx, pipelineID)
	if err != nil {
		return nil, err
	}
	var failed []Task
	for _, t := range tasks {
		if t.Status == StatusFailed {
			failed = append(failed, t)
		}
	}
	return failed, nil
}

// RerunTask re-runs a single task.
func (c *Client) RerunTask(ctx context.Context, taskID int) error {
	resp, err := c.api.PostTasksTaskIdRerunWithResponse(ctx, taskID)
	return CheckResponse(resp, err)
}

// DownloadPipelineLogs writes the pipeline's log archive (.tar.gz) to w.
func (c *Client) DownloadPipelineLogs(ctx context.Context, pipelineID int, w io.Writer) error {
	resp, err := c.send(ctx, http.MethodGet, "pipelines/"+itoa(pipelineID)+"/logging.tar.gz", url.Values{}, nil)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return checkStatus(resp.StatusCode, b)
	}
	_, err = io.Copy(w, resp.Body)
	return err
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}
