package api

import (
	"context"
	"net/http"
	"net/url"
	"strings"
)

// ViewerTokenRequest is the body of POST /v1/sandboxes/{id}/viewer-tokens.
type ViewerTokenRequest struct {
	Scope      string   `json:"scope"`
	Channels   []string `json:"channels"`
	TTLSeconds int      `json:"ttlSeconds,omitempty"`
}

// ViewerToken is the mint response.
type ViewerToken struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expiresAt"`
	EmbedURL  string `json:"embedUrl"`
}

// StreamOrigin returns the stream host origin (scheme://host) from EmbedURL.
func (t *ViewerToken) StreamOrigin() (string, error) {
	u, err := url.Parse(t.EmbedURL)
	if err != nil {
		return "", err
	}
	return u.Scheme + "://" + u.Host, nil
}

// SocketURL returns the ws(s) URL of a stream host path such as /v1/tcp.
func (t *ViewerToken) SocketURL(path string) (string, error) {
	o, err := t.StreamOrigin()
	if err != nil {
		return "", err
	}
	return "ws" + strings.TrimPrefix(o, "http") + path, nil
}

// MintViewerToken calls POST /v1/sandboxes/{id}/viewer-tokens.
func (c *Client) MintViewerToken(ctx context.Context, id string, req ViewerTokenRequest) (*ViewerToken, error) {
	var out ViewerToken
	if _, err := c.doReplayable(ctx, http.MethodPost, "/v1"+sandboxPath(id, "/viewer-tokens"), nil, req, &out, http.StatusOK); err != nil {
		return nil, err
	}
	return &out, nil
}

// SSHCert is the POST /v1/sandboxes/{id}/ssh-certs response.
type SSHCert struct {
	Certificate  string `json:"certificate"`
	User         string `json:"user"`
	Host         string `json:"host"`
	ProxyCommand string `json:"proxyCommand"`
	ExpiresAt    string `json:"expiresAt"`
}

// SSHCertificate asks the gateway to sign an OpenSSH public key.
func (c *Client) SSHCertificate(ctx context.Context, id, publicKey string) (*SSHCert, error) {
	var out SSHCert
	body := map[string]string{"publicKey": publicKey}
	if _, err := c.doReplayable(ctx, http.MethodPost, "/v1"+sandboxPath(id, "/ssh-certs"), nil, body, &out, http.StatusOK); err != nil {
		return nil, err
	}
	return &out, nil
}
