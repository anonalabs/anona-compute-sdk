package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestTokenSourceRefreshesBeforeExpiry(t *testing.T) {
	var n atomic.Int32
	var revoked atomic.Bool
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/cli/token" || r.Header.Get("Authorization") != "Bearer s1" || revoked.Load() {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		i := n.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"token": fmt.Sprintf("jwt-%d", i), "expiresAt": time.Now().Add(2 * time.Minute), "role": "member"})
	}))
	defer hs.Close()
	now := time.Now()
	ts := &TokenSource{Client: &Client{WebURL: hs.URL}, Session: "s1", Now: func() time.Time { return now }}
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if tok, err := ts.Token(ctx); err != nil || tok != "jwt-1" {
			t.Fatalf("cached token: %q %v", tok, err)
		}
	}
	now = now.Add(61 * time.Second) // within the 60 s skew of the 2-minute expiry
	if tok, _ := ts.Token(ctx); tok != "jwt-2" {
		t.Fatalf("not refreshed: %q", tok)
	}
	revoked.Store(true)
	now = now.Add(2 * time.Minute)
	if _, err := ts.Token(ctx); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("revoked session: %v", err)
	}
}

func TestPollSlowDownAndExpiry(t *testing.T) {
	var calls atomic.Int32
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		if calls.Add(1) == 1 {
			_, _ = w.Write([]byte(`{"error":"slow_down"}`))
			return
		}
		_, _ = w.Write([]byte(`{"error":"expired_token"}`))
	}))
	defer hs.Close()
	c := &Client{WebURL: hs.URL}
	start := time.Now()
	_, err := c.Poll(context.Background(), &DeviceCode{DeviceCode: "d", Interval: 1, ExpiresIn: 60})
	if !errors.Is(err, ErrExpired) {
		t.Fatalf("err %v", err)
	}
	// 1 s, then 1 + 5 s after slow_down.
	if d := time.Since(start); d < 6*time.Second {
		t.Fatalf("slow_down not honoured: polled again after %v", d)
	}
}
