// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/harness/harness-cli/pkg/auth"
	"github.com/harness/harness-cli/pkg/hlog"
)

func networkError(err error, apiURL string) error {
	if _, ok := errors.AsType[*net.DNSError](err); ok {
		return fmt.Errorf("cannot reach %s — host not found. Check the API URL in your profile.", apiURL)
	}
	var uerr *url.Error
	if errors.As(err, &uerr) {
		return uerr.Err
	}
	return err
}

func isHTML(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	prefix := strings.ToLower(string(body[:min(len(body), 100)]))
	return body[0] == '<' || strings.Contains(prefix, "<!doctype") || strings.Contains(prefix, "<html")
}

func APIErrorMessage(status int, body []byte) string {
	if isHTML(body) {
		switch status {
		case 401:
			return "Unauthorized — API key is invalid or expired. Verify your token with 'harness auth status'."
		case 403:
			return "Forbidden — access denied. Check your account ID, RBAC permissions, or network restrictions."
		default:
			return "Harness returned an HTML page (possible redirect, proxy, or WAF). Check your API URL and credentials."
		}
	}
	// Try to extract a message from JSON
	var parsed struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &parsed) == nil && parsed.Message != "" {
		return parsed.Message
	}
	if len(body) > 200 {
		return string(body[:200]) + "..."
	}
	return string(body)
}

// Request describes a single HTTP request to the Harness API.
type Request struct {
	Method      string
	Path        string
	QueryParams map[string]string
	// Body is always fully materialized: string → sent as-is with BodyContentType;
	// any other type → JSON-marshaled, BodyContentType defaults to "application/json".
	Body            any
	BodyContentType string
	Headers         map[string]string
}

// Client makes authenticated HTTP requests to the Harness API.
type Client struct {
	ctx      context.Context
	resolved *auth.ResolvedAuth
	http     *http.Client
}

func New(ctx context.Context, resolved *auth.ResolvedAuth) *Client {
	return &Client{
		ctx:      ctx,
		resolved: resolved,
		http:     &http.Client{Timeout: 30 * time.Second},
	}
}

// Get performs a GET request.
func (c *Client) Get(path string, queryParams map[string]string) (any, http.Header, error) {
	return c.do("GET", path, queryParams, nil)
}

// Post performs a POST request with an optional JSON body.
func (c *Client) Post(path string, queryParams map[string]string, body any) (any, http.Header, error) {
	return c.do("POST", path, queryParams, body)
}

// Put performs a PUT request with an optional JSON body.
func (c *Client) Put(path string, queryParams map[string]string, body any) (any, http.Header, error) {
	return c.do("PUT", path, queryParams, body)
}

// Delete performs a DELETE request.
func (c *Client) Delete(path string, queryParams map[string]string) (any, http.Header, error) {
	return c.do("DELETE", path, queryParams, nil)
}

// DeleteWithBody performs a DELETE request with a JSON body.
func (c *Client) DeleteWithBody(path string, queryParams map[string]string, body any) (any, http.Header, error) {
	return c.do("DELETE", path, queryParams, body)
}

// PostRaw performs a POST request with a raw string body and explicit Content-Type.
func (c *Client) PostRaw(path string, queryParams map[string]string, body, contentType string) (any, http.Header, error) {
	return c.DoRequest(Request{Method: "POST", Path: path, QueryParams: queryParams, Body: body, BodyContentType: contentType})
}

// PutRaw performs a PUT request with a raw string body and explicit Content-Type.
func (c *Client) PutRaw(path string, queryParams map[string]string, body, contentType string) (any, http.Header, error) {
	return c.DoRequest(Request{Method: "PUT", Path: path, QueryParams: queryParams, Body: body, BodyContentType: contentType})
}

// DoRequest executes a Request and returns the decoded JSON response body, response headers, and any error.
// If Body is a string, it is sent as-is using BodyContentType. Otherwise Body is JSON-marshaled and
// BodyContentType defaults to "application/json". Extra per-request headers may be set via Headers.
func (c *Client) DoRequest(r Request) (any, http.Header, error) {
	if err := auth.CheckAndUpdateAccessToken(c.resolved, time.Now()); err != nil {
		return nil, nil, err
	}

	u, err := url.Parse(c.resolved.APIUrl + r.Path)
	if err != nil {
		return nil, nil, fmt.Errorf("building URL: %w", err)
	}
	q := u.Query()
	q.Set("accountIdentifier", c.resolved.AccountID)
	for k, v := range r.QueryParams {
		if v != "" {
			q.Set(k, v)
		}
	}
	u.RawQuery = q.Encode()

	var bodyReader io.Reader
	contentType := r.BodyContentType
	if r.Body != nil {
		if s, ok := r.Body.(string); ok {
			bodyReader = strings.NewReader(s)
			hlog.Debug(r.Method + " " + u.Path)
		} else {
			b, err := json.Marshal(r.Body)
			if err != nil {
				return nil, nil, fmt.Errorf("encoding API request body: %w", err)
			}
			hlog.Debug(r.Method+" "+u.Path, "body_bytes", len(b), "body", string(b))
			bodyReader = strings.NewReader(string(b))
			if contentType == "" {
				contentType = "application/json"
			}
		}
	} else {
		hlog.Debug(r.Method + " " + u.Path)
	}

	req, err := http.NewRequestWithContext(c.ctx, r.Method, u.String(), bodyReader)
	if err != nil {
		return nil, nil, fmt.Errorf("creating API request: %w", err)
	}
	if c.resolved.AuthType == auth.AuthTypeSSO {
		req.Header.Set("Authorization", "Bearer "+c.resolved.SSOToken)
	} else {
		req.Header.Set("x-api-key", c.resolved.PATToken)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for k, v := range r.Headers {
		req.Header.Set(k, v)
	}

	start := time.Now()
	resp, err := c.http.Do(req)
	elapsed := time.Since(start).Milliseconds()
	if err != nil {
		nerr := networkError(err, c.resolved.APIUrl)
		hlog.Info(r.Method+" "+u.Path, "ms", elapsed, "error", nerr)
		return nil, nil, fmt.Errorf("API request failed: %w", nerr)
	}
	defer resp.Body.Close()
	hlog.Info(r.Method+" "+u.Path, "status", resp.StatusCode, "ms", elapsed, "url", u.String())

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("reading API response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.Header, fmt.Errorf("API error %d: %s", resp.StatusCode, APIErrorMessage(resp.StatusCode, respBody))
	}
	if len(respBody) == 0 {
		return nil, resp.Header, nil
	}
	var result any
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, nil, fmt.Errorf("malformed API response: %w", err)
	}
	return result, resp.Header, nil
}

func (c *Client) do(method, path string, queryParams map[string]string, body any) (any, http.Header, error) {
	return c.DoRequest(Request{Method: method, Path: path, QueryParams: queryParams, Body: body})
}
