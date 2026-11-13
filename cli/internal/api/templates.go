package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Template is components.schemas.Template, plus the gateway's "phase".
type Template struct {
	TemplateID  string    `json:"templateID"`
	BuildID     string    `json:"buildID"`
	CPUCount    int       `json:"cpuCount"`
	MemoryMB    int       `json:"memoryMB"`
	DiskSizeMB  int       `json:"diskSizeMB"`
	Public      bool      `json:"public"`
	Names       []string  `json:"names"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
	BuildCount  int       `json:"buildCount"`
	EnvdVersion string    `json:"envdVersion"`
	BuildStatus string    `json:"buildStatus"`
	Phase       string    `json:"phase,omitempty"`
	// ImageBytes is the uncompressed size of the image the template resolves
	// to; StorageBytes the image bytes it keeps (that build and the one
	// before it), which is what counts against the team's template storage.
	// The gateway's extensions; zero when it does not state them.
	ImageBytes   int64 `json:"imageBytes,omitempty"`
	StorageBytes int64 `json:"storageBytes,omitempty"`
	// LastBuild* are set only when the newest build failed after an earlier
	// one succeeded: the template stays ready on that earlier build, and
	// these say what happened to the newest one.
	LastBuildID     string `json:"lastBuildID,omitempty"`
	LastBuildStatus string `json:"lastBuildStatus,omitempty"`
	LastBuildError  string `json:"lastBuildError,omitempty"`
}

// TemplateUsage is GET /v1/templates/usage: the team's templates and template
// storage against its plan's limits. MaxStorageBytes is nil when the plan sets
// no storage limit.
type TemplateUsage struct {
	Templates       int    `json:"templates"`
	MaxTemplates    int    `json:"maxTemplates"`
	StorageBytes    int64  `json:"storageBytes"`
	MaxStorageBytes *int64 `json:"maxStorageBytes"`
	MaxImageBytes   int64  `json:"maxImageBytes"`
}

// TemplateUsage calls GET /v1/templates/usage. A gateway older than it, or
// one without the template builder, answers 404.
func (c *Client) TemplateUsage(ctx context.Context) (*TemplateUsage, error) {
	var out TemplateUsage
	_, err := c.do(ctx, http.MethodGet, "/v1/templates/usage", nil, nil, &out, http.StatusOK)
	return &out, err
}

// TemplateBuild is components.schemas.TemplateBuild plus Anona fields.
type TemplateBuild struct {
	BuildID    string     `json:"buildID"`
	Status     string     `json:"status"`
	Phase      string     `json:"phase,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	UpdatedAt  time.Time  `json:"updatedAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	CPUCount   int        `json:"cpuCount"`
	MemoryMB   int        `json:"memoryMB"`
	Error      string     `json:"error,omitempty"`
	ImageRef   string     `json:"imageRef,omitempty"`
	ImageBytes int64      `json:"imageBytes,omitempty"`
}

// TemplateWithBuilds is GET /templates/{templateID}.
type TemplateWithBuilds struct {
	Template
	Builds []TemplateBuild `json:"builds"`
}

// TemplateBuildRequest is POST /v3/templates.
type TemplateBuildRequest struct {
	Name     string   `json:"name"`
	Tags     []string `json:"tags,omitempty"`
	CPUCount int      `json:"cpuCount,omitempty"`
	MemoryMB int      `json:"memoryMB,omitempty"`
	// DiskSizeMB is the writable disk, in MiB (the gateway's extension).
	// Use WithDisk, which also sets the E2B field for an older gateway.
	DiskSizeMB int `json:"diskSizeMB,omitempty"`
	// MinFreeDiskMb is E2B's field: free space after the build, in MiB. The
	// gateway sizes the disk at it + 512.
	MinFreeDiskMb int `json:"minFreeDiskMb,omitempty"`
}

// templateDiskOverheadMB is what the gateway adds to minFreeDiskMb for the
// filesystem (internal/templates/api.go diskOverheadMB).
const templateDiskOverheadMB = 512

// WithDisk asks for a writable disk of exactly mib MiB. It sends both the
// gateway's diskSizeMB and the E2B minFreeDiskMb that comes to the same disk,
// so a gateway from before diskSizeMB existed still builds the size asked
// for instead of the 1 GiB default. mib 0 leaves the default.
func (r TemplateBuildRequest) WithDisk(mib int) TemplateBuildRequest {
	if mib > 0 {
		r.DiskSizeMB = mib
		r.MinFreeDiskMb = max(mib-templateDiskOverheadMB, 1)
	}
	return r
}

