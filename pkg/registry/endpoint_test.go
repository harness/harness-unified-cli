// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/harness/cli/pkg/auth"
	"github.com/harness/cli/pkg/cmdctx"
	"github.com/harness/cli/pkg/spec"
)

// ---------------------------------------------------------------------------
// Shared context builders
// ---------------------------------------------------------------------------

func testAuth(apiURL string) *auth.ResolvedAuth {
	return &auth.ResolvedAuth{
		APIUrl:    apiURL,
		AccountID: "acct",
		OrgID:     "org",
		ProjectID: "proj",
		PATToken:  "pat.test",
		AuthType:  auth.AuthTypePAT,
	}
}

func testEndpointCtx(apiURL string, flags map[string]any) *cmdctx.Ctx {
	return &cmdctx.Ctx{
		Context:    context.Background(),
		Auth:       testAuth(apiURL),
		FlagValues: flags,
	}
}

// ---------------------------------------------------------------------------
// captureServer — records the single inbound request and returns a fixed body.
// ---------------------------------------------------------------------------

type captured struct {
	method      string
	path        string
	rawQuery    string
	body        []byte
	contentType string
	header      http.Header
}

func captureServer(t *testing.T, resp string) (*httptest.Server, *captured) {
	t.Helper()
	cap := &captured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.method = r.Method
		cap.path = r.URL.Path
		cap.rawQuery = r.URL.RawQuery
		cap.body, _ = io.ReadAll(r.Body)
		cap.contentType = r.Header.Get("Content-Type")
		cap.header = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, resp)
	}))
	t.Cleanup(srv.Close)
	return srv, cap
}

// ---------------------------------------------------------------------------
// sequenceServer — returns a different response for each successive call and
// records all captured requests in order.
// ---------------------------------------------------------------------------

func sequenceServer(t *testing.T, resps []string) (*httptest.Server, *[]captured) {
	t.Helper()
	caps := &[]captured{}
	var mu sync.Mutex
	i := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		cap := captured{}
		cap.method = r.Method
		cap.path = r.URL.Path
		cap.rawQuery = r.URL.RawQuery
		cap.body, _ = io.ReadAll(r.Body)
		cap.contentType = r.Header.Get("Content-Type")
		cap.header = r.Header.Clone()
		*caps = append(*caps, cap)
		resp := "{}"
		if i < len(resps) {
			resp = resps[i]
			i++
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, resp)
	}))
	t.Cleanup(srv.Close)
	return srv, caps
}

// ---------------------------------------------------------------------------
// writeTempFile helper — ext controls the file extension (e.g. ".json", ".yaml").
// ---------------------------------------------------------------------------

func writeTempFile(t *testing.T, content string) string {
	return writeTempFileExt(t, content, ".json")
}

func writeTempFileExt(t *testing.T, content, ext string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "endpoint-*"+ext)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return f.Name()
}

// bodyMap unmarshals a JSON byte slice into map[string]any for assertions.
func bodyMap(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("bodyMap: %v (raw: %s)", err, b)
	}
	return m
}

// queryValues parses a raw query string into url.Values for easy key lookups.
func queryValues(t *testing.T, raw string) url.Values {
	t.Helper()
	v, err := url.ParseQuery(raw)
	if err != nil {
		t.Fatalf("queryValues: %v", err)
	}
	return v
}

// ---------------------------------------------------------------------------
// TestCallEndpointFull_ErrorPaths — early-exit errors before any HTTP call
// ---------------------------------------------------------------------------

func TestCallEndpointFull_ErrorPaths(t *testing.T) {
	tests := []struct {
		name      string
		setupCtx  func(*cmdctx.Ctx)
		ep        *spec.EndpointSpec
		wantErr   string
	}{
		{
			name: "nil_auth",
			setupCtx: func(ctx *cmdctx.Ctx) { ctx.Auth = nil },
			ep:       &spec.EndpointSpec{Path: "/x"},
			wantErr:  "requires auth",
		},
		{
			name:    "file_required_no_flag",
			ep:      &spec.EndpointSpec{Path: "/x", FileBody: spec.FileBodyRequired},
			wantErr: "-f/--file is required",
		},
		{
			name:    "unclosed_path_template",
			ep:      &spec.EndpointSpec{Path: "/items/{{unclosed"},
			wantErr: "resolving path",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := testEndpointCtx("http://unused", nil)
			if tc.setupCtx != nil {
				tc.setupCtx(ctx)
			}
			result, headers, err := callEndpointFull(ctx, tc.ep, nil)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %q, want substring %q", err, tc.wantErr)
			}
			if result != nil || headers != nil {
				t.Fatalf("expected nil result and headers on error, got result=%v headers=%v", result, headers)
			}
		})
	}
}

