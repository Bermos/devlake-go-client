package devlake_test

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"time"

	devlake "github.com/Bermos/devlake-go-client"
)

// Onboard a GitLab repository and a Jira board into a DORA project and run
// the first sync.
func Example_onboardTeam() {
	ctx := context.Background()
	c, err := devlake.New("https://devlake.example.com/api",
		devlake.WithBasicAuth("admin", "secret"),
		devlake.WithLogger(slog.Default()),
	)
	if err != nil {
		log.Fatal(err)
	}

	gitlab, err := c.CreateGitLabConnection(ctx, devlake.GitLabConnection{Name: "gitlab", Token: "glpat-..."})
	if err != nil {
		log.Fatal(err)
	}
	repos, err := c.AddScopesByName(ctx, devlake.PluginGitLab, gitlab.ID, devlake.ScopeOptions{}, "team-a/backend")
	if err != nil {
		log.Fatal(err)
	}

	jira, err := c.CreateJiraConnection(ctx, devlake.JiraConnection{
		Name: "jira", Endpoint: "https://example.atlassian.net/rest/", Username: "bot@example.com", Password: "api-token",
	})
	if err != nil {
		log.Fatal(err)
	}
	boards, err := c.AddScopesByName(ctx, devlake.PluginJira, jira.ID, devlake.ScopeOptions{}, "TEAM-A board")
	if err != nil {
		log.Fatal(err)
	}

	if _, err := c.EnsureProject(ctx, devlake.ProjectSpec{Name: "team-a", Cron: "0 */6 * * *"}); err != nil {
		log.Fatal(err)
	}
	if err := c.ConnectProject(ctx, "team-a", devlake.PluginGitLab, gitlab.ID, repos[0].ID); err != nil {
		log.Fatal(err)
	}
	if err := c.ConnectProject(ctx, "team-a", devlake.PluginJira, jira.ID, boards[0].ID); err != nil {
		log.Fatal(err)
	}

	syncCtx, cancel := context.WithTimeout(ctx, 2*time.Hour)
	_, err = c.SyncProject(syncCtx, "team-a", devlake.SyncOptions{})
	cancel()
	if err != nil {
		log.Fatal(err)
	}
}

// Report a production deployment from a CI job using a webhook connection's
// API key.
func Example_reportDeployment() {
	ctx := context.Background()
	c, err := devlake.New("https://devlake.example.com/api/rest",
		devlake.WithAPIKey("webhook-api-key"),
		devlake.WithHTTPClient(&http.Client{Timeout: 10 * time.Second}),
	)
	if err != nil {
		log.Fatal(err)
	}
	err = c.Webhook(3).Deployment(ctx, devlake.Deployment{
		ID:         "pipeline-4711",
		StartedAt:  time.Now().Add(-4 * time.Minute),
		FinishedAt: time.Now(),
		Commits:    []devlake.DeploymentCommit{{SHA: "3f2a...", RepoURL: "https://gitlab.example.com/team-a/backend"}},
	})
	if err != nil {
		log.Fatal(err)
	}
}

// Operator check: list projects whose data is stale or broken.
func Example_operatorReport() {
	ctx := context.Background()
	c, err := devlake.New("http://devlake:8080")
	if err != nil {
		log.Fatal(err)
	}
	st, err := c.Status(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("DevLake %s ready=%v running=%d recent failures=%d\n",
		st.Version, st.Ready, len(st.RunningPipelines), len(st.RecentFailures))

	bad, err := c.UnhealthyProjects(ctx, 48*time.Hour)
	if err != nil {
		log.Fatal(err)
	}
	for _, p := range bad {
		fmt.Printf("%s: %s\n", p.Project, p.Problem)
	}
}
