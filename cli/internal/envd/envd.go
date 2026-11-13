// Package envd calls the envd process service inside a sandbox the way the
// E2B SDKs do: the Connect protocol with JSON bodies (connect-web with
// useBinaryFormat: false in the JS SDK, packages/js-sdk/src/sandbox/index.ts
// at e2b-dev/E2B@ee355b765ac0e884fbeaa69e4b44f4c7c58ed6b1).
//
// Messages follow spec/envd/process/process.proto at the same commit, in
// protobuf JSON form (lowerCamelCase names, bytes as base64, enums as names).
// The wire format (5-byte envelopes, end-of-stream flag 0x02 with a JSON
// {error, metadata} body) is the Connect protocol as implemented by
// connectrpc.com/connect; the tests run this client against a connect-go
// server.
package envd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Port is the envd port (packages/js-sdk/src/sandbox/index.ts: envdPort).
const Port = 49983

// ProcessConfig is process.ProcessConfig.
type ProcessConfig struct {
	Cmd  string            `json:"cmd"`
	Args []string          `json:"args,omitempty"`
	Envs map[string]string `json:"envs,omitempty"`
	Cwd  *string           `json:"cwd,omitempty"`
}

// StartRequest is process.StartRequest (no PTY).
type StartRequest struct {
	Process ProcessConfig `json:"process"`
	Tag     *string       `json:"tag,omitempty"`
	Stdin   *bool         `json:"stdin,omitempty"`
}

// Event is process.ProcessEvent; exactly one field is set.
type Event struct {
	Start *struct {
		PID uint32 `json:"pid"`
	} `json:"start,omitempty"`
	Data *struct {
		Stdout []byte `json:"stdout,omitempty"`
		Stderr []byte `json:"stderr,omitempty"`
		Pty    []byte `json:"pty,omitempty"`
	} `json:"data,omitempty"`
	End       *EndEvent `json:"end,omitempty"`
	Keepalive *struct{} `json:"keepalive,omitempty"`
}

// EndEvent is process.ProcessEvent.EndEvent.
type EndEvent struct {
	ExitCode int32   `json:"exitCode"`
	Exited   bool    `json:"exited"`
	Status   string  `json:"status"`
	Error    *string `json:"error,omitempty"`
}

type startResponse struct {
	Event Event `json:"event"`
}

// Signal names from process.Signal. The enum has only SIGTERM and SIGKILL.
const (
	SignalTerm = "SIGNAL_SIGTERM"
	SignalKill = "SIGNAL_SIGKILL"
)

// Error is a Connect error returned by envd.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string {
	if e.Message == "" {
		return "envd: " + e.Code
	}
	return "envd: " + e.Code + ": " + e.Message
}

// Client talks to one sandbox's envd.
type Client struct {
	BaseURL     string // e.g. https://49983-<id>.<domain>
	SandboxID   string
	AccessToken string
	EnvdVersion string
	User        string
	UserAgent   string
	HTTP        *http.Client
}

// URL returns the envd base URL for a sandbox, as the SDK builds it:
// https://{port}-{sandboxID}.{domain} (connectionConfig.ts getSandboxUrl).
func URL(sandboxID, domain string) string {
	return fmt.Sprintf("https://%d-%s.%s", Port, sandboxID, domain)
}

func (c *Client) headers(h http.Header) {
	h.Set("Connect-Protocol-Version", "1")
	h.Set("E2b-Sandbox-Id", c.SandboxID)
	h.Set("E2b-Sandbox-Port", strconv.Itoa(Port))
	if c.AccessToken != "" {
		h.Set("X-Access-Token", c.AccessToken)
	}
	if c.UserAgent != "" {
		h.Set("User-Agent", c.UserAgent)
	}
	if auth := AuthHeader(c.EnvdVersion, c.User); auth != "" {
		h.Set("Authorization", auth)
	}
}

// AuthHeader mirrors authenticationHeader in packages/js-sdk/src/envd/rpc.ts:
// envd older than 0.4.0 needs an explicit user ("user"); newer envd uses
// its default user when no header is sent.
func AuthHeader(envdVersion, user string) string {
	if user == "" && versionLess(envdVersion, "0.4.0") {
		user = "user"
	}
	if user == "" {
		return ""
	}
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"))
}

