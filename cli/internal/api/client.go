package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Error is components.schemas.Error, plus the HTTP status it came with.
type Error struct {
	Status    int    `json:"-"`
	Code      int32  `json:"code"`
	ErrorCode string `json:"error_code,omitempty"`
	Message   string `json:"message"`
	// RequestID is the gateway's X-Request-Id for the failed request, to
	// quote to support; empty when the response had none.
	RequestID string `json:"-"`
	// OutcomeUnknown is set on a request that was not retried because it is
	// not replayable and a 502, 503 or 504 arrived without Should-Retry: the
	// request may have been carried out (a sandbox may exist and be billed),
	// so the caller must check before sending it again.
	OutcomeUnknown bool `json:"-"`
}

// newError is the Error for a non-success response whose body is data.
func newError(resp *http.Response, data []byte) *Error {
	e := &Error{Status: resp.StatusCode}
	if json.Unmarshal(data, e) != nil || e.Message == "" {
		e.Message = strings.TrimSpace(string(data))
		if len(e.Message) > 300 {
			e.Message = e.Message[:300]
		}
	}
	e.Status = resp.StatusCode
	e.RequestID = resp.Header.Get("X-Request-Id")
	return e
}

func (e *Error) Error() string {
	msg := e.Message
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	var s string
	if e.ErrorCode != "" {
		s = fmt.Sprintf("%s (HTTP %d, %s)", msg, e.Status, e.ErrorCode)
	} else {
		s = fmt.Sprintf("%s (HTTP %d)", msg, e.Status)
	}
	if e.OutcomeUnknown {
		s += "; not retried, because it may already have been carried out: check (for a create, list your sandboxes) before trying again"
	}
	return s
}

// CodeOf returns the error_code of an API error, or "".
func CodeOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.ErrorCode
	}
	return ""
}

// StatusOf returns the HTTP status of an API error, or 0.
func StatusOf(err error) int {
	var e *Error
	if errors.As(err, &e) {
		return e.Status
	}
	return 0
}

// Client calls the E2B-compatible REST API. Authentication is the
// ApiKeyAuth scheme from the spec (header X-API-Key) or, when Token is set,
// a dashboard JWT as Authorization: Bearer (what `anona-compute login` gives).
type Client struct {
	BaseURL string
	APIKey  string
	Token   func(context.Context) (string, error)
	// Refresh, with Token, drops the cached token after the gateway refused it (401): a role change or
	// a session revocation refuses tokens already issued. The request is sent once more with a new one.
	Refresh   func()
	UserAgent string
	HTTP      *http.Client
	// sleep is the context-cancelable wait function the retry loop in do
	// uses between attempts; nil means a real timer selected against
	// ctx.Done(). Tests override it to avoid real multi-second waits or to
	// simulate cancellation. It returns ctx.Err() if ctx is done before (or
	// instead of) the delay elapsing.
	sleep func(context.Context, time.Duration) error
	// now returns the current time; nil means time.Now. Tests override it
	// to exercise maxRetryElapsed without a real multi-second wait.
	now func() time.Time
	// rand returns a value in [0, 1) for the capacity wait's jitter; nil
	// means math/rand/v2. Tests fix it to get exact delays.
	rand func() float64
	// CapacityWait bounds how long a request keeps retrying while the
	// gateway says there is no capacity yet (a 503 with Should-Retry: true
	// and a capacity error code, see isCapacityWait): a pool host is
	// launching, or the sandbox server is waking. Those take minutes, so the
	// ordinary maxRetries / maxRetryElapsed budget would give up while the
	// host it waits for is still starting. Zero means DefaultCapacityWait; a
	// negative value turns the longer wait off.
	CapacityWait time.Duration
	// OnCapacityWait, when set, is told before each capacity wait: the error
	// code, how long until the next try and how long it has waited so far.
	// The CLI prints a progress line from it.
	OnCapacityWait func(code string, next, elapsed time.Duration)
}

// DefaultCapacityWait is how long a request waits for capacity when the
// Client does not say: long enough for a pool host to launch and warm (4-8
// minutes measured on 2026-11-12) with room to spare.
const DefaultCapacityWait = 10 * time.Minute

