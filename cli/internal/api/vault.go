package api

import (
	"context"
	"net/http"
	"net/url"
	"time"
)

// The credential vault (the gateway's /v1/vault routes).
//
// A value goes in and never comes back out: no response on any of these routes
// carries it, and a sandbox that uses the secret sees only the binding id. That
// is the whole point of the feature, so the CLI has no "get" and no "show".

// VaultBinding is one place a secret is injected: a header on a host, at the
// scope of the policy that named it.
type VaultBinding struct {
	ID         string    `json:"id"`
	SecretName string    `json:"secret"`
	Scope      string    `json:"scope"`
	ScopeID    string    `json:"scopeID"`
	Host       string    `json:"host"`
	Header     string    `json:"header"`
	Format     string    `json:"format"`
	CreatedAt  time.Time `json:"createdAt"`
}

// VaultSecret is what the API lists. Never a value.
type VaultSecret struct {
	Name    string `json:"name"`
	Version int    `json:"version"`
	// AllowedHosts is empty for a secret stored before allowed hosts
	// existed. Nothing may use such a secret until the list is set, and the
	// refusal is a 403 on the sandbox create rather than anything visible
	// here, so the CLI calls it out when it lists.
	AllowedHosts []string       `json:"allowedHosts"`
	CreatedAt    time.Time      `json:"createdAt"`
	UpdatedAt    time.Time      `json:"updatedAt"`
	Bindings     []VaultBinding `json:"bindings"`
}

// VaultList is GET /v1/vault/secrets. Enabled is false when the gateway has no
// key-encryption key configured, in which case every write is a 503.
type VaultList struct {
	Secrets []VaultSecret `json:"secrets"`
	Enabled bool          `json:"enabled"`
	Raw     any           `json:"-"`
}

func vaultSecretPath(name, suffix string) string {
	return "/v1/vault/secrets/" + url.PathEscape(name) + suffix
}

// ListVaultSecrets returns the tenant's secrets and their bindings.
func (c *Client) ListVaultSecrets(ctx context.Context) (*VaultList, error) {
	var out VaultList
	if _, err := c.do(ctx, http.MethodGet, "/v1/vault/secrets", nil, nil, &out, http.StatusOK); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateVaultSecret stores a new secret. allowedHosts is required: a secret
// with no host list cannot be used by anything.
func (c *Client) CreateVaultSecret(ctx context.Context, name, value string, allowedHosts []string) (*VaultSecret, error) {
	body := struct {
		Name         string   `json:"name"`
		Value        string   `json:"value"`
		AllowedHosts []string `json:"allowedHosts"`
	}{name, value, allowedHosts}
	var out VaultSecret
	if _, err := c.do(ctx, http.MethodPost, "/v1/vault/secrets", nil, body, &out, http.StatusCreated); err != nil {
		return nil, err
	}
	return &out, nil
}

// RotateVaultSecret stores a new version and makes it current. A nil
// allowedHosts keeps the recorded list.
func (c *Client) RotateVaultSecret(ctx context.Context, name, value string, allowedHosts []string) (*VaultSecret, error) {
	body := struct {
		Value        string   `json:"value"`
		AllowedHosts []string `json:"allowedHosts,omitempty"`
	}{value, allowedHosts}
	var out VaultSecret
	if _, err := c.do(ctx, http.MethodPost, vaultSecretPath(name, "/versions"), nil, body, &out, http.StatusCreated); err != nil {
		return nil, err
	}
	return &out, nil
}

// SetVaultAllowedHosts replaces the hosts a secret may be injected to.
func (c *Client) SetVaultAllowedHosts(ctx context.Context, name string, allowedHosts []string) (*VaultSecret, error) {
	body := struct {
		AllowedHosts []string `json:"allowedHosts"`
	}{allowedHosts}
	var out VaultSecret
	if _, err := c.do(ctx, http.MethodPut, vaultSecretPath(name, "/allowed-hosts"), nil, body, &out, http.StatusOK); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteVaultSecret deletes a secret and every binding of it.
func (c *Client) DeleteVaultSecret(ctx context.Context, name string) error {
	_, err := c.do(ctx, http.MethodDelete, vaultSecretPath(name, ""), nil, nil, nil, http.StatusNoContent)
	return err
}

// RevokeVaultBinding stops one binding being resolved, leaving the secret.
func (c *Client) RevokeVaultBinding(ctx context.Context, id string) error {
	_, err := c.do(ctx, http.MethodDelete, "/v1/vault/bindings/"+url.PathEscape(id), nil, nil, nil, http.StatusNoContent)
	return err
}
