// Package auth signs the CLI in through the dashboard: the OAuth 2.0 device
// authorization grant (RFC 8628) served by Better Auth's device-authorization
// plugin, then short-lived gateway JWTs exchanged from the resulting session.
//
// Flow (dashboard routes):
//
//	POST /api/auth/device/code   {client_id, scope}            -> device_code, user_code, verification_uri[_complete]
//	   the user opens verification_uri_complete, signs in, picks an
//	   organization and approves (app/device)
//	POST /api/auth/device/token  {grant_type, device_code, client_id}
//	                              -> 400 authorization_pending | slow_down | access_denied | expired_token
//	                              -> 200 {access_token: <session token>, expires_in}
//	POST /api/cli/token          Authorization: Bearer <session token>
//	                              -> {token: <EdDSA JWT for the gateway, 15 min>, user, org, role}
//	POST /api/cli/logout         Authorization: Bearer <session token>   (deletes the session)
package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ClientID identifies the CLI to the dashboard's device-authorization plugin,
// which accepts only known client IDs.
const ClientID = "anona-compute"

// DeviceCode is the RFC 8628 section 3.2 device authorization response.
type DeviceCode struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// FormattedCode is the user code as shown to people: ABCD-EFGH.
func (d *DeviceCode) FormattedCode() string {
	c := d.UserCode
	if len(c) == 8 && !strings.Contains(c, "-") {
		return c[:4] + "-" + c[4:]
	}
	return c
}

// OAuthError is an RFC 6749 section 5.2 error body.
type OAuthError struct {
	Status      int    `json:"-"`
	Code        string `json:"error"`
	Description string `json:"error_description"`
	Message     string `json:"message"`
}

func (e *OAuthError) Error() string {
	d := e.Description
	if d == "" {
		d = e.Message
	}
	if e.Code == "" {
		return fmt.Sprintf("HTTP %d: %s", e.Status, d)
	}
	if d == "" {
		return e.Code
	}
	return e.Code + ": " + d
}

// Errors returned by Poll.
var (
	ErrDenied  = errors.New("the sign-in request was denied in the browser")
	ErrExpired = errors.New("the code expired before it was approved; run `anona-compute login` again")
)

// Client talks to the dashboard.
type Client struct {
	WebURL    string // dashboard origin, no trailing slash
	UserAgent string
	HTTP      *http.Client
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (c *Client) post(ctx context.Context, path, bearer string, body any, out any) (int, error) {
	st, _, err := c.postRaw(ctx, path, bearer, body, out)
	return st, err
}

func (c *Client) postRaw(ctx context.Context, path, bearer string, body any, out any) (int, []byte, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.WebURL+path, bytes.NewReader(b))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	res, err := c.http().Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return res.StatusCode, nil, err
	}
	if res.StatusCode >= 300 {
		e := &OAuthError{Status: res.StatusCode}
		if json.Unmarshal(data, e) != nil || (e.Code == "" && e.Description == "" && e.Message == "") {
			msg := strings.TrimSpace(string(data))
			if len(msg) > 200 {
				msg = msg[:200]
			}
			e.Message = msg
		}
		return res.StatusCode, data, e
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return res.StatusCode, data, fmt.Errorf("decode %s response: %w", path, err)
		}
	}
	return res.StatusCode, data, nil
}

// RequestCode starts the device flow (RFC 8628 section 3.1).
func (c *Client) RequestCode(ctx context.Context) (*DeviceCode, error) {
	var d DeviceCode
	if _, err := c.post(ctx, "/api/auth/device/code", "", map[string]string{"client_id": ClientID, "scope": "gateway"}, &d); err != nil {
		return nil, fmt.Errorf("start sign-in at %s: %w", c.WebURL, err)
	}
	if d.DeviceCode == "" || d.UserCode == "" {
		return nil, errors.New("the dashboard returned no device code")
	}
	if d.Interval <= 0 {
		d.Interval = 5
	}
	if d.VerificationURI != "" && strings.HasPrefix(d.VerificationURI, "/") {
		d.VerificationURI = c.WebURL + d.VerificationURI
	}
	if d.VerificationURIComplete != "" && strings.HasPrefix(d.VerificationURIComplete, "/") {
		d.VerificationURIComplete = c.WebURL + d.VerificationURIComplete
	}
	if d.VerificationURIComplete == "" {
		d.VerificationURIComplete = d.VerificationURI + "?user_code=" + d.UserCode
	}
	return &d, nil
}

// Token is the device token response: a dashboard session.
type Token struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
}

// Poll waits for the user to approve (RFC 8628 section 3.4/3.5): it waits
// interval seconds between requests and adds 5 s on slow_down.
func (c *Client) Poll(ctx context.Context, d *DeviceCode) (*Token, error) {
	interval := time.Duration(d.Interval) * time.Second
	deadline := time.Now().Add(time.Duration(d.ExpiresIn) * time.Second)
	if d.ExpiresIn <= 0 {
		deadline = time.Now().Add(30 * time.Minute)
	}
	body := map[string]string{
		"grant_type":  "urn:ietf:params:oauth:grant-type:device_code",
		"device_code": d.DeviceCode,
		"client_id":   ClientID,
	}
	for {
		t := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		case <-t.C:
		}
		var tok Token
		_, err := c.post(ctx, "/api/auth/device/token", "", body, &tok)
		if err == nil {
			if tok.AccessToken == "" {
				return nil, errors.New("the dashboard returned no access token")
			}
			return &tok, nil
		}
		var oe *OAuthError
		if !errors.As(err, &oe) {
			// Network trouble: keep polling until the code expires.
			if time.Now().After(deadline) {
				return nil, err
			}
			continue
		}
		switch oe.Code {
		case "authorization_pending":
		case "slow_down":
			interval += 5 * time.Second
		case "access_denied":
			return nil, ErrDenied
		case "expired_token":
			return nil, ErrExpired
		default:
			return nil, err
		}
		if time.Now().After(deadline) {
			return nil, ErrExpired
		}
	}
}