// TemplateBuildRequested is the 202 of POST /v3/templates. The sizes are the
// gateway's extension, the size the build was accepted at; zero from a
// gateway that does not report them.
type TemplateBuildRequested struct {
	TemplateID string   `json:"templateID"`
	BuildID    string   `json:"buildID"`
	Names      []string `json:"names"`
	Tags       []string `json:"tags"`
	CPUCount   int      `json:"cpuCount,omitempty"`
	MemoryMB   int      `json:"memoryMB,omitempty"`
	DiskSizeMB int      `json:"diskSizeMB,omitempty"`
}

// DockerfileBuild is the body of POST /v1/templates/{id}/builds/{build}/dockerfile.
type DockerfileBuild struct {
	Dockerfile  string `json:"dockerfile"`
	ContextHash string `json:"contextHash,omitempty"`
	StartCmd    string `json:"startCmd,omitempty"`
	NoCache     bool   `json:"noCache,omitempty"`
}

// BuildEvent is one line of the build event stream.
type BuildEvent struct {
	Type      string    `json:"type"` // log | status
	Timestamp time.Time `json:"timestamp,omitempty"`
	Level     string    `json:"level,omitempty"`
	Step      string    `json:"step,omitempty"`
	Message   string    `json:"message,omitempty"`
	Status    string    `json:"status,omitempty"` // E2B: waiting | building | ready | error
	Phase     string    `json:"phase,omitempty"`
	Error     string    `json:"error,omitempty"`
	ImageRef  string    `json:"imageRef,omitempty"`
}

func tplPath(id string, suffix string) string { return "/templates/" + url.PathEscape(id) + suffix }

// ListTemplates calls GET /v2/templates.
func (c *Client) ListTemplates(ctx context.Context) ([]Template, error) {
	var out []Template
	_, err := c.do(ctx, http.MethodGet, "/v2/templates", nil, nil, &out, http.StatusOK)
	return out, err
}

// CatalogEntry is one template of GET /v1/template-catalog (the gateway's
// handlers/catalog Entry): a template the caller may launch.
type CatalogEntry struct {
	TemplateID string   `json:"templateID"`
	Label      string   `json:"label"`
	Aliases    []string `json:"aliases,omitempty"`
	// Public is true for a platform (built-in) template, false for one the
	// caller's team built.
	Public      bool   `json:"public"`
	Description string `json:"description,omitempty"`
	// CPUCount and MemoryMB are nil when no source states the size.
	CPUCount   *int `json:"cpuCount"`
	MemoryMB   *int `json:"memoryMB"`
	DiskSizeMB *int `json:"diskSizeMB"`
	// MinimumSize is the smallest sandbox size that meets the template's
	// minimums; Sizes is every size the caller may create it at, smallest
	// first; DefaultSize is the one to suggest.
	MinimumSize string       `json:"minimumSize,omitempty"`
	Sizes       []SizeOption `json:"sizes,omitempty"`
	DefaultSize string       `json:"defaultSize,omitempty"`
}

// SizeOption is one sandbox size a template may be created at.
type SizeOption struct {
	Name       string `json:"name"`
	CPUCount   int    `json:"cpuCount"`
	MemoryMB   int    `json:"memoryMB"`
	DiskSizeMB int    `json:"diskSizeMB"`
}

// Catalog is GET /v1/template-catalog. Engine is set only while the engine
// is asleep: the list then comes from the gateway's records alone and is not
// verified against the engine.
type Catalog struct {
	Templates []CatalogEntry `json:"templates"`
	Engine    *struct {
		State    string `json:"state"`
		Verified bool   `json:"verified"`
	} `json:"engine,omitempty"`
}

// TemplateCatalog calls GET /v1/template-catalog: the platform templates and
// the caller's own that can be launched now. It is the only route that
// lists platform templates; GET /v2/templates lists the team's own only.
func (c *Client) TemplateCatalog(ctx context.Context) (*Catalog, error) {
	var out Catalog
	_, err := c.do(ctx, http.MethodGet, "/v1/template-catalog", nil, nil, &out, http.StatusOK)
	return &out, err
}

// GetTemplate calls GET /templates/{templateID} (an ID or alias).
func (c *Client) GetTemplate(ctx context.Context, ref string) (*TemplateWithBuilds, error) {
	var out TemplateWithBuilds
	_, err := c.do(ctx, http.MethodGet, tplPath(ref, ""), nil, nil, &out, http.StatusOK)
	return &out, err
}

// DeleteTemplate calls DELETE /templates/{templateID}.
func (c *Client) DeleteTemplate(ctx context.Context, ref string) error {
	_, err := c.do(ctx, http.MethodDelete, tplPath(ref, ""), nil, nil, nil, http.StatusNoContent)
	return err
}

