package envd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// PTYSize is process.PTY.Size.
type PTYSize struct {
	Cols uint32 `json:"cols"`
	Rows uint32 `json:"rows"`
}

type ptyStartRequest struct {
	Process ProcessConfig `json:"process"`
	PTY     struct {
		Size PTYSize `json:"size"`
	} `json:"pty"`
}

// StartPTY starts cmd on a PTY (process.Process/Start with pty.size), as the
// SDK's sandbox.pty.create does. Output arrives as Event.Data.Pty.
func (c *Client) StartPTY(ctx context.Context, cfg ProcessConfig, size PTYSize) (*Stream, error) {
	var req ptyStartRequest
	req.Process = cfg
	req.PTY.Size = size
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
	hreq.Header.Set("Keepalive-Ping-Interval", "50")
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

func (c *Client) unary(ctx context.Context, method string, body any) error {
	return c.unaryOut(ctx, method, body, nil)
}

// unaryOut calls a unary method and decodes its JSON response into out
// (when out is not nil).
func (c *Client) unaryOut(ctx context.Context, method string, body, out any) error {
	b, _ := json.Marshal(body)
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+"/process.Process/"+method, bytes.NewReader(b))
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
	if out != nil {
		return json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(out)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// SendStdin calls process.Process/SendInput with stdin bytes. The process
// must have been started with StartRequest.Stdin true.
func (c *Client) SendStdin(ctx context.Context, pid uint32, data []byte) error {
	return c.unary(ctx, "SendInput", map[string]any{
		"process": map[string]any{"pid": pid},
		"input":   map[string]any{"stdin": data},
	})
}

// CloseStdin calls process.Process/CloseStdin: the process reads EOF.
func (c *Client) CloseStdin(ctx context.Context, pid uint32) error {
	return c.unary(ctx, "CloseStdin", map[string]any{"process": map[string]any{"pid": pid}})
}

// ProcessInfo is process.ProcessInfo.
type ProcessInfo struct {
	Config ProcessConfig `json:"config"`
	PID    uint32        `json:"pid"`
	Tag    *string       `json:"tag,omitempty"`
}

// List calls process.Process/List: the processes envd started that are
// still running.
func (c *Client) List(ctx context.Context) ([]ProcessInfo, error) {
	var out struct {
		Processes []ProcessInfo `json:"processes"`
	}
	if err := c.unaryOut(ctx, "List", map[string]any{}, &out); err != nil {
		return nil, err
	}
	return out.Processes, nil
}

// SendPTYInput calls process.Process/SendInput with PTY bytes.
func (c *Client) SendPTYInput(ctx context.Context, pid uint32, data []byte) error {
	return c.unary(ctx, "SendInput", map[string]any{
		"process": map[string]any{"pid": pid},
		"input":   map[string]any{"pty": data}, // []byte marshals as base64, as protobuf JSON wants
	})
}

// ResizePTY calls process.Process/Update with a new PTY size.
func (c *Client) ResizePTY(ctx context.Context, pid uint32, size PTYSize) error {
	return c.unary(ctx, "Update", map[string]any{
		"process": map[string]any{"pid": pid},
		"pty":     map[string]any{"size": size},
	})
}
