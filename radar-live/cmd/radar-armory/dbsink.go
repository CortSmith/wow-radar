package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"radarlive/internal/domain"
)

// radar-db internal routes (see internal/mod/writeapi) and the ingest-role header the relay uses.
const (
	batchPath     = "/internal/v1/armory/batch"
	runPath       = "/internal/v1/armory/run"
	dbKeyHeader   = "X-Ingest-Key"
	dbTimeout     = 30 * time.Second
	maxDBRequest  = 8 << 20 // radar-db's default --max-body; a larger batch could never be accepted
	maxDBResponse = 64 << 10
	// A page of 500 maximal seeds (48 + 64 characters plus JSON) is about 120 KB, so seed answers get their own cap.
	maxSeedResponse = 256 << 10
	seedsPath       = "/internal/v1/armory/seeds"
)

// dbError is a failed call to radar-db. Retryable errors (network, 408, 429, 5xx including 503 degraded
// and 507 disk full) may succeed later; the rest (other 4xx, redirects, oversize) will not. The text
// holds a status code or a fixed phrase only, never the key, a URL with credentials or a response body.
type dbError struct {
	Retryable bool
	status    int
	msg       string
}

func (e *dbError) Error() string { return e.msg }

func fatalf(format string, a ...any) error {
	return &dbError{msg: fmt.Sprintf(format, a...)}
}

// dbSink implements armorysync.Sink over radar-db's internal HTTP API.
type dbSink struct {
	base    string
	key     secretVal
	client  *http.Client
	timeout time.Duration
	auth    atomic.Bool // radar-db refused the key (401/403)
}

// newDBSink validates rawURL (https, or http on loopback only) and builds the sink. A nil client gets one
// that never follows redirects and never uses a proxy (the key must go only to the configured URL).
func newDBSink(rawURL string, key secretVal, client *http.Client) (*dbSink, error) {
	if err := checkDBURL(rawURL); err != nil {
		return nil, err
	}
	if client == nil {
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.Proxy = nil
		client = &http.Client{Transport: tr}
	}
	c := *client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &dbSink{base: trimSlash(rawURL), key: key, client: &c, timeout: dbTimeout}, nil
}

func trimSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}

// authFailed reports whether radar-db ever refused the DB key during this run.
func (s *dbSink) authFailed() bool { return s.auth.Load() }

// ApplyBatch posts one batch.
func (s *dbSink) ApplyBatch(ctx context.Context, b domain.ArmoryBatch) (domain.ArmoryApplyResult, error) {
	var res domain.ArmoryApplyResult
	err := s.do(ctx, http.MethodPost, batchPath, b, &res)
	if err == nil && (res.Stored < 0 || res.Skipped < 0 || res.Stored+res.Skipped > len(b.Characters)) {
		// Counts that cannot describe this batch: do not believe the answer, and do not retry it.
		return domain.ArmoryApplyResult{}, fatalf("radar-armory: radar-db sent implausible counts")
	}
	return res, err
}

// RecordRun posts a run checkpoint.
func (s *dbSink) RecordRun(ctx context.Context, r domain.ArmoryRun) error {
	return s.do(ctx, http.MethodPost, runPath, r, nil)
}

// LastRun reads the newest run; ok is false when radar-db has none (404).
func (s *dbSink) LastRun(ctx context.Context) (domain.ArmoryRun, bool, error) {
	var run domain.ArmoryRun
	err := s.do(ctx, http.MethodGet, runPath, nil, &run)
	var de *dbError
	if errors.As(err, &de) && de.status == 404 {
		return domain.ArmoryRun{}, false, nil
	}
	return run, err == nil, err
}

// do performs one request. body (if any) is JSON encoded; out (if any) receives the JSON answer.
func (s *dbSink) do(ctx context.Context, method, path string, body, out any) error {
	return s.doLimit(ctx, method, path, body, out, maxDBResponse)
}

// doLimit is do with an explicit cap on the answer size.
func (s *dbSink) doLimit(ctx context.Context, method, path string, body, out any, maxResp int) error {
	var rd io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fatalf("radar-armory: cannot encode request")
		}
		if len(data) > maxDBRequest {
			return fatalf("radar-armory: request larger than %d bytes", maxDBRequest)
		}
		rd = bytes.NewReader(data)
	}
	rctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, method, s.base+path, rd)
	if err != nil {
		return fatalf("radar-armory: cannot build request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(dbKeyHeader, s.key.Reveal())
	resp, err := s.client.Do(req)
	if err != nil {
		// err text can include the URL; the URL has no credentials (validated) and never the header.
		return &dbError{Retryable: true, msg: "radar-armory: radar-db is unreachable or timed out"}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxResp)+1))
	switch {
	case err != nil:
		return &dbError{Retryable: true, msg: "radar-armory: reading the radar-db answer failed"}
	case len(data) > maxResp:
		return fatalf("radar-armory: radar-db answer larger than %d bytes", maxResp)
	}
	if resp.StatusCode/100 != 2 {
		return s.statusError(resp.StatusCode)
	}
	if out != nil {
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields() // radar-db answers exactly the domain types; anything else is not radar-db
		if err := dec.Decode(out); err != nil || dec.More() {
			return &dbError{Retryable: true, msg: "radar-armory: radar-db sent an unreadable answer"}
		}
	}
	return nil
}

func (s *dbSink) statusError(code int) error {
	if code == http.StatusUnauthorized || code == http.StatusForbidden {
		s.auth.Store(true)
	}
	retry := code == http.StatusRequestTimeout || code == http.StatusTooManyRequests || code >= 500
	return &dbError{Retryable: retry, status: code, msg: fmt.Sprintf("radar-armory: radar-db answered %d", code)}
}

// Seeds reads one page of crawl seeds (GET /internal/v1/armory/seeds?cursor=&limit=, same ingest key as the
// other calls). The answer is decoded strictly (unknown fields, trailing data and wrong types are refused),
// capped in size and item count, items that fail domain validation are dropped one by one, and a cursor
// that does not advance is refused so a lying radar-db cannot make the crawl loop.
func (s *dbSink) Seeds(ctx context.Context, cursor string, limit int) (domain.ArmorySeedPage, error) {
	if domain.ValidateArmorySeedLimit(limit) != nil || !plainCursor(cursor) {
		return domain.ArmorySeedPage{}, fatalf("radar-armory: invalid seed request")
	}
	q := url.Values{"limit": {strconv.Itoa(limit)}}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	var page domain.ArmorySeedPage
	if err := s.doLimit(ctx, http.MethodGet, seedsPath+"?"+q.Encode(), nil, &page, maxSeedResponse); err != nil {
		return domain.ArmorySeedPage{}, err
	}
	if len(page.Items) > limit || !plainCursor(page.Next) || (page.Next != "" && page.Next == cursor) {
		return domain.ArmorySeedPage{}, fatalf("radar-armory: radar-db sent an implausible seed page")
	}
	kept := page.Items[:0]
	for _, it := range page.Items {
		if it.Validate() == nil {
			kept = append(kept, it)
		}
	}
	page.Items = kept
	return page, nil
}

// plainCursor accepts an opaque cursor of printable text up to the domain limit.
func plainCursor(c string) bool {
	if utf8.RuneCountInString(c) > domain.ArmoryCursorMax || !utf8.ValidString(c) {
		return false
	}
	for _, r := range c {
		if unicode.IsControl(r) || r == utf8.RuneError {
			return false
		}
	}
	return true
}