// TestCallEndpointFull_ValidatorAbort verifies that a failing validator prevents
// the HTTP call and propagates its error.
func TestCallEndpointFull_ValidatorAbort(t *testing.T) {
	srv, cap := captureServer(t, `{}`)

	r := New()
	const id = "test:abort"
	r.RegisterEndpointValidatorFn(id, func(_ *cmdctx.Ctx, _ cmdctx.EndpointRequest) error {
		return errors.New("validator rejected")
	})
	ctx := testEndpointCtx(srv.URL, map[string]any{
		"file": writeTempFile(t, `{}`),
	})
	ctx.Resolver = r
	ep := &spec.EndpointSpec{
		Path:               "/x",
		Method:             "POST",
		FileBody:           spec.FileBodyOptional,
		ValidatorsEndpoint: []string{id},
	}

	_, _, err := callEndpointFull(ctx, ep, nil)
	if err == nil || !strings.Contains(err.Error(), "validator rejected") {
		t.Fatalf("err = %v, want validator rejected", err)
	}
	// Server must not have received any request.
	if cap.method != "" {
		t.Fatal("server received a request after validator abort")
	}
}

// ---------------------------------------------------------------------------
// TestCallEndpointFull_AuthScopeStripping — ctx.Level strips org/project from auth
// ---------------------------------------------------------------------------