// capacityCodes are the error codes the gateway sends with a 503 when the
// request will succeed once capacity arrives: no host has room yet (a pool
// host may be launching), the sandbox server is waking from its idle stop,
// or the template is being prepared at the size asked for (about half a
// minute, the first time). Only these get the long wait; any other retryable
// error keeps the short budget. sandbox_engine_unavailable is left out on
// purpose: it is also the answer for a server that is stopped and that this
// request does not wake, which waiting does not change.
var capacityCodes = map[string]bool{
	"sandbox_capacity_unavailable": true,
	"sandbox_engine_starting":      true,
	"sandbox_size_preparing":       true,
}

// isCapacityWait reports a response the gateway marked retryable because
// capacity is on its way: 503, Should-Retry: true, and a capacity code.
func isCapacityWait(resp *http.Response, data []byte) (string, bool) {
	if resp.StatusCode != http.StatusServiceUnavailable {
		return "", false
	}
	if v, present := shouldRetryHeader(resp); !present || !v {
		return "", false
	}
	var body struct {
		ErrorCode string `json:"error_code"`
	}
	if json.Unmarshal(data, &body) != nil || !capacityCodes[body.ErrorCode] {
		return "", false
	}
	return body.ErrorCode, true
}

func (c *Client) capacityWait() time.Duration {
	if c.CapacityWait == 0 {
		return DefaultCapacityWait
	}
	return c.CapacityWait
}

// Bounds of one wait during a capacity wait. Retry-After is held between
// them, so a hint of 0 does not spin and a large one does not sleep through
// the host that arrives meanwhile.
const (
	minCapacityDelay = time.Second
	maxCapacityDelay = 30 * time.Second
)

// capacityRetryDelay is the wait before the next try during a capacity wait:
// the server's Retry-After held between 1s and 30s, else 15s, never past the
// remaining budget.
func capacityRetryDelay(h string, remaining time.Duration) time.Duration {
	d, ok := retryAfterDelay(h)
	if !ok {
		d = 15 * time.Second
	}
	d = min(max(d, minCapacityDelay), maxCapacityDelay)
	if d > remaining {
		d = remaining
	}
	return d
}

// capacityJitter spreads d by up to 10% either way, still within the capacity
// bounds, so the creates of one --count batch that were refused together do
// not all come back in the same instant. rnd returns a value in [0, 1).
func capacityJitter(d time.Duration, rnd func() float64) time.Duration {
	j := time.Duration(float64(d) * (0.9 + 0.2*rnd()))
	return min(max(j, minCapacityDelay), maxCapacityDelay)
}

// maxRetries bounds the number of retries do makes for a retryable status
// (429, 502, 503, 504, the vendored e2b SDK's retry.py set; shouldRetry says
// which requests retry which of them).
const maxRetries = 3

// maxRetryElapsed bounds the *total* time do spends retrying one request,
// across all attempts and their Retry-After/backoff waits. Without this, a
// worst case of repeated 60s engine-starting holds plus 30s Retry-After
// waits can string a caller along for minutes; this caps it in the same
// ballpark as the vendored e2b SDK's _request_deadline (~60s), so a client
// gets a definite answer well before that worst case.
const maxRetryElapsed = 60 * time.Second

// shouldRetryHeader returns the gateway's Should-Retry verdict for resp, and
// whether the header was present at all. When present it is authoritative:
// "true" means retry, anything else (in particular "false") means the
// outcome is unknown or retrying cannot help, and the caller must not retry
// regardless of status code. When absent, shouldRetry falls back to the
// status code.
func shouldRetryHeader(resp *http.Response) (retry bool, present bool) {
	v := resp.Header.Get("Should-Retry")
	if v == "" {
		return false, false
	}
	return strings.EqualFold(v, "true"), true
}

// sleepCtx is the default sleep: a real timer for d, interruptible by ctx
// cancellation.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func isRetryableStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// isUpstreamFailure reports a 502, 503 or 504: a status that a proxy or load
// balancer in front of the gateway can answer after the gateway has already
// acted on the request, so the request's outcome is unknown.
func isUpstreamFailure(code int) bool {
	return code == http.StatusBadGateway || code == http.StatusServiceUnavailable || code == http.StatusGatewayTimeout
}