// RequestTemplateBuild calls POST /v3/templates.
func (c *Client) RequestTemplateBuild(ctx context.Context, req TemplateBuildRequest) (*TemplateBuildRequested, error) {
	var out TemplateBuildRequested
	_, err := c.do(ctx, http.MethodPost, "/v3/templates", nil, req, &out, http.StatusAccepted)
	return &out, err
}

// TemplateFileLink calls GET /templates/{id}/files/{hash}.
func (c *Client) TemplateFileLink(ctx context.Context, templateID, hash string) (present bool, uploadURL string, err error) {
	var out struct {
		Present bool   `json:"present"`
		URL     string `json:"url"`
	}
	_, err = c.do(ctx, http.MethodGet, tplPath(templateID, "/files/"+hash), nil, nil, &out, http.StatusCreated)
	return out.Present, out.URL, err
}

// UploadTemplateFile PUTs a build context to a signed upload URL (no API key).
func (c *Client) UploadTemplateFile(ctx context.Context, uploadURL string, body io.Reader, size int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, uploadURL, body)
	if err != nil {
		return err
	}
	req.ContentLength = size
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return newError(resp, b)
	}
	return nil
}

// StartDockerfileBuild calls POST /v1/templates/{id}/builds/{build}/dockerfile.
func (c *Client) StartDockerfileBuild(ctx context.Context, templateID, buildID string, body DockerfileBuild) error {
	_, err := c.do(ctx, http.MethodPost, "/v1"+tplPath(templateID, "/builds/"+url.PathEscape(buildID)+"/dockerfile"), nil, body, nil, http.StatusAccepted)
	return err
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

// followReopens is how many times in a row FollowTemplateBuild reopens a
// build's event stream that closed without a single event before it gives up.
const followReopens = 5

// TerminalBuildStatus reports whether an E2B build status is final.
func TerminalBuildStatus(s string) bool { return s == "ready" || s == "error" }

// FollowTemplateBuild reads GET /v1/templates/{id}/builds/{build}/events until
// the build ends, calling fn for each event, and returns the last status
// event, which is ready or error unless the error says otherwise.
//
// A stream that closes before a final status - the gateway restarting, a
// proxy cutting an idle response - is reopened from the log line it had
// reached, after a short wait, rather than being taken as the end of the
// build. That used to read as success: the last status was "building", which
// is not "error", so the CLI reported a build ready that was still
// registering. It gives up after followReopens consecutive reopens that
// deliver nothing, and at once on a refusal (4xx).
func (c *Client) FollowTemplateBuild(ctx context.Context, templateID, buildID string, fn func(BuildEvent)) (*BuildEvent, error) {
	sleep := c.sleep
	if sleep == nil {
		sleep = sleepCtx
	}
	var last *BuildEvent
	offset, empty := 0, 0
	for {
		got := 0
		err := c.followOnce(ctx, templateID, buildID, offset, func(ev BuildEvent) {
			got++
			switch ev.Type {
			case "log":
				offset++
			case "status":
				e := ev
				last = &e
			}
			fn(ev)
		})
		if last != nil && TerminalBuildStatus(last.Status) {
			return last, nil
		}
		if ctx.Err() != nil {
			return last, ctx.Err()
		}
		var ae *Error
		if errors.As(err, &ae) && ae.Status >= 400 && ae.Status < 500 && ae.Status != http.StatusTooManyRequests {
			return last, err
		}
		var de *decodeError
		if errors.As(err, &de) {
			return last, err
		}
		if got > 0 {
			empty = 0
		} else if empty++; empty >= followReopens {
			if err == nil {
				err = errors.New("the build's event stream keeps closing without a final status")
			}
			return last, fmt.Errorf("follow build %s: %w", buildID, err)
		}
		if err := sleep(ctx, time.Duration(empty+1)*time.Second); err != nil {
			return last, err
		}
	}
}

// decodeError is an event line that is not JSON: reopening the stream would
// only read it again.
type decodeError struct{ err error }

func (e *decodeError) Error() string { return "decode build event: " + e.err.Error() }
func (e *decodeError) Unwrap() error { return e.err }

// followOnce reads one connection of the event stream from log line offset.
func (c *Client) followOnce(ctx context.Context, templateID, buildID string, offset int, fn func(BuildEvent)) error {
	u := strings.TrimRight(c.BaseURL, "/") + "/v1" + tplPath(templateID, "/builds/"+url.PathEscape(buildID)+"/events")
	if offset > 0 {
		u += "?logsOffset=" + strconv.Itoa(offset)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	if err := c.authorize(ctx, req); err != nil {
		return err
	}
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	// No client timeout: a build can take many minutes.
	hc := *c.httpClient()
	hc.Timeout = 0
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return newError(resp, b)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		var ev BuildEvent
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			return &decodeError{err}
		}
		fn(ev)
	}
	return sc.Err()
}
