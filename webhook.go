package devlake

import (
	"context"
	"errors"
	"time"

	"github.com/Bermos/devlake-go-client/devlakeapi"
)

// WebhookConnection is a webhook connection including the API key DevLake
// generated for it. The key is only returned on creation; store it.
type WebhookConnection struct {
	ID     int
	Name   string
	APIKey string
	// Endpoints as reported by DevLake, relative to the DevLake API root.
	DeploymentsEndpoint  string
	IssuesEndpoint       string
	PullRequestsEndpoint string
}

// CreateWebhookConnection creates a webhook connection. Use it for
// deployments from CI/CD systems and incidents from tools DevLake has no
// plugin for; both feed DORA metrics.
func (c *Client) CreateWebhookConnection(ctx context.Context, name string) (*WebhookConnection, error) {
	resp, err := c.api.PostPluginsWebhookConnectionsWithResponse(ctx, devlakeapi.ApiWebhookConnectionResponse{Name: name})
	if err := CheckResponse(resp, err); err != nil {
		return nil, err
	}
	out := resp.JSON200
	if out == nil {
		out = resp.JSON201
	}
	if out == nil || out.Id == nil {
		return nil, errors.New("devlake: webhook connection response without id")
	}
	wc := &WebhookConnection{
		ID:                   *out.Id,
		Name:                 out.Name,
		DeploymentsEndpoint:  deref(out.PostPipelineDeployTaskEndpoint),
		IssuesEndpoint:       deref(out.PostIssuesEndpoint),
		PullRequestsEndpoint: deref(out.PostPullRequestsEndpoint),
	}
	if out.ApiKey != nil {
		wc.APIKey = deref(out.ApiKey.ApiKey)
	}
	return wc, nil
}

// Webhook returns a sender for pushing events into a webhook connection.
// The client should be authenticated with the connection's API key
// (WithAPIKey) and point at the "/rest" prefix when DevLake has API key
// auth enabled; it works with any client that may access the endpoints.
func (c *Client) Webhook(connectionID int) *WebhookSender {
	return &WebhookSender{c: c, id: connectionID}
}

// WebhookSender pushes deployments, incidents and pull requests into DevLake.
type WebhookSender struct {
	c  *Client
	id int
}

// Deployment describes a finished deployment.
type Deployment struct {
	// ID uniquely identifies the deployment in your CI/CD system.
	ID   string
	Name string
	// Environment is PRODUCTION, STAGING, TESTING or DEVELOPMENT. Only
	// PRODUCTION deployments count for DORA. Defaults to PRODUCTION.
	Environment string
	// Result is SUCCESS, FAILURE or ABORT. Defaults to SUCCESS.
	Result       string
	StartedAt    time.Time
	FinishedAt   time.Time
	DisplayTitle string
	URL          string
	// Commits deployed. Each needs at least SHA and RepoURL so DevLake can
	// link the deployment to pull requests for change lead time.
	Commits []DeploymentCommit
}

// DeploymentCommit is one commit that was part of a deployment.
type DeploymentCommit struct {
	SHA     string
	RepoURL string
	RefName string
	Message string
}

// Deployment records a deployment.
func (w *WebhookSender) Deployment(ctx context.Context, d Deployment) error {
	if d.ID == "" || d.StartedAt.IsZero() || d.FinishedAt.IsZero() {
		return errors.New("devlake: deployment needs ID, StartedAt and FinishedAt")
	}
	env := devlakeapi.ApiWebhookDeploymentReqEnvironment(withDefault(d.Environment, "PRODUCTION"))
	body := devlakeapi.ApiWebhookDeploymentReq{
		Id:           d.ID,
		Name:         optional(d.Name),
		DisplayTitle: optional(d.DisplayTitle),
		Environment:  &env,
		Result:       ptr(withDefault(d.Result, "SUCCESS")),
		StartedDate:  d.StartedAt.UTC().Format(time.RFC3339),
		FinishedDate: d.FinishedAt.UTC().Format(time.RFC3339),
	}
	if len(d.Commits) > 0 {
		commits := make([]devlakeapi.ApiWebhookDeploymentCommitReq, 0, len(d.Commits))
		for _, cm := range d.Commits {
			commits = append(commits, devlakeapi.ApiWebhookDeploymentCommitReq{
				CommitSha:    cm.SHA,
				RepoUrl:      cm.RepoURL,
				RefName:      optional(cm.RefName),
				CommitMsg:    optional(cm.Message),
				StartedDate:  body.StartedDate,
				FinishedDate: body.FinishedDate,
				Result:       body.Result,
			})
		}
		body.DeploymentCommits = &commits
	}
	resp, err := w.c.api.PostPluginsWebhookConnectionsConnectionIdDeploymentsWithResponse(ctx, w.id, body)
	return CheckResponse(resp, err)
}

// Incident describes an incident (or any issue) to record.
type Incident struct {
	// Key uniquely identifies the incident, e.g. "INC-42".
	Key   string
	Title string
	// Type defaults to INCIDENT, which DORA uses for change failure rate
	// and time to restore.
	Type        string
	Description string
	URL         string
	Severity    string
	Priority    string
	Component   string
	CreatedAt   time.Time
	// ResolvedAt marks the incident as done when set.
	ResolvedAt time.Time
}

// Incident creates or updates an incident.
func (w *WebhookSender) Incident(ctx context.Context, in Incident) error {
	if in.Key == "" || in.Title == "" {
		return errors.New("devlake: incident needs Key and Title")
	}
	created := in.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	status := devlakeapi.ApiWebhookIssueRequestStatusTODO
	original := "open"
	body := devlakeapi.ApiWebhookIssueRequest{
		IssueKey:    in.Key,
		Title:       in.Title,
		Type:        ptr(withDefault(in.Type, "INCIDENT")),
		Description: optional(in.Description),
		Url:         optional(in.URL),
		Severity:    optional(in.Severity),
		Priority:    optional(in.Priority),
		Component:   optional(in.Component),
		CreatedDate: created.UTC().Format(time.RFC3339),
	}
	if !in.ResolvedAt.IsZero() {
		status = devlakeapi.ApiWebhookIssueRequestStatusDONE
		original = "resolved"
		body.ResolutionDate = ptr(in.ResolvedAt.UTC().Format(time.RFC3339))
		body.LeadTimeMinutes = ptr(int(in.ResolvedAt.Sub(created).Minutes()))
	}
	body.Status = &status
	body.OriginalStatus = original
	resp, err := w.c.api.PostPluginsWebhookConnectionIdIssuesWithResponse(ctx, w.id, body)
	return CheckResponse(resp, err)
}

// CloseIncident marks an incident as resolved now.
func (w *WebhookSender) CloseIncident(ctx context.Context, key string) error {
	resp, err := w.c.api.PostPluginsWebhookConnectionIdIssueIssueKeyCloseWithResponse(ctx, w.id, key)
	return CheckResponse(resp, err)
}