// shouldRetry decides whether do sends a request again after resp.
//
//   - A Should-Retry header is authoritative for every request: "true"
//     retries whatever the status, anything else never does.
//   - Without one, a replayable request (GET, PUT, DELETE, and the POSTs
//     sent through doReplayable) retries 429, 502, 503 and 504.
//   - Without one, a request that is not replayable (every other POST: a
//     sandbox create or fork, a template build, a new preview link or vault
//     secret) retries only 429, which is refused before any work is done.
//     The gateway's own create path sets Should-Retry on every error, so a
//     502, 503 or 504 without it came from a proxy or load balancer in front
//     of the gateway, which cannot know whether the gateway already started
//     the sandbox; sending the create again could start a second, billed one.
//     Decided 2026-11-11: never retry a create on a 502/503/504 unless the
//     server says Should-Retry: true.
func shouldRetry(resp *http.Response, replayable bool) bool {
	if v, present := shouldRetryHeader(resp); present {
		return v
	}
	if !replayable && isUpstreamFailure(resp.StatusCode) {
		return false
	}
	return isRetryableStatus(resp.StatusCode)
}

// retryAfterDelay parses a Retry-After header (seconds) if present and
// valid.
func retryAfterDelay(h string) (time.Duration, bool) {
	if h == "" {
		return 0, false
	}
	secs, err := strconv.Atoi(h)
	if err != nil || secs < 0 {
		return 0, false
	}
	return time.Duration(secs) * time.Second, true
}

// retryBackoff is the exponential backoff for the n-th retry (0-indexed):
// 500ms, 1s, 2s, ..., capped at 5s.
func retryBackoff(n int) time.Duration {
	d := 500 * time.Millisecond << n
	if d > 5*time.Second {
		d = 5 * time.Second
	}
	return d
}

func (c *Client) authorize(ctx context.Context, req *http.Request) error {
	if c.Token != nil {
		t, err := c.Token(ctx)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+t)
		return nil
	}
	req.Header.Set("X-API-Key", c.APIKey)
	return nil
}

// do sends one API request, retrying it as shouldRetry allows, and decodes a
// response whose status is one of okStatus into out. Every POST through do
// is treated as not replayable; doReplayable is for one that is safe to send
// twice.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body any, out any, okStatus ...int) (*http.Response, error) {
	return c.send(ctx, method != http.MethodPost, method, path, query, body, out, okStatus...)
}

// doReplayable is do for a POST whose effect is the same however many times
// it arrives (pause, resume, set a timeout, revoke links, mint a short-lived
// credential), so it keeps a GET's status-code retry rule.
func (c *Client) doReplayable(ctx context.Context, method, path string, query url.Values, body any, out any, okStatus ...int) (*http.Response, error) {
	return c.send(ctx, true, method, path, query, body, out, okStatus...)
}

func (c *Client) send(ctx context.Context, replayable bool, method, path string, query url.Values, body any, out any, okStatus ...int) (*http.Response, error) {
	u := strings.TrimRight(c.BaseURL, "/") + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var payload []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		payload = b
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	sleep := c.sleep
	if sleep == nil {
		sleep = sleepCtx
	}
	now := c.now
	if now == nil {
		now = time.Now
	}
	rnd := c.rand
	if rnd == nil {
		rnd = rand.Float64
	}
	var resp *http.Response
	var data []byte
	retries := 0
	start := now()
	for attempt := 0; ; attempt++ {
		var rd io.Reader
		if body != nil {
			rd = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, u, rd)
		if err != nil {
			return nil, err
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("Accept", "application/json")
		if err := c.authorize(ctx, req); err != nil {
			return nil, err
		}
		if c.UserAgent != "" {
			req.Header.Set("User-Agent", c.UserAgent)
		}
		resp, err = hc.Do(req)
		if err != nil {
			return nil, err
		}
		data, err = io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		resp.Body.Close()
		if err != nil {
			return resp, err
		}
		if resp.StatusCode == http.StatusUnauthorized && c.Token != nil && c.Refresh != nil && attempt == 0 {
			c.Refresh()
			continue
		}
		if code, ok := isCapacityWait(resp, data); ok && c.capacityWait() > 0 {
			elapsed := now().Sub(start)
			if remaining := c.capacityWait() - elapsed; remaining > 0 {
				d := min(capacityJitter(capacityRetryDelay(resp.Header.Get("Retry-After"), remaining), rnd), remaining)
				if c.OnCapacityWait != nil {
					c.OnCapacityWait(code, d, elapsed)
				}
				if err := sleep(ctx, d); err != nil {
					return resp, err
				}
				continue
			}
		}
		if shouldRetry(resp, replayable) && retries < maxRetries && now().Sub(start) < maxRetryElapsed {
			d, ok := retryAfterDelay(resp.Header.Get("Retry-After"))
			if !ok {
				d = retryBackoff(retries)
			}
			retries++
			if err := sleep(ctx, d); err != nil {
				return resp, err
			}
			continue
		}
		break
	}
	ok := false
	for _, s := range okStatus {
		if resp.StatusCode == s {
			ok = true
		}
	}
	if !ok {
		e := newError(resp, data)
		if _, present := shouldRetryHeader(resp); !present && !replayable && isUpstreamFailure(resp.StatusCode) {
			e.OutcomeUnknown = true
		}
		return resp, e
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return resp, fmt.Errorf("decode %s %s response: %w", method, path, err)
		}
	}
	return resp, nil
}