func TestCallEndpointFull_AuthScopeStripping(t *testing.T) {
	tests := []struct {
		level           string
		wantOrg         bool
		wantProject     bool
		// also verify ctx.Auth is restored after the call
	}{
		{"", true, true},
		{"org", true, false},
		{"account", false, false},
	}

	for _, tc := range tests {
		t.Run("level_"+tc.level, func(t *testing.T) {
			srv, cap := captureServer(t, `{}`)
			ctx := testEndpointCtx(srv.URL, nil)
			ctx.Level = tc.level
			origAuth := ctx.Auth

			ep := &spec.EndpointSpec{Path: "/x"}
			_, _, err := callEndpointFull(ctx, ep, nil)
			fmt.Printf("err = %v\n", err)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			qv := queryValues(t, cap.rawQuery)
			if tc.wantOrg {
				if qv.Get("orgIdentifier") != "org" {
					t.Fatalf("orgIdentifier = %q, want org", qv.Get("orgIdentifier"))
				}
			} else {
				if qv.Get("orgIdentifier") != "" {
					t.Fatalf("orgIdentifier = %q, want empty", qv.Get("orgIdentifier"))
				}
			}
			if tc.wantProject {
				if qv.Get("projectIdentifier") != "proj" {
					t.Fatalf("projectIdentifier = %q, want proj", qv.Get("projectIdentifier"))
				}
			} else {
				if qv.Get("projectIdentifier") != "" {
					t.Fatalf("projectIdentifier = %q, want empty", qv.Get("projectIdentifier"))
				}
			}

			// defer must have restored the original auth pointer
			if ctx.Auth != origAuth {
				t.Fatal("ctx.Auth was not restored after callEndpointFull returned")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestCallEndpointFull_Priority3_DefaultDispatch — no file, no strategy
// ---------------------------------------------------------------------------

func TestCallEndpointFull_Priority3_DefaultDispatch(t *testing.T) {
	tests := []struct {
		name        string
		ep          *spec.EndpointSpec
		extraQP     map[string]string
		wantMethod  string
		wantPath    string
		wantBodyKey string // non-empty: check cap.body JSON contains this key
		wantBodyNil bool   // true: body should be empty/nil
		wantCT      string
		wantQPKey   string // extra query param key to assert present
	}{
		{
			name:        "GET_default_method",
			ep:          &spec.EndpointSpec{Path: "/items"},
			wantMethod:  "GET",
			wantPath:    "/items",
			wantBodyNil: true,
		},
		{
			name: "POST_with_body_params",
			ep: &spec.EndpointSpec{
				Path:       "/items",
				Method:     "POST",
				BodyParams: map[string]string{"name": `"hello"`},
			},
			wantMethod:  "POST",
			wantPath:    "/items",
			wantBodyKey: "name",
			wantCT:      "application/json",
		},
		{
			name: "PUT_with_body_params",
			ep: &spec.EndpointSpec{
				Path:       "/items/1",
				Method:     "PUT",
				BodyParams: map[string]string{"val": `"v"`},
			},
			wantMethod:  "PUT",
			wantPath:    "/items/1",
			wantBodyKey: "val",
		},
		{
			name: "DELETE_no_body",
			ep: &spec.EndpointSpec{
				Path:   "/items/1",
				Method: "DELETE",
			},
			wantMethod:  "DELETE",
			wantPath:    "/items/1",
			wantBodyNil: true,
		},
		{
			name: "extra_query_params_forwarded",
			ep:   &spec.EndpointSpec{Path: "/items"},
			extraQP: map[string]string{"page": "3"},
			wantMethod: "GET",
			wantPath:   "/items",
			wantQPKey:  "page",
		},
		{
			name: "empty_query_param_expr_omitted",
			ep: &spec.EndpointSpec{
				Path:        "/items",
				QueryParams: map[string]string{"missing": "flags.nonexistent"},
			},
			wantMethod: "GET",
			wantPath:   "/items",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, cap := captureServer(t, `{}`)
			ctx := testEndpointCtx(srv.URL, nil)

			result, _, err := callEndpointFull(ctx, tc.ep, tc.extraQP)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result == nil {
				t.Fatal("result is nil, want non-nil")
			}
			if cap.method != tc.wantMethod {
				t.Fatalf("method = %q, want %q", cap.method, tc.wantMethod)
			}
			if cap.path != tc.wantPath {
				t.Fatalf("path = %q, want %q", cap.path, tc.wantPath)
			}
			if tc.wantBodyNil && len(cap.body) != 0 && string(cap.body) != "null" {
				t.Fatalf("expected empty/nil body, got %s", cap.body)
			}
			if tc.wantBodyKey != "" {
				m := bodyMap(t, cap.body)
				if _, ok := m[tc.wantBodyKey]; !ok {
					t.Fatalf("body missing key %q: %s", tc.wantBodyKey, cap.body)
				}
			}
			if tc.wantCT != "" && !strings.Contains(cap.contentType, tc.wantCT) {
				t.Fatalf("content-type = %q, want %q", cap.contentType, tc.wantCT)
			}
			if tc.wantQPKey != "" {
				qv := queryValues(t, cap.rawQuery)
				if qv.Get(tc.wantQPKey) == "" {
					t.Fatalf("query param %q missing from %q", tc.wantQPKey, cap.rawQuery)
				}
			}
			// assert empty expr param was NOT sent
			if tc.ep.QueryParams != nil {
				for _, expr := range tc.ep.QueryParams {
					if strings.Contains(expr, "nonexistent") {
						qv := queryValues(t, cap.rawQuery)
						if qv.Get("missing") != "" {
							t.Fatalf("empty expr param should be omitted, got %q", cap.rawQuery)
						}
					}
				}
			}
		})
	}
}

// TestCallEndpointFull_Priority3_RequestHeaders verifies that request_headers
// expressions are evaluated and injected into the outbound HTTP request.
func TestCallEndpointFull_Priority3_RequestHeaders(t *testing.T) {
	srv, cap := captureServer(t, `{}`)
	ctx := testEndpointCtx(srv.URL, nil)
	ep := &spec.EndpointSpec{
		Path:           "/items",
		RequestHeaders: map[string]string{"X-Account": "auth.account"},
	}
	_, _, err := callEndpointFull(ctx, ep, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cap.header.Get("X-Account") != "acct" {
		t.Fatalf("X-Account = %q, want acct", cap.header.Get("X-Account"))
	}
}

// TestCallEndpointFull_Priority3_BodyFn verifies that a registered body_fn
// is called and its return value is used as the POST body.
func TestCallEndpointFull_Priority3_BodyFn(t *testing.T) {
	srv, cap := captureServer(t, `{}`)
	r := New()
	const fnID = "test:body"
	r.RegisterBodyFn(fnID, func(*cmdctx.Ctx) (any, error) {
		return map[string]any{"from": "fn"}, nil
	})
	ctx := testEndpointCtx(srv.URL, nil)
	ctx.Resolver = r
	ep := &spec.EndpointSpec{Path: "/items", Method: "POST", BodyFn: fnID}

	_, _, err := callEndpointFull(ctx, ep, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m := bodyMap(t, cap.body)
	if m["from"] != "fn" {
		t.Fatalf("body[from] = %v, want fn", m["from"])
	}
}

// TestCallEndpointFull_Priority3_DeleteWithBody verifies that DELETE with
// body_params sends the body rather than a plain DELETE.
func TestCallEndpointFull_Priority3_DeleteWithBody(t *testing.T) {
	srv, cap := captureServer(t, `{}`)
	ctx := testEndpointCtx(srv.URL, nil)
	ep := &spec.EndpointSpec{
		Path:       "/items/1",
		Method:     "DELETE",
		BodyParams: map[string]string{"reason": `"gone"`},
	}
	_, _, err := callEndpointFull(ctx, ep, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cap.method != "DELETE" {
		t.Fatalf("method = %q, want DELETE", cap.method)
	}
	m := bodyMap(t, cap.body)
	if m["reason"] != "gone" {
		t.Fatalf("body[reason] = %v, want gone", m["reason"])
	}
}

// ---------------------------------------------------------------------------
// TestCallEndpointFull_Priority1_FileBody — file body dispatch
// ---------------------------------------------------------------------------

func TestCallEndpointFull_Priority1_FileBody(t *testing.T) {
	tests := []struct {
		name          string
		fileContent   string
		fileExt       string // defaults to ".json" when empty
		ep            *spec.EndpointSpec
		wantMethod    string
		wantBodyKey   string
		wantBodyVal   any
		wantCT        string
		// for wrap tests: key whose value we inspect in the body map
		wantWrapKey   string
		wantWrapInner string // key inside the wrapped object
	}{
		{
			name:        "json_post_no_wrapping",
			fileContent: `{"id":"a","val":1}`,
			ep: &spec.EndpointSpec{
				Path:     "/items",
				Method:   "POST",
				FileBody: spec.FileBodyOptional,
			},
			wantMethod:  "POST",
			wantBodyKey: "id",
			wantBodyVal: "a",
			wantCT:      "application/json",
		},
		{
			name:        "file_body_wrap_as_string_post",
			fileContent: "name: foo\ntype: step\n",
			fileExt:     ".yaml",
			ep: &spec.EndpointSpec{
				Path:                 "/items",
				Method:               "POST",
				FileBody:             spec.FileBodyOptional,
				FileBodyWrapAsString: "template_yaml",
				FileBodyContentType:  "application/yaml",
			},
			wantMethod:  "POST",
			wantWrapKey: "template_yaml",
		},
		{
			name:        "file_body_wrap_as_string_put",
			fileContent: "name: foo\ntype: step\n",
			fileExt:     ".yaml",
			ep: &spec.EndpointSpec{
				Path:                 "/items/1",
				Method:               "PUT",
				FileBody:             spec.FileBodyOptional,
				FileBodyWrapAsString: "template_yaml",
				FileBodyContentType:  "application/yaml",
			},
			wantMethod:  "PUT",
			wantWrapKey: "template_yaml",
		},
		{
			name:        "update_body_wrap_put_not_wrapped",
			fileContent: `{"name":"svc"}`,
			ep: &spec.EndpointSpec{
				Path:           "/items/1",
				Method:         "PUT",
				FileBody:       spec.FileBodyOptional,
				UpdateBodyWrap: "service",
			},
			wantMethod:    "PUT",
			wantWrapKey:   "service",
			wantWrapInner: "name",
		},
		{
			name:        "update_body_wrap_put_already_wrapped",
			fileContent: `{"service":{"name":"svc"}}`,
			ep: &spec.EndpointSpec{
				Path:           "/items/1",
				Method:         "PUT",
				FileBody:       spec.FileBodyOptional,
				UpdateBodyWrap: "service",
			},
			wantMethod:    "PUT",
			wantWrapKey:   "service",
			wantWrapInner: "name",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, cap := captureServer(t, `{}`)
			ext := tc.fileExt
			if ext == "" {
				ext = ".json"
			}
			ctx := testEndpointCtx(srv.URL, map[string]any{
				"file": writeTempFileExt(t, tc.fileContent, ext),
			})

			_, _, err := callEndpointFull(ctx, tc.ep, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cap.method != tc.wantMethod {
				t.Fatalf("method = %q, want %q", cap.method, tc.wantMethod)
			}
			if tc.wantBodyKey != "" {
				m := bodyMap(t, cap.body)
				if m[tc.wantBodyKey] != tc.wantBodyVal {
					t.Fatalf("body[%s] = %v, want %v", tc.wantBodyKey, m[tc.wantBodyKey], tc.wantBodyVal)
				}
			}
			if tc.wantCT != "" && !strings.Contains(cap.contentType, tc.wantCT) {
				t.Fatalf("content-type = %q, want %q", cap.contentType, tc.wantCT)
			}
			if tc.wantWrapKey != "" {
				m := bodyMap(t, cap.body)
				if _, ok := m[tc.wantWrapKey]; !ok {
					t.Fatalf("body missing wrap key %q: %s", tc.wantWrapKey, cap.body)
				}
				if tc.wantWrapInner != "" {
					inner, ok := m[tc.wantWrapKey].(map[string]any)
					if !ok {
						t.Fatalf("body[%s] is not a map: %T", tc.wantWrapKey, m[tc.wantWrapKey])
					}
					if _, ok := inner[tc.wantWrapInner]; !ok {
						t.Fatalf("inner map missing key %q: %v", tc.wantWrapInner, inner)
					}
				}
			}
		})
	}
}

// TestCallEndpointFull_Priority1_YamlEnvelope verifies that file_body_yaml_envelope
// lifts identity fields and wraps the resource YAML.
func TestCallEndpointFull_Priority1_YamlEnvelope(t *testing.T) {
	srv, cap := captureServer(t, `{}`)
	ctx := testEndpointCtx(srv.URL, map[string]any{
		"file": writeTempFile(t, "identifier: svc1\nname: My Service\n"),
	})
	ep := &spec.EndpointSpec{
		Path:                "/services",
		Method:              "POST",
		FileBody:            spec.FileBodyOptional,
		FileBodyYamlEnvelope: "service",
	}

	_, _, err := callEndpointFull(ctx, ep, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m := bodyMap(t, cap.body)
	if m["identifier"] != "svc1" {
		t.Fatalf("envelope identifier = %v, want svc1", m["identifier"])
	}
	if _, ok := m["yaml"].(string); !ok {
		t.Fatalf("envelope missing yaml string field: %v", m)
	}
}

// TestCallEndpointFull_Priority1_CreateBodyWrap verifies that create_body_wrap +
// create_body_init result in the correct POST body shape and that init fields
// already set by the file are not overwritten.
func TestCallEndpointFull_Priority1_CreateBodyWrap(t *testing.T) {
	tests := []struct {
		name        string
		fileContent string
		ep          *spec.EndpointSpec
		wantWrap    string
		wantInner   map[string]any
	}{
		{
			name:        "wrap_and_init",
			fileContent: `{"name":"svc"}`,
			ep: &spec.EndpointSpec{
				Path:          "/items",
				Method:        "POST",
				FileBody:      spec.FileBodyOptional,
				CreateBodyWrap: "service",
				CreateBodyInit: map[string]string{
					"type": `"Kubernetes"`,
				},
			},
			wantWrap:  "service",
			wantInner: map[string]any{"name": "svc", "type": "Kubernetes"},
		},
		{
			name:        "init_does_not_overwrite_existing",
			fileContent: `{"name":"existing","type":"Custom"}`,
			ep: &spec.EndpointSpec{
				Path:          "/items",
				Method:        "POST",
				FileBody:      spec.FileBodyOptional,
				CreateBodyWrap: "service",
				CreateBodyInit: map[string]string{
					"type": `"Kubernetes"`,
				},
			},
			wantWrap:  "service",
			wantInner: map[string]any{"name": "existing", "type": "Custom"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, cap := captureServer(t, `{}`)
			ctx := testEndpointCtx(srv.URL, map[string]any{
				"file": writeTempFile(t, tc.fileContent),
			})

			_, _, err := callEndpointFull(ctx, tc.ep, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			m := bodyMap(t, cap.body)
			inner, ok := m[tc.wantWrap].(map[string]any)
			if !ok {
				t.Fatalf("body missing wrap key %q: %s", tc.wantWrap, cap.body)
			}
			for k, want := range tc.wantInner {
				if inner[k] != want {
					t.Fatalf("inner[%s] = %v, want %v", k, inner[k], want)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestCallEndpointFull_Priority2_UpdateStrategies — get-then-put/patch/kv
// ---------------------------------------------------------------------------

// testNounRegistry builds a *Registry with a "widget" noun that has a mutable
// "name" field at path "name".
func testNounRegistry(t *testing.T) *Registry {
	t.Helper()
	r := New()
	if err := r.RegisterNoun(spec.NounDef{
		Noun:        "widget",
		NounAliases: []string{"widgets"},
		Fields: []spec.FieldDef{
			{ID: "name", Expr: "it.name", MutablePath: "name"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestCallEndpointFull_Priority2_UpdateStrategies(t *testing.T) {
	// GET response fixture used as the base for all get-then-* tests.
	getResp := `{"it":{"name":"old"},"name":"old"}`

	tests := []struct {
		name          string
		ep            *spec.EndpointSpec
		setArgs       map[string]string
		delArgs       []string
		resps         []string // sequenceServer responses: [GET resp, PUT/PATCH resp]
		assertCall    int      // which captured call index to inspect (0=GET,1=PUT/PATCH)
		wantMethod    string
		wantBodyCheck func(t *testing.T, body []byte)
	}{
		{
			name: "get_then_put_issues_get_first",
			ep: &spec.EndpointSpec{
				Path:           "/widgets/w1",
				Method:         "PUT",
				UpdateStrategy: spec.UpdateStrategyGetThenPut,
				UpdateBodyPick: "it",
			},
			resps:      []string{getResp, `{}`},
			assertCall: 0,
			wantMethod: "GET",
		},
		{
			name: "get_then_put_applies_set",
			ep: &spec.EndpointSpec{
				Path:           "/widgets/w1",
				Method:         "PUT",
				UpdateStrategy: spec.UpdateStrategyGetThenPut,
				UpdateBodyPick: "it",
			},
			setArgs:    map[string]string{"name": "new"},
			resps:      []string{getResp, `{}`},
			assertCall: 1,
			wantMethod: "PUT",
			wantBodyCheck: func(t *testing.T, body []byte) {
				m := bodyMap(t, body)
				if m["name"] != "new" {
					t.Fatalf("PUT body name = %v, want new", m["name"])
				}
			},
		},
		{
			name: "get_then_put_with_wrap",
			ep: &spec.EndpointSpec{
				Path:           "/widgets/w1",
				Method:         "PUT",
				UpdateStrategy: spec.UpdateStrategyGetThenPut,
				UpdateBodyPick: "it",
				UpdateBodyWrap: "widget",
			},
			resps:      []string{getResp, `{}`},
			assertCall: 1,
			wantMethod: "PUT",
			wantBodyCheck: func(t *testing.T, body []byte) {
				m := bodyMap(t, body)
				if _, ok := m["widget"]; !ok {
					t.Fatalf("PUT body missing wrap key 'widget': %s", body)
				}
			},
		},
		{
			name: "get_then_patch",
			ep: &spec.EndpointSpec{
				Path:           "/widgets/w1",
				Method:         "PATCH",
				UpdateStrategy: spec.UpdateStrategyGetThenPatch,
				UpdateBodyPick: "it",
			},
			resps:      []string{getResp, `{}`},
			assertCall: 1,
			wantMethod: "PATCH",
			wantBodyCheck: func(t *testing.T, body []byte) {
				// PATCH uses merge-patch+json content type
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, caps := sequenceServer(t, tc.resps)
			ctx := testEndpointCtx(srv.URL, nil)
			ctx.Noun = "widget"
			ctx.Id = "w1"
			ctx.SetArgs = tc.setArgs
			ctx.DelArgs = tc.delArgs
			ctx.Resolver = testNounRegistry(t)

			_, _, err := callEndpointFull(ctx, tc.ep, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(*caps) <= tc.assertCall {
				t.Fatalf("expected at least %d calls, got %d", tc.assertCall+1, len(*caps))
			}
			got := (*caps)[tc.assertCall]
			if got.method != tc.wantMethod {
				t.Fatalf("call[%d] method = %q, want %q", tc.assertCall, got.method, tc.wantMethod)
			}
			if tc.wantBodyCheck != nil {
				tc.wantBodyCheck(t, got.body)
			}
		})
	}
}

// TestCallEndpointFull_Priority2_GetThenPutKV verifies the kv-array strategy:
// GET returns existing pairs, --set upserts, --del removes, PUT sends full array.
func TestCallEndpointFull_Priority2_GetThenPutKV(t *testing.T) {
	kvGetResp := `[{"key":"env","value":"prod"},{"key":"team","value":"ops"}]`

	tests := []struct {
		name        string
		setArgs     map[string]string
		delArgs     []string
		wantPresent map[string]string // key → value expected in PUT body pairs
		wantAbsent  []string          // keys expected absent from PUT body pairs
	}{
		{
			name:        "upsert_existing_key",
			setArgs:     map[string]string{"env": "staging"},
			wantPresent: map[string]string{"env": "staging", "team": "ops"},
		},
		{
			name:        "add_new_key",
			setArgs:     map[string]string{"region": "us-east"},
			wantPresent: map[string]string{"region": "us-east", "env": "prod"},
		},
		{
			name:       "del_key",
			delArgs:    []string{"team"},
			wantAbsent: []string{"team"},
			wantPresent: map[string]string{"env": "prod"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, caps := sequenceServer(t, []string{kvGetResp, `{}`})
			ctx := testEndpointCtx(srv.URL, nil)
			ctx.Noun = "widget"
			ctx.SetArgs = tc.setArgs
			ctx.DelArgs = tc.delArgs
			ctx.Resolver = testNounRegistry(t)

			ep := &spec.EndpointSpec{
				Path:           "/widgets/kv",
				Method:         "PUT",
				UpdateStrategy: spec.UpdateStrategyGetThenPutKV,
				UpdateBodyWrap: "metadata",
			}
			_, _, err := callEndpointFull(ctx, ep, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(*caps) < 2 {
				t.Fatalf("expected 2 calls (GET+PUT), got %d", len(*caps))
			}

			// Decode PUT body: {"metadata": [{key,value},...]}
			putBody := bodyMap(t, (*caps)[1].body)
			pairsRaw, ok := putBody["metadata"].([]any)
			if !ok {
				t.Fatalf("PUT body missing metadata array: %s", (*caps)[1].body)
			}
			kvMap := map[string]string{}
			for _, p := range pairsRaw {
				if pm, ok := p.(map[string]any); ok {
					kvMap[pm["key"].(string)] = pm["value"].(string)
				}
			}
			for k, want := range tc.wantPresent {
				if kvMap[k] != want {
					t.Fatalf("kv[%s] = %q, want %q", k, kvMap[k], want)
				}
			}
			for _, k := range tc.wantAbsent {
				if _, found := kvMap[k]; found {
					t.Fatalf("key %q should be absent after --del, but found in PUT body", k)
				}
			}
		})
	}
}

// TestCallEndpointFull_Priority2_SetFields verifies the set-fields create strategy:
// body is seeded from create_body_init, --set args are applied, result is POSTed.
func TestCallEndpointFull_Priority2_SetFields(t *testing.T) {
	tests := []struct {
		name        string
		setArgs     map[string]string
		ep          *spec.EndpointSpec
		wantBody    map[string]any
	}{
		{
			name:    "create_body_init_seeded",
			setArgs: map[string]string{"name": "my-widget"},
			ep: &spec.EndpointSpec{
				Path:           "/widgets",
				Method:         "POST",
				CreateStrategy: spec.CreateStrategySetFields,
				CreateBodyInit: map[string]string{
					"type": `"KUBERNETES"`,
				},
			},
			wantBody: map[string]any{"type": "KUBERNETES", "name": "my-widget"},
		},
		{
			name:    "create_body_wrap",
			setArgs: map[string]string{"name": "my-widget"},
			ep: &spec.EndpointSpec{
				Path:           "/widgets",
				Method:         "POST",
				CreateStrategy: spec.CreateStrategySetFields,
				CreateBodyWrap: "widget",
				CreateBodyInit: map[string]string{
					"type": `"KUBERNETES"`,
				},
			},
			wantBody: nil, // checked via wrap key below
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, cap := captureServer(t, `{}`)
			ctx := testEndpointCtx(srv.URL, nil)
			ctx.Noun = "widget"
			ctx.SetArgs = tc.setArgs
			ctx.Resolver = testNounRegistry(t)

			_, _, err := callEndpointFull(ctx, tc.ep, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cap.method != "POST" {
				t.Fatalf("method = %q, want POST", cap.method)
			}

			m := bodyMap(t, cap.body)

			// If wrapped, unwrap first.
			if tc.ep.CreateBodyWrap != "" {
				inner, ok := m[tc.ep.CreateBodyWrap].(map[string]any)
				if !ok {
					t.Fatalf("body missing wrap key %q: %s", tc.ep.CreateBodyWrap, cap.body)
				}
				m = inner
			}

			if tc.wantBody != nil {
				for k, want := range tc.wantBody {
					if m[k] != want {
						t.Fatalf("body[%s] = %v, want %v", k, m[k], want)
					}
				}
			}
			// --set name should always be applied
			if m["name"] != tc.setArgs["name"] {
				t.Fatalf("body[name] = %v, want %v", m["name"], tc.setArgs["name"])
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestEvalQueryParams — isolated unit tests for evalQueryParams
// ---------------------------------------------------------------------------

func TestEvalQueryParams(t *testing.T) {
	ctx := testEndpointCtx("http://unused", map[string]any{"q": "find"})
	ctx.Auth.OrgID = "my-org"
	ctx.Auth.ProjectID = "my-proj"

	t.Run("seeds_scope", func(t *testing.T) {
		params := evalQueryParams(ctx, map[string]string{"search": "flags.q"}, true)
		if params["orgIdentifier"] != "my-org" {
			t.Fatalf("orgIdentifier = %q, want my-org", params["orgIdentifier"])
		}
		if params["projectIdentifier"] != "my-proj" {
			t.Fatalf("projectIdentifier = %q, want my-proj", params["projectIdentifier"])
		}
		if params["search"] != "find" {
			t.Fatalf("search = %q, want find", params["search"])
		}
	})

	t.Run("merges_extra", func(t *testing.T) {
		params := evalQueryParams(ctx, nil, true, map[string]string{"page": "2"})
		if params["page"] != "2" {
			t.Fatalf("page = %q, want 2", params["page"])
		}
	})

	t.Run("omits_empty_expr", func(t *testing.T) {
		params := evalQueryParams(ctx, map[string]string{"empty": `flags.missing`}, true)
		if _, ok := params["empty"]; ok {
			t.Fatalf("empty param should be omitted, got %v", params)
		}
	})
}

// ---------------------------------------------------------------------------
// TestBuildYamlEnvelope — isolated unit tests for buildYamlEnvelope
// ---------------------------------------------------------------------------

func TestBuildYamlEnvelope(t *testing.T) {
	ctx := testEndpointCtx("http://unused", nil)

	t.Run("flat_yaml", func(t *testing.T) {
		raw := "identifier: id1\nname: svc1\n"
		env, err := buildYamlEnvelope(ctx, "service", raw)
		if err != nil {
			t.Fatal(err)
		}
		if env["identifier"] != "id1" {
			t.Fatalf("identifier = %v, want id1", env["identifier"])
		}
		if _, ok := env["yaml"].(string); !ok {
			t.Fatalf("yaml field missing or not string: %v", env["yaml"])
		}
	})

	t.Run("passthrough_existing_yaml_field", func(t *testing.T) {
		raw := "identifier: id1\nyaml: \"already wrapped\"\n"
		env, err := buildYamlEnvelope(ctx, "service", raw)
		if err != nil {
			t.Fatal(err)
		}
		if env["yaml"] != "already wrapped" {
			t.Fatalf("yaml = %v, want already wrapped", env["yaml"])
		}
	})
}

// ---------------------------------------------------------------------------
// TestUnwrapIfAlreadyWrapped
// ---------------------------------------------------------------------------

func TestUnwrapIfAlreadyWrapped(t *testing.T) {
	inner := map[string]any{"name": "x"}
	wrapped := map[string]any{"project": inner}
	got := unwrapIfAlreadyWrapped(wrapped, "project")
	if m, ok := got.(map[string]any); !ok || m["name"] != "x" {
		t.Fatalf("got = %v, want inner map", got)
	}
	bare := map[string]any{"name": "y"}
	got = unwrapIfAlreadyWrapped(bare, "project")
	if m, ok := got.(map[string]any); !ok || m["name"] != "y" {
		t.Fatalf("got = %v, want bare map", got)
	}
}

// ---------------------------------------------------------------------------
// TestResolveBody — isolated unit tests for resolveBody
// ---------------------------------------------------------------------------

func TestResolveBody(t *testing.T) {
	t.Run("body_params", func(t *testing.T) {
		ctx := testEndpointCtx("http://unused", nil)
		ep := &spec.EndpointSpec{
			BodyParams: map[string]string{
				"name":       `"hello"`,
				"opts.count": "3",
			},
		}
		body, err := resolveBody(ep, ctx)
		if err != nil {
			t.Fatal(err)
		}
		m, ok := body.(map[string]any)
		if !ok {
			t.Fatalf("body type = %T, want map", body)
		}
		if m["name"] != "hello" {
			t.Fatalf("name = %v, want hello", m["name"])
		}
		opts, ok := m["opts"].(map[string]any)
		if !ok {
			t.Fatalf("opts missing or not map: %v", m["opts"])
		}
		// expr evaluator returns float64 for numeric literals
		switch v := opts["count"].(type) {
		case float64:
			if v != 3 {
				t.Fatalf("opts.count = %v, want 3", v)
			}
		case int:
			if v != 3 {
				t.Fatalf("opts.count = %v, want 3", v)
			}
		default:
			t.Fatalf("opts.count unexpected type %T = %v", opts["count"], opts["count"])
		}
	})

	t.Run("body_fn", func(t *testing.T) {
		r := New()
		const fnID = "test:body"
		r.RegisterBodyFn(fnID, func(*cmdctx.Ctx) (any, error) {
			return map[string]any{"from": "fn"}, nil
		})
		ctx := testEndpointCtx("http://unused", nil)
		ctx.Resolver = r
		ep := &spec.EndpointSpec{BodyFn: fnID}
		body, err := resolveBody(ep, ctx)
		if err != nil {
			t.Fatal(err)
		}
		m := body.(map[string]any)
		if m["from"] != "fn" {
			t.Fatalf("from = %v, want fn", m["from"])
		}
	})

	t.Run("no_resolver", func(t *testing.T) {
		ctx := testEndpointCtx("http://unused", nil)
		ep := &spec.EndpointSpec{BodyFn: "test:missing"}
		_, err := resolveBody(ep, ctx)
		if err == nil || !strings.Contains(err.Error(), "no resolver") {
			t.Fatalf("err = %v, want no resolver", err)
		}
	})
}

// ---------------------------------------------------------------------------
// TestRunEndpointValidators — isolated unit tests for runEndpointValidators
// ---------------------------------------------------------------------------

func TestRunEndpointValidators(t *testing.T) {
	t.Run("none_registered", func(t *testing.T) {
		ctx := testEndpointCtx("http://unused", nil)
		ep := &spec.EndpointSpec{}
		if err := runEndpointValidators(ctx, ep, cmdctx.EndpointRequest{}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("missing_fn", func(t *testing.T) {
		r := New()
		ctx := testEndpointCtx("http://unused", nil)
		ctx.Resolver = r
		ep := &spec.EndpointSpec{ValidatorsEndpoint: []string{"test:missing"}}
		err := runEndpointValidators(ctx, ep, cmdctx.EndpointRequest{})
		if err == nil || !strings.Contains(err.Error(), "not registered") {
			t.Fatalf("err = %v, want not registered", err)
		}
	})

	t.Run("first_error_wins", func(t *testing.T) {
		r := New()
		const id = "test:fail"
		r.RegisterEndpointValidatorFn(id, func(_ *cmdctx.Ctx, _ cmdctx.EndpointRequest) error {
			return fmt.Errorf("boom")
		})
		ctx := testEndpointCtx("http://unused", nil)
		ctx.Resolver = r
		ep := &spec.EndpointSpec{ValidatorsEndpoint: []string{id}}
		err := runEndpointValidators(ctx, ep, cmdctx.EndpointRequest{})
		if err == nil || err.Error() != "boom" {
			t.Fatalf("err = %v, want boom", err)
		}
	})
}
