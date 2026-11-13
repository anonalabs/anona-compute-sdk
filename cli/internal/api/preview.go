package api

import (
	"context"
	"net/http"
	"strconv"
)

// PreviewLink is the POST /v1/sandboxes/{id}/preview-links response (01 GW-16).
type PreviewLink struct {
	URL       string `json:"url"`
	SandboxID string `json:"sandboxID"`
	Port      int    `json:"port"`
	ExpiresAt string `json:"expiresAt"`
}

// AccessSettings is GET /v1/sandboxes/{id}/access.
type AccessSettings struct {
	SandboxID          string `json:"sandboxID"`
	AllowPublicTraffic bool   `json:"allowPublicTraffic"`
	PublicPorts        []int  `json:"publicPorts"`
	IdlePauseMinutes   int    `json:"idlePauseMinutes"`
}

// MintPreviewLink signs a shareable link to one port; ttlSeconds 0 is the
// gateway default (one hour).
func (c *Client) MintPreviewLink(ctx context.Context, id string, port, ttlSeconds int) (*PreviewLink, error) {
	var out PreviewLink
	body := map[string]int{"port": port}
	if ttlSeconds > 0 {
		body["ttlSeconds"] = ttlSeconds
	}
	if _, err := c.do(ctx, http.MethodPost, "/v1"+sandboxPath(id, "/preview-links"), nil, body, &out, http.StatusCreated); err != nil {
		return nil, err
	}
	return &out, nil
}

// RevokePreviewLinks ends every preview link and viewer token of a sandbox.
func (c *Client) RevokePreviewLinks(ctx context.Context, id string) error {
	_, err := c.doReplayable(ctx, http.MethodPost, "/v1"+sandboxPath(id, "/preview-links:revoke-all"), nil, nil, nil, http.StatusNoContent)
	return err
}

// SetPortPublic opens a port without links (public) or makes it private.
func (c *Client) SetPortPublic(ctx context.Context, id string, port int, public bool) (*AccessSettings, error) {
	var out AccessSettings
	if _, err := c.do(ctx, http.MethodPut, "/v1"+sandboxPath(id, "/ports/"+strconv.Itoa(port)), nil, map[string]bool{"public": public}, &out, http.StatusOK); err != nil {
		return nil, err
	}
	return &out, nil
}

// MaxIdlePauseMinutes is the longest idle time the gateway accepts: one week.
const MaxIdlePauseMinutes = 10080

// SetIdlePause sets how long a running sandbox may sit unused before the
// gateway pauses it; 0 turns idle pausing off. This is not the same as pausing
// at the deadline: see the AutoPause type.
func (c *Client) SetIdlePause(ctx context.Context, id string, minutes int) (*AccessSettings, error) {
	var out AccessSettings
	if _, err := c.do(ctx, http.MethodPut, "/v1"+sandboxPath(id, "/auto-pause"), nil, map[string]int{"idleMinutes": minutes}, &out, http.StatusOK); err != nil {
		return nil, err
	}
	return &out, nil
}

// MetaName is the metadata key that holds a sandbox's human name, the same
// key the gateway uses. It is ordinary E2B metadata: set it at
// create and filter on it like any other key.
const MetaName = "name"

// SandboxName is the PUT /v1/sandboxes/{id}/name response.
type SandboxName struct {
	SandboxID string `json:"sandboxID"`
	Name      string `json:"name"`
}

// SetSandboxName renames a sandbox; "" removes its name. Other metadata is
// left alone.
func (c *Client) SetSandboxName(ctx context.Context, id, name string) (*SandboxName, error) {
	var out SandboxName
	if _, err := c.do(ctx, http.MethodPut, "/v1"+sandboxPath(id, "/name"), nil, map[string]string{"name": name}, &out, http.StatusOK); err != nil {
		return nil, err
	}
	return &out, nil
}

// Access reads a sandbox's sharing and idle settings.
func (c *Client) Access(ctx context.Context, id string) (*AccessSettings, error) {
	var out AccessSettings
	if _, err := c.do(ctx, http.MethodGet, "/v1"+sandboxPath(id, "/access"), nil, nil, &out, http.StatusOK); err != nil {
		return nil, err
	}
	return &out, nil
}