func sandboxPath(id string, suffix string) string {
	return "/sandboxes/" + url.PathEscape(id) + suffix
}

// CreateSandbox calls POST /v2/sandboxes (201 → Sandbox).
func (c *Client) CreateSandbox(ctx context.Context, req NewSandboxV2) (*Sandbox, error) {
	var out Sandbox
	if _, err := c.do(ctx, http.MethodPost, "/v2/sandboxes", nil, req, &out, http.StatusCreated); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListParams are the query parameters of GET /v2/sandboxes.
type ListParams struct {
	Metadata  map[string]string
	States    []string
	NextToken string
	Limit     int
}

// ListSandboxes calls GET /v2/sandboxes once and returns one page plus the
// X-Next-Token header.
func (c *Client) ListSandboxes(ctx context.Context, p ListParams) ([]ListedSandbox, string, error) {
	q := url.Values{}
	if len(p.Metadata) > 0 {
		md := url.Values{}
		for k, v := range p.Metadata {
			md.Set(k, v)
		}
		// The spec says each key and value must be URL encoded inside the
		// metadata parameter ("user=abc&app=prod").
		q.Set("metadata", md.Encode())
	}
	if len(p.States) > 0 {
		// style: form, explode: false → comma separated.
		q.Set("state", strings.Join(p.States, ","))
	}
	if p.NextToken != "" {
		q.Set("nextToken", p.NextToken)
	}
	if p.Limit > 0 {
		q.Set("limit", strconv.Itoa(p.Limit))
	}
	var out []ListedSandbox
	resp, err := c.do(ctx, http.MethodGet, "/v2/sandboxes", q, nil, &out, http.StatusOK)
	if err != nil {
		return nil, "", err
	}
	return out, resp.Header.Get("X-Next-Token"), nil
}

// GetSandbox calls GET /sandboxes/{sandboxID}.
func (c *Client) GetSandbox(ctx context.Context, id string) (*SandboxDetail, error) {
	var out SandboxDetail
	if _, err := c.do(ctx, http.MethodGet, sandboxPath(id, ""), nil, nil, &out, http.StatusOK); err != nil {
		return nil, err
	}
	return &out, nil
}

// KillSandbox calls DELETE /sandboxes/{sandboxID} (204).
func (c *Client) KillSandbox(ctx context.Context, id string) error {
	_, err := c.do(ctx, http.MethodDelete, sandboxPath(id, ""), nil, nil, nil, http.StatusNoContent)
	return err
}

// PauseSandbox calls POST /sandboxes/{sandboxID}/pause with
// SandboxPauseRequest{memory}. It returns alreadyPaused=true on 409, which
// the E2B SDK also treats as "already paused".
func (c *Client) PauseSandbox(ctx context.Context, id string, memory bool) (alreadyPaused bool, err error) {
	body := map[string]bool{"memory": memory}
	_, err = c.doReplayable(ctx, http.MethodPost, sandboxPath(id, "/pause"), nil, body, nil, http.StatusNoContent)
	if StatusOf(err) == http.StatusConflict {
		return true, nil
	}
	return false, err
}

// ConnectSandbox calls POST /v2/sandboxes/{sandboxID}/connect with
// ConnectSandboxV2{timeout}. It resumes a paused sandbox (201) or returns
// a running one (200).
func (c *Client) ConnectSandbox(ctx context.Context, id string, timeoutSec int32) (sb *Sandbox, resumed bool, err error) {
	var out Sandbox
	var body any
	if timeoutSec != 0 {
		body = map[string]int32{"timeout": timeoutSec}
	}
	resp, err := c.doReplayable(ctx, http.MethodPost, "/v2"+sandboxPath(id, "/connect"), nil, body, &out, http.StatusOK, http.StatusCreated)
	if err != nil {
		return nil, false, err
	}
	return &out, resp.StatusCode == http.StatusCreated, nil
}

// SetTimeout calls POST /sandboxes/{sandboxID}/timeout (204).
func (c *Client) SetTimeout(ctx context.Context, id string, timeoutSec int32) error {
	body := map[string]int32{"timeout": timeoutSec}
	_, err := c.doReplayable(ctx, http.MethodPost, sandboxPath(id, "/timeout"), nil, body, nil, http.StatusNoContent)
	return err
}

// ForkSandbox calls POST /sandboxes/{sandboxID}/fork with
// SandboxForkRequest{count, timeout} (201 → []SandboxForkResult).
func (c *Client) ForkSandbox(ctx context.Context, id string, count int32, timeoutSec *int32) ([]SandboxForkResult, error) {
	body := map[string]int32{"count": count}
	if timeoutSec != nil {
		body["timeout"] = *timeoutSec
	}
	var out []SandboxForkResult
	if _, err := c.do(ctx, http.MethodPost, sandboxPath(id, "/fork"), nil, body, &out, http.StatusCreated); err != nil {
		return nil, err
	}
	return out, nil
}

// Metrics calls GET /sandboxes/{sandboxID}/metrics. start and end are Unix
// seconds and may be zero for "every sample the gateway has".
func (c *Client) Metrics(ctx context.Context, id string, start, end int64) ([]SandboxMetric, error) {
	q := url.Values{}
	if start > 0 {
		q.Set("start", strconv.FormatInt(start, 10))
	}
	if end > 0 {
		q.Set("end", strconv.FormatInt(end, 10))
	}
	var out []SandboxMetric
	if _, err := c.do(ctx, http.MethodGet, sandboxPath(id, "/metrics"), q, nil, &out, http.StatusOK); err != nil {
		return nil, err
	}
	return out, nil
}

// LogsParams are the query parameters of GET /v2/sandboxes/{id}/logs.
type LogsParams struct {
	Cursor *time.Time // start timestamp, sent in milliseconds
	Limit  int
	Level  string
	Search string
}

// Logs calls GET /v2/sandboxes/{sandboxID}/logs with direction=forward.
func (c *Client) Logs(ctx context.Context, id string, p LogsParams) ([]SandboxLogEntry, error) {
	return c.logs(ctx, id, p, "forward")
}

// LogsBackward asks the same route for the newest lines first, which is what
// a caller that only wants the tail should ask for.
func (c *Client) LogsBackward(ctx context.Context, id string, p LogsParams) ([]SandboxLogEntry, error) {
	return c.logs(ctx, id, p, "backward")
}

func (c *Client) logs(ctx context.Context, id string, p LogsParams, direction string) ([]SandboxLogEntry, error) {
	q := url.Values{}
	q.Set("direction", direction)
	if p.Cursor != nil {
		q.Set("cursor", strconv.FormatInt(p.Cursor.UnixMilli(), 10))
	}
	if p.Limit > 0 {
		q.Set("limit", strconv.Itoa(p.Limit))
	}
	if p.Level != "" {
		q.Set("level", p.Level)
	}
	if p.Search != "" {
		q.Set("search", p.Search)
	}
	var out SandboxLogsV2Response
	if _, err := c.do(ctx, http.MethodGet, "/v2"+sandboxPath(id, "/logs"), q, nil, &out, http.StatusOK); err != nil {
		return nil, err
	}
	return out.Logs, nil
}
