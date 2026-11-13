package armoryclient

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"context"
)

// Token lifetime handling. Battle.net client-credentials tokens last about 24 hours (UNVERIFIED); the
// lifetime comes from expires_in. A token is refreshed once 90% of its lifetime is spent. A missing or
// unusable expires_in means "short" (five minutes), never "forever", and a huge one is capped.
const (
	refreshFraction = 0.9
	defaultLifetime = 5 * time.Minute
	maxLifetime     = 7 * 24 * time.Hour
	maxTokenLen     = 4096
	maxTokenBody    = 64 << 10
)

// TokenSource obtains and caches the OAuth2 client-credentials bearer token. The token lives in memory
// only, as a Secret, and is never logged. It uses the client's transport (no redirects, size cap, limiter,
// retries, breaker) and the client's clock. Concurrent callers share one refresh (single flight).
type TokenSource struct {
	c         *Client
	mu        sync.Mutex // held across a refresh: the single flight
	tok       Secret
	refreshAt time.Time
	last      atomic.Value // string: the latest token, for error scrubbing only
}

func newTokenSource(c *Client) *TokenSource { return &TokenSource{c: c} }

// Token returns a valid token, fetching a new one when none is cached or the cached one is due for refresh.
func (ts *TokenSource) Token(ctx context.Context) (Secret, error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.tok.IsSet() && ts.c.now().Before(ts.refreshAt) {
		return ts.tok, nil
	}
	return ts.fetch(ctx)
}

// Invalidate drops the cached token if it is still the one the caller used (a slow caller holding an old
// token must not discard a fresh one).
func (ts *TokenSource) Invalidate(used Secret) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.tok.Reveal() == used.Reveal() {
		ts.tok = Secret{}
	}
}

// known returns the latest token text for scrubbing errors ("" if none yet).
func (ts *TokenSource) known() string {
	s, _ := ts.last.Load().(string)
	return s
}

func (ts *TokenSource) fetch(ctx context.Context) (Secret, error) {
	c := ts.c
	res, err := c.get(ctx, request{
		method: http.MethodPost, url: c.cfg.TokenURL, body: "grant_type=client_credentials",
		basicID: c.cfg.ClientID, basicSecret: c.cfg.ClientSecret,
	})
	if err != nil {
		return Secret{}, err
	}
	switch {
	case res.status == http.StatusBadRequest || res.status == http.StatusUnauthorized || res.status == http.StatusForbidden:
		return Secret{}, fmt.Errorf("%w: the token endpoint answered %d (client id or secret not accepted; the secret file must hold the client secret, not an access token)", ErrAuth, res.status)
	case res.status != http.StatusOK:
		return Secret{}, &StatusError{Code: res.status}
	}
	var d struct {
		AccessToken string          `json:"access_token"`
		TokenType   string          `json:"token_type"`
		ExpiresIn   json.RawMessage `json:"expires_in"`
	}
	if err := json.Unmarshal(res.body, &d); err != nil {
		return Secret{}, fmt.Errorf("%w: token answer is not the expected JSON", ErrBadResponse)
	}
	if !validToken(d.AccessToken) || (d.TokenType != "" && !strings.EqualFold(d.TokenType, "bearer")) {
		return Secret{}, fmt.Errorf("%w: token answer has no usable bearer token", ErrBadResponse)
	}
	life := time.Duration(min(numberField(d.ExpiresIn), int64(maxLifetime/time.Second))) * time.Second
	if life <= 0 {
		life = defaultLifetime
	}
	ts.tok = NewSecret(d.AccessToken)
	ts.last.Store(d.AccessToken)
	ts.refreshAt = c.now().Add(time.Duration(float64(life) * refreshFraction))
	return ts.tok, nil
}

// validToken accepts printable ASCII without spaces, so a token can never break a header.
func validToken(s string) bool {
	if s == "" || len(s) > maxTokenLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] <= ' ' || s[i] >= 0x7f {
			return false
		}
	}
	return true
}
