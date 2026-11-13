package envd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Files follow spec/envd/envd.yaml (GET and POST /files, query path and
// username, multipart field "file") and filesystem.proto ListDir at
// e2b-dev/E2B@ee355b765ac0e884fbeaa69e4b44f4c7c58ed6b1, as the SDKs call them.

func (c *Client) filesURL(path string) string {
	q := url.Values{}
	q.Set("path", path)
	if c.User != "" {
		q.Set("username", c.User)
	}
	return strings.TrimRight(c.BaseURL, "/") + "/files?" + q.Encode()
}

// ReadFile downloads a file, up to limit bytes (it errors when the file is
// larger).
func (c *Client) ReadFile(ctx context.Context, path string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.filesURL(path), nil)
	if err != nil {
		return nil, err
	}
	c.headers(req.Header)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readFilesError(resp)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, limit)
	}
	return data, nil
}

// WriteFile uploads data to path, creating parent directories and
// overwriting an existing file.
func (c *Client) WriteFile(ctx context.Context, path string, data []byte) error {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	name := path
	if i := strings.LastIndex(path, "/"); i >= 0 {
		name = path[i+1:]
	}
	fw, err := mw.CreateFormFile("file", name)
	if err != nil {
		return err
	}
	if _, err := fw.Write(data); err != nil {
		return err
	}
	if err := mw.Close(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.filesURL(path), &body)
	if err != nil {
		return err
	}
	c.headers(req.Header)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return readFilesError(resp)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// readFilesError reads the envd.yaml Error schema {message, code}.
func readFilesError(resp *http.Response) error {
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var e struct {
		Message string `json:"message"`
	}
	msg := strings.TrimSpace(string(data))
	if json.Unmarshal(data, &e) == nil && e.Message != "" {
		msg = e.Message
	}
	if len(msg) > 300 {
		msg = msg[:300]
	}
	return &Error{Code: fmt.Sprintf("http_%d", resp.StatusCode), Message: msg}
}

// Entry is filesystem.EntryInfo in protobuf JSON form.
type Entry struct {
	Name          string  `json:"name"`
	Type          string  `json:"type"`
	Path          string  `json:"path"`
	Size          Int64   `json:"size"`
	Mode          uint32  `json:"mode"`
	Permissions   string  `json:"permissions"`
	Owner         string  `json:"owner"`
	Group         string  `json:"group"`
	ModifiedTime  string  `json:"modifiedTime"`
	SymlinkTarget *string `json:"symlinkTarget,omitempty"`
}

// Int64 decodes a protobuf JSON int64, which is a string ("12") but may be
// a number.
type Int64 int64

// UnmarshalJSON accepts "12" and 12.
func (n *Int64) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*n = 0
		return nil
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return err
	}
	*n = Int64(v)
	return nil
}

// ListDir calls filesystem.Filesystem/ListDir (unary). depth 1 lists the
// directory itself.
func (c *Client) ListDir(ctx context.Context, path string, depth uint32) ([]Entry, error) {
	if depth == 0 {
		depth = 1
	}
	body, _ := json.Marshal(map[string]any{"path": path, "depth": depth})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+"/filesystem.Filesystem/ListDir", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	c.headers(req.Header)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readUnaryError(resp)
	}
	var out struct {
		Entries []Entry `json:"entries"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("envd: bad ListDir response: %w", err)
	}
	return out.Entries, nil
}
