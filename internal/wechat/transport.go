// Package wechat implements remoteapi.PuppetAPI against the WeChat HTTP
// protocol service described by swagger.json.
//
// This file is the low-level transport: it knows about HTTP, the common
// response envelope, and authentication query params. The higher-level
// semantic mapping lives in client.go.
package wechat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Envelope is the common response wrapper used by the WeChat API
// (see Msg.SyncResult etc. in swagger.json).
type Envelope struct {
	Code    int             `json:"Code"`
	Success bool            `json:"Success"`
	Message string          `json:"Message"`
	Data    json.RawMessage `json:"Data"`
	Data62  string          `json:"Data62,omitempty"`
}

// APIError is returned when the backend reports a logical failure.
type APIError struct {
	Code    int
	Message string
	Path    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("wechat api %s: code=%d %s", e.Path, e.Code, e.Message)
}

// transport is a thin HTTP client for the WeChat API. The API is keyed by a
// per-account wxid that is appended as a query param on most calls.
type transport struct {
	base string // e.g. https://host/api
	hc   *http.Client
}

func newTransport(baseURL string) *transport {
	return &transport{
		base: strings.TrimRight(baseURL, "/"),
		hc:   &http.Client{Timeout: 70 * time.Second}, // Sync long-polls
	}
}

// post calls path with a JSON body and optional query params, unwraps the
// envelope, and decodes Data into out (out may be nil).
func (t *transport) post(ctx context.Context, path string, body any, query url.Values, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal %s: %w", path, err)
		}
		rdr = bytes.NewReader(b)
	}

	u := t.base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.hc.Do(req)
	if err != nil {
		return fmt.Errorf("http %s: %w", path, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("http %s: status %d: %s", path, resp.StatusCode, snippet(raw))
	}

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("decode envelope %s: %w (%s)", path, err, snippet(raw))
	}
	if !env.Success {
		return &APIError{Code: env.Code, Message: env.Message, Path: path}
	}
	if out != nil && len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return fmt.Errorf("decode data %s: %w", path, err)
		}
	}
	return nil
}

func snippet(b []byte) string {
	if len(b) > 240 {
		return string(b[:240]) + "..."
	}
	return string(b)
}

// q is a tiny helper to build query values.
func q(pairs ...string) url.Values {
	v := url.Values{}
	for i := 0; i+1 < len(pairs); i += 2 {
		v.Set(pairs[i], pairs[i+1])
	}
	return v
}

var _ = context.Background
