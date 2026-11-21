package api

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// The GitHub Actions runners routes (the gateway's /v1/runners).

// RunnerInstallation is a GitHub App installation linked to the workspace.
type RunnerInstallation struct {
	ID           int64     `json:"id"`
	AccountLogin string    `json:"accountLogin"`
	AccountType  string    `json:"accountType"`
	Suspended    bool      `json:"suspended"`
	LinkedAt     time.Time `json:"linkedAt"`
}

// RunnerInstallations is GET /v1/runners/installations.
type RunnerInstallations struct {
	Eligible      bool                 `json:"eligible"`
	Reason        string               `json:"reason"`
	AllowedSizes  []string             `json:"allowedSizes"`
	Installations []RunnerInstallation `json:"installations"`
}

// RunnerJob is one workflow job the runners service took.
type RunnerJob struct {
	ID         string     `json:"id"`
	Repo       string     `json:"repo"`
	Workflow   string     `json:"workflow"`
	JobName    string     `json:"jobName"`
	Label      string     `json:"label"`
	Size       string     `json:"size"`
	Status     string     `json:"status"`
	Reason     string     `json:"reason"`
	SandboxID  string     `json:"sandboxId"`
	QueuedAt   *time.Time `json:"queuedAt"`
	StartedAt  *time.Time `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt"`
	Conclusion string     `json:"conclusion"`
	GithubURL  string     `json:"githubUrl"`
}

func (c *Client) ListRunnerInstallations(ctx context.Context) (*RunnerInstallations, error) {
	var out RunnerInstallations
	if _, err := c.do(ctx, http.MethodGet, "/v1/runners/installations", nil, nil, &out, http.StatusOK); err != nil {
		return nil, err
	}
	return &out, nil
}

// LinkRunnerInstallation returns the GitHub URL that installs the app.
func (c *Client) LinkRunnerInstallation(ctx context.Context) (string, error) {
	var out struct {
		URL string `json:"url"`
	}
	if _, err := c.do(ctx, http.MethodPost, "/v1/runners/installations/link", nil, struct{}{}, &out, http.StatusOK); err != nil {
		return "", err
	}
	return out.URL, nil
}

func (c *Client) DeleteRunnerInstallation(ctx context.Context, id string) error {
	_, err := c.do(ctx, http.MethodDelete, "/v1/runners/installations/"+url.PathEscape(id), nil, nil, nil, http.StatusNoContent, http.StatusOK)
	return err
}

func (c *Client) ListRunnerJobs(ctx context.Context, repo, status string, limit int) ([]RunnerJob, error) {
	q := url.Values{}
	if repo != "" {
		q.Set("repo", repo)
	}
	if status != "" {
		q.Set("status", status)
	}
	q.Set("limit", strconv.Itoa(limit))
	var out struct {
		Jobs []RunnerJob `json:"jobs"`
	}
	if _, err := c.do(ctx, http.MethodGet, "/v1/runners/jobs", q, nil, &out, http.StatusOK); err != nil {
		return nil, err
	}
	return out.Jobs, nil
}

func (c *Client) GetRunnerJob(ctx context.Context, id string) (*RunnerJob, error) {
	var out RunnerJob
	if _, err := c.do(ctx, http.MethodGet, "/v1/runners/jobs/"+url.PathEscape(id), nil, nil, &out, http.StatusOK); err != nil {
		return nil, err
	}
	return &out, nil
}