func versionLess(a, b string) bool {
	pa, pb := parseVersion(a), parseVersion(b)
	if pa == nil {
		return false // unknown version: assume new
	}
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			return pa[i] < pb[i]
		}
	}
	return false
}

func parseVersion(v string) []int {
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return nil
	}
	out := make([]int, 3)
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil
		}
		out[i] = n
	}
	return out
}

// Stream is an open server stream of process events.
type Stream struct {
	body io.ReadCloser
	r    *bufio.Reader
	done bool
}

// Start calls process.Process/Start (server streaming). timeout bounds the
// whole stream; 0 means no limit, as with the SDK's timeout=0.
func (c *Client) Start(ctx context.Context, req StartRequest, timeout time.Duration) (*Stream, error) {
	msg, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	writeEnvelope(&buf, 0, msg)
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+"/process.Process/Start", &buf)
	if err != nil {
		return nil, err
	}
	c.headers(hreq.Header)
	hreq.Header.Set("Content-Type", "application/connect+json")
	// Keepalive-Ping-Interval: 50, as the SDK sends on streams.
	hreq.Header.Set("Keepalive-Ping-Interval", "50")
	if timeout > 0 {
		hreq.Header.Set("Connect-Timeout-Ms", strconv.FormatInt(timeout.Milliseconds(), 10))
	}
	resp, err := c.httpClient().Do(hreq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, readUnaryError(resp)
	}
	return &Stream{body: resp.Body, r: bufio.NewReaderSize(resp.Body, 64<<10)}, nil
}

// Next returns the next event. It returns io.EOF after a clean end of
// stream, or an *Error if envd ended the stream with an error.
func (s *Stream) Next() (*Event, error) {
	if s.done {
		return nil, io.EOF
	}
	var hdr [5]byte
	if _, err := io.ReadFull(s.r, hdr[:]); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, io.ErrUnexpectedEOF // stream ended without end-of-stream message
		}
		return nil, err
	}
	flags := hdr[0]
	n := binary.BigEndian.Uint32(hdr[1:])
	if n > 64<<20 {
		return nil, fmt.Errorf("envd: message too large (%d bytes)", n)
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(s.r, payload); err != nil {
		return nil, err
	}
	if flags&0x01 != 0 {
		return nil, errors.New("envd: compressed message not supported")
	}
	if flags&0x02 != 0 {
		s.done = true
		var end struct {
			Error *Error `json:"error"`
		}
		if len(payload) > 0 {
			if err := json.Unmarshal(payload, &end); err != nil {
				return nil, fmt.Errorf("envd: bad end-of-stream message: %w", err)
			}
		}
		if end.Error != nil {
			return nil, end.Error
		}
		return nil, io.EOF
	}
	var sr startResponse
	if err := json.Unmarshal(payload, &sr); err != nil {
		return nil, fmt.Errorf("envd: bad message: %w", err)
	}
	return &sr.Event, nil
}

// Close closes the stream. The remote process keeps running.
func (s *Stream) Close() error { return s.body.Close() }

// SendSignal calls process.Process/SendSignal (unary) for a PID.
func (c *Client) SendSignal(ctx context.Context, pid uint32, signal string) error {
	body, _ := json.Marshal(map[string]any{
		"process": map[string]any{"pid": pid},
		"signal":  signal,
	})
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+"/process.Process/SendSignal", bytes.NewReader(body))
	if err != nil {
		return err
	}
	c.headers(hreq.Header)
	hreq.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient().Do(hreq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return readUnaryError(resp)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func readUnaryError(resp *http.Response) error {
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	e := &Error{}
	if json.Unmarshal(data, e) != nil || e.Code == "" {
		msg := strings.TrimSpace(string(data))
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return &Error{Code: fmt.Sprintf("http_%d", resp.StatusCode), Message: msg}
	}
	return e
}

func writeEnvelope(w *bytes.Buffer, flags byte, msg []byte) {
	var hdr [5]byte
	hdr[0] = flags
	binary.BigEndian.PutUint32(hdr[1:], uint32(len(msg)))
	w.Write(hdr[:])
	w.Write(msg)
}
