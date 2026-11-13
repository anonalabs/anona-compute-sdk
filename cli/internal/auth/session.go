package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"
)

// Identity is what the dashboard says about a session.
type Identity struct {
	User struct {
		ID    string `json:"id"`
		Email string `json:"email"`
		Name  string `json:"name"`
	} `json:"user"`
	Org struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
		Name string `json:"name"`
	} `json:"org"`
	Role string `json:"role"`
}

// Exchange is the POST /api/cli/token response.
type Exchange struct {
	Identity
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// OrgChoice is one organization the user belongs to.
type OrgChoice struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
	Role string `json:"role"`
}

// OrgRequiredError: the user belongs to several organizations and the
// session has none selected (409 org_required).
type OrgRequiredError struct {
	Orgs []OrgChoice
}

func (e *OrgRequiredError) Error() string {
	return "you belong to several organizations; pick one with --org"
}

// ErrSessionRevoked means the dashboard no longer knows the session: it was
// signed out, revoked or has expired.
var ErrSessionRevoked = errors.New("the CLI session was revoked or has expired; run `anona-compute login`")

// ExchangeToken trades the session for a gateway JWT acting in org (a slug
// or id; empty keeps the session's organization).
func (c *Client) ExchangeToken(ctx context.Context, session, org string) (*Exchange, error) {
	var out Exchange
	body := map[string]string{}
	if org != "" {
		body["org"] = org
	}
	status, data, err := c.postRaw(ctx, "/api/cli/token", session, body, &out)
	if status == http.StatusUnauthorized {
		return nil, ErrSessionRevoked
	}
	if status == http.StatusConflict {
		var oe struct {
			Error string      `json:"error"`
			Orgs  []OrgChoice `json:"orgs"`
		}
		if json.Unmarshal(data, &oe) == nil && oe.Error == "org_required" {
			return nil, &OrgRequiredError{Orgs: oe.Orgs}
		}
	}
	if err != nil {
		return nil, err
	}
	if out.Token == "" {
		return nil, errors.New("the dashboard returned no gateway token")
	}
	return &out, nil
}

// Logout deletes the session on the dashboard. It reports whether a session
// was deleted (false: it was already gone).
func (c *Client) Logout(ctx context.Context, session string) (bool, error) {
	var out struct {
		Revoked bool `json:"revoked"`
	}
	if _, err := c.post(ctx, "/api/cli/logout", session, map[string]string{}, &out); err != nil {
		return false, err
	}
	return out.Revoked, nil
}

// TokenSource hands out a gateway JWT, exchanging the session again shortly
// before the current token expires. It is safe for concurrent use.
type TokenSource struct {
	Client  *Client
	Session string
	Org     string
	// Skew: refresh this long before expiry (default 60 s).
	Skew time.Duration
	Now  func() time.Time

	mu  sync.Mutex
	cur *Exchange
}

func (s *TokenSource) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Current returns a valid exchange (token plus identity).
func (s *TokenSource) Current(ctx context.Context) (*Exchange, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	skew := s.Skew
	if skew == 0 {
		skew = 60 * time.Second
	}
	if s.cur != nil && s.now().Add(skew).Before(s.cur.ExpiresAt) {
		return s.cur, nil
	}
	ex, err := s.Client.ExchangeToken(ctx, s.Session, s.Org)
	if err != nil {
		return nil, err
	}
	s.cur = ex
	return ex, nil
}

// Invalidate drops the cached token, so the next call exchanges the session again (after the gateway
// refused the token: a role change or a revocation).
func (s *TokenSource) Invalidate() {
	s.mu.Lock()
	s.cur = nil
	s.mu.Unlock()
}

// Token returns the current gateway JWT.
func (s *TokenSource) Token(ctx context.Context) (string, error) {
	ex, err := s.Current(ctx)
	if err != nil {
		return "", err
	}
	return ex.Token, nil
}
