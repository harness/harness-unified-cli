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
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/harness/cli/pkg/auth"
	"github.com/harness/cli/pkg/cmdctx"
	"github.com/harness/cli/pkg/spec"
)

// ---------------------------------------------------------------------------
// Test helpers
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

func testCtx(apiURL string, flags map[string]any) *cmdctx.Ctx {
	return &cmdctx.Ctx{
		Context:    context.Background(),
		Auth:       testAuth(apiURL),
		FlagValues: flags,
	}
}

// captureServer records the single inbound request and returns a fixed JSON body.
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

// sequenceServer returns a different response per call and records all calls.
func sequenceServer(t *testing.T, resps []string) (*httptest.Server, *[]captured) {
	t.Helper()
	caps := &[]captured{}
	var mu sync.Mutex
	i := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		cap := captured{
			method:      r.Method,
			path:        r.URL.Path,
			rawQuery:    r.URL.RawQuery,
			contentType: r.Header.Get("Content-Type"),
			header:      r.Header.Clone(),
		}
		cap.body, _ = io.ReadAll(r.Body)
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

// tempFile writes content to a temp file with the given extension and returns its path.
func tempFile(t *testing.T, content, ext string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "ep-*"+ext)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	return f.Name()
}

// bodyMap parses a captured JSON body into a map for field assertions.
func bodyMap(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("bodyMap: %v (raw: %s)", err, b)
	}
	return m
}

// qv parses a raw query string into url.Values.
func qv(t *testing.T, raw string) url.Values {
	t.Helper()
	v, err := url.ParseQuery(raw)
	if err != nil {
		t.Fatalf("qv: %v", err)
	}
	return v
}

// testNounRegistry returns a *Registry with a "widget" noun that has a mutable "name" field.
func testNounRegistry(t *testing.T) *Registry {
	t.Helper()
	r := New()
	if err := r.RegisterNoun(spec.NounDef{
		Noun:        "widget",
		NounAliases: []string{"widgets"},
		Fields:      []spec.FieldDef{{ID: "name", Expr: "it.name", MutablePath: "name"}},
	}); err != nil {
		t.Fatal(err)
	}
	return r
}

// ---------------------------------------------------------------------------
// TestCallEndpointFull_ErrorPaths — errors that abort before any HTTP call
// ---------------------------------------------------------------------------

func TestCallEndpointFull_ErrorPaths(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(*cmdctx.Ctx) // optional ctx mutation
		flags    map[string]any
		ep       *spec.EndpointSpec
		wantErr  string
	}{
		{
			name:    "nil_auth",
			setup:   func(ctx *cmdctx.Ctx) { ctx.Auth = nil },
			ep:      &spec.EndpointSpec{Path: "/x"},
			wantErr: "requires auth",
		},
		{
			name:    "file_required_no_flag",
			ep:      &spec.EndpointSpec{Path: "/x", FileBody: spec.FileBodyRequired},
			wantErr: "-f/--file is required",
		},
		{
			name:    "unclosed_path_template",
			ep:      &spec.EndpointSpec{Path: "/x/{{unclosed"},
			wantErr: "resolving path",
		},
		{
			name:    "slurp_file_error",
			flags:   map[string]any{"file": "/nonexistent/path/file.json"},
			ep:      &spec.EndpointSpec{Path: "/x", Method: "POST", FileBody: spec.FileBodyOptional},
			wantErr: "",
		},
		{
			name:    "normalize_file_body_error",
			flags:   map[string]any{"file": ""},
			ep:      &spec.EndpointSpec{Path: "/x", Method: "POST", FileBody: spec.FileBodyOptional, ContentType: "application/yaml"},
			wantErr: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			flags := tc.flags
			if flags == nil {
				flags = map[string]any{}
			}
			// slurp_file_error: file flag points at non-existent path — set inline
			if tc.name == "slurp_file_error" {
				// already set above
			}
			// normalize_file_body_error: write a JSON file but claim content-type yaml
			if tc.name == "normalize_file_body_error" {
				flags["file"] = tempFile(t, `{"key":"val"}`, ".json")
			}

			ctx := testCtx("http://unused", flags)
			if tc.setup != nil {
				tc.setup(ctx)
			}
			result, headers, err := callEndpointFull(ctx, tc.ep, nil)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if result != nil || headers != nil {
				t.Fatalf("expected nil result+headers on error, got result=%v headers=%v", result, headers)
			}
			if tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %q, want substring %q", err, tc.wantErr)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestCallEndpointFull_ValidatorAbort — validator fires and blocks the request
// ---------------------------------------------------------------------------

func TestCallEndpointFull_ValidatorAbort(t *testing.T) {
	tests := []struct {
		name    string
		ep      *spec.EndpointSpec
		flags   map[string]any
	}{
		{
			// validator in the standard file-body path (Priority 1, non-envelope)
			name: "file_body_path",
			ep: &spec.EndpointSpec{
				Path:               "/x",
				Method:             "POST",
				FileBody:           spec.FileBodyOptional,
				ValidatorsEndpoint: []string{"test:abort"},
			},
			flags: map[string]any{"file": ""},
		},
		{
			// validator in the yaml-envelope path (Priority 1, envelope branch)
			name: "yaml_envelope_path",
			ep: &spec.EndpointSpec{
				Path:                "/x",
				Method:              "POST",
				FileBody:            spec.FileBodyOptional,
				FileBodyYamlEnvelope: "service",
				ValidatorsEndpoint:  []string{"test:abort"},
			},
			flags: map[string]any{"file": ""},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, cap := captureServer(t, `{}`)
			r := New()
			r.RegisterEndpointValidatorFn("test:abort", func(_ *cmdctx.Ctx, _ cmdctx.EndpointRequest) error {
				return errors.New("validator rejected")
			})
			tc.flags["file"] = tempFile(t, "identifier: x\n", ".yaml")
			ctx := testCtx(srv.URL, tc.flags)
			ctx.Resolver = r

			_, _, err := callEndpointFull(ctx, tc.ep, nil)
			if err == nil || !strings.Contains(err.Error(), "validator rejected") {
				t.Fatalf("err = %v, want validator rejected", err)
			}
			if cap.method != "" {
				t.Fatal("server received request after validator abort")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestCallEndpointFull_AuthScopeStripping — ctx.Level trims auth scope
// ---------------------------------------------------------------------------

func TestCallEndpointFull_AuthScopeStripping(t *testing.T) {
	tests := []struct {
		level       string
		wantOrg     bool
		wantProject bool
	}{
		{"", true, true},
		{"org", true, false},
		{"account", false, false},
	}

	for _, tc := range tests {
		t.Run("level_"+tc.level, func(t *testing.T) {
			srv, cap := captureServer(t, `{}`)
			ctx := testCtx(srv.URL, nil)
			ctx.Level = tc.level
			origAuth := ctx.Auth

			_, _, err := callEndpointFull(ctx, &spec.EndpointSpec{Path: "/x"}, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			got := qv(t, cap.rawQuery)
			if tc.wantOrg {
				if got.Get("orgIdentifier") != "org" {
					t.Fatalf("orgIdentifier = %q, want org", got.Get("orgIdentifier"))
				}
			} else if got.Get("orgIdentifier") != "" {
				t.Fatalf("orgIdentifier = %q, want empty", got.Get("orgIdentifier"))
			}

			if tc.wantProject {
				if got.Get("projectIdentifier") != "proj" {
					t.Fatalf("projectIdentifier = %q, want proj", got.Get("projectIdentifier"))
				}
			} else if got.Get("projectIdentifier") != "" {
				t.Fatalf("projectIdentifier = %q, want empty", got.Get("projectIdentifier"))
			}

			if ctx.Auth != origAuth {
				t.Fatal("ctx.Auth not restored after call (defer broken)")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestCallEndpointFull_Priority1_FileBody — file body dispatch branches
// ---------------------------------------------------------------------------

func TestCallEndpointFull_Priority1_FileBody(t *testing.T) {
	tests := []struct {
		name          string
		fileContent   string
		fileExt       string // defaults to ".json"
		ep            *spec.EndpointSpec
		wantMethod    string
		wantBodyKey   string // assert key present in body JSON
		wantBodyVal   any    // assert exact value if non-nil
		wantCT        string // assert content-type contains
		wantWrapKey   string // assert outer wrap key present
		wantWrapInner string // assert inner map has this key
		wantNoWrap    bool   // assert body does NOT have a top-level wrapper
	}{
		// --- JSON POST: no wrapping, body passes through as-is ---
		{
			name:        "json_post_no_wrap",
			fileContent: `{"id":"a"}`,
			ep:          &spec.EndpointSpec{Path: "/x", Method: "POST", FileBody: spec.FileBodyOptional},
			wantMethod:  "POST",
			wantBodyKey: "id",
			wantBodyVal: "a",
			wantCT:      "application/json",
		},
		// --- PostRaw fallback: POST, optional, no wrap fields at all ---
		{
			name:        "post_raw_fallback",
			fileContent: `{"x":1}`,
			ep:          &spec.EndpointSpec{Path: "/x", Method: "POST", FileBody: spec.FileBodyOptional},
			wantMethod:  "POST",
			wantBodyKey: "x",
			wantNoWrap:  false,
		},
		// --- FileBodyWrapAsString: POST ---
		{
			name:        "file_body_wrap_as_string_post",
			fileContent: "name: foo\ntype: step\n",
			fileExt:     ".yaml",
			ep: &spec.EndpointSpec{
				Path: "/x", Method: "POST", FileBody: spec.FileBodyOptional,
				FileBodyWrapAsString: "template_yaml", FileBodyContentType: "application/yaml",
			},
			wantMethod:  "POST",
			wantWrapKey: "template_yaml",
		},
		// --- FileBodyWrapAsString: PUT ---
		{
			name:        "file_body_wrap_as_string_put",
			fileContent: "name: foo\ntype: step\n",
			fileExt:     ".yaml",
			ep: &spec.EndpointSpec{
				Path: "/x", Method: "PUT", FileBody: spec.FileBodyOptional,
				FileBodyWrapAsString: "template_yaml", FileBodyContentType: "application/yaml",
			},
			wantMethod:  "PUT",
			wantWrapKey: "template_yaml",
		},
		// --- FileBodyWrapAsString: PATCH ---
		{
			name:        "file_body_wrap_as_string_patch",
			fileContent: "name: foo\ntype: step\n",
			fileExt:     ".yaml",
			ep: &spec.EndpointSpec{
				Path: "/x", Method: "PATCH", FileBody: spec.FileBodyOptional,
				FileBodyWrapAsString: "template_yaml", FileBodyContentType: "application/yaml",
			},
			wantMethod:  "PATCH",
			wantWrapKey: "template_yaml",
		},
		// --- UpdateBodyWrap: PUT, file not yet wrapped ---
		{
			name:          "update_body_wrap_put_not_wrapped",
			fileContent:   `{"name":"svc"}`,
			ep:            &spec.EndpointSpec{Path: "/x", Method: "PUT", FileBody: spec.FileBodyOptional, UpdateBodyWrap: "service"},
			wantMethod:    "PUT",
			wantWrapKey:   "service",
			wantWrapInner: "name",
		},
		// --- UpdateBodyWrap: PUT, file already wrapped (unwrap guard) ---
		{
			name:          "update_body_wrap_put_already_wrapped",
			fileContent:   `{"service":{"name":"svc"}}`,
			ep:            &spec.EndpointSpec{Path: "/x", Method: "PUT", FileBody: spec.FileBodyOptional, UpdateBodyWrap: "service"},
			wantMethod:    "PUT",
			wantWrapKey:   "service",
			wantWrapInner: "name",
		},
		// --- UpdateBodyWrap: PATCH ---
		{
			name:        "update_body_wrap_patch",
			fileContent: `{"name":"svc"}`,
			ep:          &spec.EndpointSpec{Path: "/x", Method: "PATCH", FileBody: spec.FileBodyOptional, UpdateBodyWrap: "service"},
			wantMethod:  "PATCH",
			wantWrapKey: "service",
		},
		// --- PUT bare: no FileBodyWrapAsString, no UpdateBodyWrap ---
		{
			name:        "put_bare_no_wrap",
			fileContent: `{"k":"v"}`,
			ep:          &spec.EndpointSpec{Path: "/x", Method: "PUT", FileBody: spec.FileBodyOptional},
			wantMethod:  "PUT",
			wantBodyKey: "k",
		},
		// --- CreateBodyWrap + CreateBodyInit: file not yet wrapped ---
		{
			name:        "create_body_wrap_and_init",
			fileContent: `{"name":"svc"}`,
			ep: &spec.EndpointSpec{
				Path: "/x", Method: "POST", FileBody: spec.FileBodyOptional,
				CreateBodyWrap: "service", CreateBodyInit: map[string]string{"type": `"K8s"`},
			},
			wantMethod:    "POST",
			wantWrapKey:   "service",
			wantWrapInner: "name",
		},
		// --- CreateBodyWrap: file already has wrapper key ---
		{
			name:          "create_body_wrap_file_already_wrapped",
			fileContent:   `{"service":{"name":"inner"}}`,
			ep:            &spec.EndpointSpec{Path: "/x", Method: "POST", FileBody: spec.FileBodyOptional, CreateBodyWrap: "service"},
			wantMethod:    "POST",
			wantWrapKey:   "service",
			wantWrapInner: "name",
		},
		// --- CreateBodyInit only, no CreateBodyWrap ---
		{
			name:        "create_body_init_no_wrap",
			fileContent: `{"name":"svc"}`,
			ep: &spec.EndpointSpec{
				Path: "/x", Method: "POST", FileBody: spec.FileBodyOptional,
				CreateBodyInit: map[string]string{"type": `"K8s"`},
			},
			wantMethod:  "POST",
			wantBodyKey: "name",
			wantNoWrap:  true,
		},
		// --- CreateBodyInit does not overwrite existing field ---
		{
			name:        "create_body_init_no_overwrite",
			fileContent: `{"name":"svc","type":"Custom"}`,
			ep: &spec.EndpointSpec{
				Path: "/x", Method: "POST", FileBody: spec.FileBodyOptional,
				CreateBodyWrap: "service", CreateBodyInit: map[string]string{"type": `"K8s"`},
			},
			wantMethod:    "POST",
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
			ctx := testCtx(srv.URL, map[string]any{"file": tempFile(t, tc.fileContent, ext)})

			_, _, err := callEndpointFull(ctx, tc.ep, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cap.method != tc.wantMethod {
				t.Fatalf("method = %q, want %q", cap.method, tc.wantMethod)
			}
			if tc.wantCT != "" && !strings.Contains(cap.contentType, tc.wantCT) {
				t.Fatalf("content-type = %q, want %q", cap.contentType, tc.wantCT)
			}
			m := bodyMap(t, cap.body)
			if tc.wantBodyKey != "" {
				if _, ok := m[tc.wantBodyKey]; !ok {
					t.Fatalf("body missing key %q: %s", tc.wantBodyKey, cap.body)
				}
				if tc.wantBodyVal != nil && m[tc.wantBodyKey] != tc.wantBodyVal {
					t.Fatalf("body[%s] = %v, want %v", tc.wantBodyKey, m[tc.wantBodyKey], tc.wantBodyVal)
				}
			}
			if tc.wantWrapKey != "" {
				inner, ok := m[tc.wantWrapKey]
				if !ok {
					t.Fatalf("body missing wrap key %q: %s", tc.wantWrapKey, cap.body)
				}
				if tc.wantWrapInner != "" {
					innerMap, ok := inner.(map[string]any)
					if !ok {
						t.Fatalf("body[%s] is not a map: %T", tc.wantWrapKey, inner)
					}
					if _, ok := innerMap[tc.wantWrapInner]; !ok {
						t.Fatalf("inner map missing key %q: %v", tc.wantWrapInner, innerMap)
					}
				}
			}
			// For create_body_init_no_wrap: verify the type field was injected but no outer wrapper
			if tc.name == "create_body_init_no_wrap" {
				if m["type"] == nil {
					t.Fatalf("CreateBodyInit key 'type' missing from body: %s", cap.body)
				}
				// body should be a flat map, not wrapped
				if _, hasService := m["service"]; hasService {
					t.Fatalf("body should not have wrapper key 'service': %s", cap.body)
				}
			}
			// For create_body_init_no_overwrite: verify init did not stomp existing value
			if tc.name == "create_body_init_no_overwrite" {
				inner, _ := m[tc.wantWrapKey].(map[string]any)
				if inner["type"] != "Custom" {
					t.Fatalf("CreateBodyInit overwrote existing type: got %v, want Custom", inner["type"])
				}
			}
		})
	}
}

// TestCallEndpointFull_Priority1_YamlEnvelope — yaml_envelope lifts identity fields.
func TestCallEndpointFull_Priority1_YamlEnvelope(t *testing.T) {
	srv, cap := captureServer(t, `{}`)
	ctx := testCtx(srv.URL, map[string]any{
		"file": tempFile(t, "identifier: svc1\nname: My Service\n", ".yaml"),
	})
	ep := &spec.EndpointSpec{
		Path: "/services", Method: "POST", FileBody: spec.FileBodyOptional,
		FileBodyYamlEnvelope: "service",
	}

	_, _, err := callEndpointFull(ctx, ep, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m := bodyMap(t, cap.body)
	if m["identifier"] != "svc1" {
		t.Fatalf("identifier = %v, want svc1", m["identifier"])
	}
	if _, ok := m["yaml"].(string); !ok {
		t.Fatalf("envelope missing yaml string field: %v", m)
	}
}

// ---------------------------------------------------------------------------
// TestCallEndpointFull_Priority2_UpdateStrategies — get-then-put/patch/kv/set-fields
// ---------------------------------------------------------------------------

func TestCallEndpointFull_Priority2_UpdateStrategies(t *testing.T) {
	getResp := `{"it":{"name":"old"},"name":"old"}`

	tests := []struct {
		name        string
		ep          *spec.EndpointSpec
		setArgs     map[string]string
		delArgs     []string
		resps       []string
		checkCall   int // which call index to inspect
		wantMethod  string
		checkBody   func(t *testing.T, body []byte)
	}{
		{
			name: "get_then_put_issues_get_first",
			ep:   &spec.EndpointSpec{Path: "/widgets/w1", Method: "PUT", UpdateStrategy: spec.UpdateStrategyGetThenPut, UpdateBodyPick: "it"},
			resps: []string{getResp, `{}`}, checkCall: 0, wantMethod: "GET",
		},
		{
			name:    "get_then_put_applies_set",
			ep:      &spec.EndpointSpec{Path: "/widgets/w1", Method: "PUT", UpdateStrategy: spec.UpdateStrategyGetThenPut, UpdateBodyPick: "it"},
			setArgs: map[string]string{"name": "new"},
			resps:   []string{getResp, `{}`}, checkCall: 1, wantMethod: "PUT",
			checkBody: func(t *testing.T, body []byte) {
				if bodyMap(t, body)["name"] != "new" {
					t.Fatalf("PUT body name = %v, want new", bodyMap(t, body)["name"])
				}
			},
		},
		{
			name: "get_then_put_with_wrap",
			ep:   &spec.EndpointSpec{Path: "/widgets/w1", Method: "PUT", UpdateStrategy: spec.UpdateStrategyGetThenPut, UpdateBodyPick: "it", UpdateBodyWrap: "widget"},
			resps: []string{getResp, `{}`}, checkCall: 1, wantMethod: "PUT",
			checkBody: func(t *testing.T, body []byte) {
				if _, ok := bodyMap(t, body)["widget"]; !ok {
					t.Fatalf("PUT body missing wrap key 'widget': %s", body)
				}
			},
		},
		{
			name:  "get_then_patch",
			ep:    &spec.EndpointSpec{Path: "/widgets/w1", Method: "PATCH", UpdateStrategy: spec.UpdateStrategyGetThenPatch, UpdateBodyPick: "it"},
			resps: []string{getResp, `{}`}, checkCall: 1, wantMethod: "PATCH",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, caps := sequenceServer(t, tc.resps)
			ctx := testCtx(srv.URL, nil)
			ctx.Noun = "widget"
			ctx.Id = "w1"
			ctx.SetArgs = tc.setArgs
			ctx.DelArgs = tc.delArgs
			ctx.Resolver = testNounRegistry(t)

			_, _, err := callEndpointFull(ctx, tc.ep, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(*caps) <= tc.checkCall {
				t.Fatalf("expected ≥%d calls, got %d", tc.checkCall+1, len(*caps))
			}
			got := (*caps)[tc.checkCall]
			if got.method != tc.wantMethod {
				t.Fatalf("call[%d] method = %q, want %q", tc.checkCall, got.method, tc.wantMethod)
			}
			if tc.checkBody != nil {
				tc.checkBody(t, got.body)
			}
		})
	}
}

func TestCallEndpointFull_Priority2_GetThenPutKV(t *testing.T) {
	getResp := `[{"key":"env","value":"prod"},{"key":"team","value":"ops"}]`

	tests := []struct {
		name        string
		setArgs     map[string]string
		delArgs     []string
		wantPresent map[string]string
		wantAbsent  []string
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
			name:        "del_key",
			delArgs:     []string{"team"},
			wantPresent: map[string]string{"env": "prod"},
			wantAbsent:  []string{"team"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, caps := sequenceServer(t, []string{getResp, `{}`})
			ctx := testCtx(srv.URL, nil)
			ctx.SetArgs = tc.setArgs
			ctx.DelArgs = tc.delArgs
			ctx.Resolver = testNounRegistry(t)

			ep := &spec.EndpointSpec{
				Path: "/kv", Method: "PUT",
				UpdateStrategy: spec.UpdateStrategyGetThenPutKV, UpdateBodyWrap: "metadata",
			}
			_, _, err := callEndpointFull(ctx, ep, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(*caps) < 2 {
				t.Fatalf("expected 2 calls (GET+PUT), got %d", len(*caps))
			}

			putBody := bodyMap(t, (*caps)[1].body)
			pairsRaw, ok := putBody["metadata"].([]any)
			if !ok {
				t.Fatalf("PUT body missing metadata array: %s", (*caps)[1].body)
			}
			kvMap := map[string]string{}
			for _, p := range pairsRaw {
				if pm, ok := p.(map[string]any); ok {
					kvMap[fmt.Sprint(pm["key"])] = fmt.Sprint(pm["value"])
				}
			}
			for k, want := range tc.wantPresent {
				if kvMap[k] != want {
					t.Fatalf("kv[%s] = %q, want %q", k, kvMap[k], want)
				}
			}
			for _, k := range tc.wantAbsent {
				if _, found := kvMap[k]; found {
					t.Fatalf("key %q should be absent after --del, found in PUT body", k)
				}
			}
		})
	}
}

func TestCallEndpointFull_Priority2_SetFields(t *testing.T) {
	tests := []struct {
		name     string
		setArgs  map[string]string
		ep       *spec.EndpointSpec
		checkBody func(t *testing.T, m map[string]any)
	}{
		{
			name:    "init_seeded_and_set_applied",
			setArgs: map[string]string{"name": "my-widget"},
			ep: &spec.EndpointSpec{
				Path: "/widgets", Method: "POST", CreateStrategy: spec.CreateStrategySetFields,
				CreateBodyInit: map[string]string{"type": `"KUBERNETES"`},
			},
			checkBody: func(t *testing.T, m map[string]any) {
				if m["type"] != "KUBERNETES" {
					t.Fatalf("type = %v, want KUBERNETES", m["type"])
				}
				if m["name"] != "my-widget" {
					t.Fatalf("name = %v, want my-widget", m["name"])
				}
			},
		},
		{
			name:    "with_create_body_wrap",
			setArgs: map[string]string{"name": "my-widget"},
			ep: &spec.EndpointSpec{
				Path: "/widgets", Method: "POST", CreateStrategy: spec.CreateStrategySetFields,
				CreateBodyWrap: "widget", CreateBodyInit: map[string]string{"type": `"KUBERNETES"`},
			},
			checkBody: func(t *testing.T, m map[string]any) {
				inner, ok := m["widget"].(map[string]any)
				if !ok {
					t.Fatalf("body missing wrap key 'widget': %v", m)
				}
				if inner["name"] != "my-widget" {
					t.Fatalf("inner name = %v, want my-widget", inner["name"])
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, cap := captureServer(t, `{}`)
			ctx := testCtx(srv.URL, nil)
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
			tc.checkBody(t, bodyMap(t, cap.body))
		})
	}
}

// ---------------------------------------------------------------------------
// TestCallEndpointFull_Priority3_DefaultDispatch — body_params, body_fn, headers
// ---------------------------------------------------------------------------

func TestCallEndpointFull_Priority3_DefaultDispatch(t *testing.T) {
	tests := []struct {
		name        string
		ep          *spec.EndpointSpec
		extraQP     map[string]string
		setupCtx    func(*cmdctx.Ctx, *Registry)
		wantMethod  string
		wantBodyKey string
		wantBodyNil bool
		wantCT      string
		wantQPKey   string
		wantHeader  map[string]string
	}{
		{
			name:        "GET_default",
			ep:          &spec.EndpointSpec{Path: "/items"},
			wantMethod:  "GET",
			wantBodyNil: true,
		},
		{
			name: "POST_body_params",
			ep:   &spec.EndpointSpec{Path: "/items", Method: "POST", BodyParams: map[string]string{"name": `"hello"`}},
			wantMethod: "POST", wantBodyKey: "name", wantCT: "application/json",
		},
		{
			name: "PUT_body_params",
			ep:   &spec.EndpointSpec{Path: "/items/1", Method: "PUT", BodyParams: map[string]string{"val": `"v"`}},
			wantMethod: "PUT", wantBodyKey: "val",
		},
		{
			name:        "DELETE_no_body",
			ep:          &spec.EndpointSpec{Path: "/items/1", Method: "DELETE"},
			wantMethod:  "DELETE",
			wantBodyNil: true,
		},
		{
			name:    "DELETE_with_body",
			ep:      &spec.EndpointSpec{Path: "/items/1", Method: "DELETE", BodyParams: map[string]string{"reason": `"gone"`}},
			wantMethod: "DELETE", wantBodyKey: "reason",
		},
		{
			// PATCH with no strategy falls through to c.Post in the default switch arm (line 253).
			// The wire method becomes POST — this documents the current behaviour.
			name:       "PATCH_non_strategy_fallthrough",
			ep:         &spec.EndpointSpec{Path: "/items/1", Method: "PATCH", BodyParams: map[string]string{"k": `"v"`}},
			wantMethod: "POST", wantBodyKey: "k",
		},
		{
			name:    "extra_query_params_forwarded",
			ep:      &spec.EndpointSpec{Path: "/items"},
			extraQP: map[string]string{"page": "3"},
			wantMethod: "GET", wantQPKey: "page",
		},
		{
			name: "empty_expr_param_omitted",
			ep:   &spec.EndpointSpec{Path: "/items", QueryParams: map[string]string{"missing": "flags.nonexistent"}},
			wantMethod: "GET",
		},
		// extraHeaders branches
		{
			name: "GET_with_extra_headers",
			ep:   &spec.EndpointSpec{Path: "/items", RequestHeaders: map[string]string{"X-Acct": "auth.account"}},
			wantMethod: "GET", wantHeader: map[string]string{"X-Acct": "acct"},
		},
		{
			name: "POST_with_extra_headers",
			ep:   &spec.EndpointSpec{Path: "/items", Method: "POST", RequestHeaders: map[string]string{"X-Acct": "auth.account"}},
			wantMethod: "POST", wantCT: "application/json", wantHeader: map[string]string{"X-Acct": "acct"},
		},
		{
			name: "PUT_nil_body_with_extra_headers",
			ep:   &spec.EndpointSpec{Path: "/items/1", Method: "PUT", RequestHeaders: map[string]string{"X-Acct": "auth.account"}},
			wantMethod: "PUT", wantCT: "application/json", wantHeader: map[string]string{"X-Acct": "acct"},
		},
		{
			name: "DELETE_with_extra_headers",
			ep:   &spec.EndpointSpec{Path: "/items/1", Method: "DELETE", RequestHeaders: map[string]string{"X-Acct": "auth.account"}},
			wantMethod: "DELETE", wantHeader: map[string]string{"X-Acct": "acct"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, cap := captureServer(t, `{}`)
			r := New()
			ctx := testCtx(srv.URL, nil)
			ctx.Resolver = r
			if tc.setupCtx != nil {
				tc.setupCtx(ctx, r)
			}

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
			if tc.wantBodyNil && len(cap.body) > 0 && string(cap.body) != "null" && string(cap.body) != "{}" {
				t.Fatalf("expected no/empty body, got %s", cap.body)
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
				if qv(t, cap.rawQuery).Get(tc.wantQPKey) == "" {
					t.Fatalf("query param %q missing from %q", tc.wantQPKey, cap.rawQuery)
				}
			}
			for k, want := range tc.wantHeader {
				if got := cap.header.Get(k); got != want {
					t.Fatalf("header %s = %q, want %q", k, got, want)
				}
			}
			// empty expr param must be absent
			if tc.name == "empty_expr_param_omitted" {
				if qv(t, cap.rawQuery).Get("missing") != "" {
					t.Fatalf("empty expr param should be omitted, got %q", cap.rawQuery)
				}
			}
		})
	}
}

// TestCallEndpointFull_Priority3_BodyFn — body_fn supplies the POST body.
func TestCallEndpointFull_Priority3_BodyFn(t *testing.T) {
	srv, cap := captureServer(t, `{}`)
	r := New()
	r.RegisterBodyFn("test:body", func(*cmdctx.Ctx) (any, error) {
		return map[string]any{"from": "fn"}, nil
	})
	ctx := testCtx(srv.URL, nil)
	ctx.Resolver = r

	_, _, err := callEndpointFull(ctx, &spec.EndpointSpec{Path: "/x", Method: "POST", BodyFn: "test:body"}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if bodyMap(t, cap.body)["from"] != "fn" {
		t.Fatalf("body[from] = %v, want fn", bodyMap(t, cap.body)["from"])
	}
}

// TestCallEndpointFull_Priority3_RawBody — body_fn returning *cmdctx.RawBody.
func TestCallEndpointFull_Priority3_RawBody(t *testing.T) {
	tests := []struct {
		name           string
		requestHeaders map[string]string
		wantCT         string
	}{
		{
			name:   "raw_body_no_headers",
			wantCT: "text/plain",
		},
		{
			name:           "raw_body_with_extra_headers",
			requestHeaders: map[string]string{"X-Acct": "auth.account"},
			wantCT:         "text/plain",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, cap := captureServer(t, `{}`)
			r := New()
			r.RegisterBodyFn("test:raw", func(*cmdctx.Ctx) (any, error) {
				return &cmdctx.RawBody{Content: "raw-data", ContentType: "text/plain"}, nil
			})
			ctx := testCtx(srv.URL, nil)
			ctx.Resolver = r

			ep := &spec.EndpointSpec{Path: "/x", Method: "POST", BodyFn: "test:raw", RequestHeaders: tc.requestHeaders}
			_, _, err := callEndpointFull(ctx, ep, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !strings.Contains(cap.contentType, tc.wantCT) {
				t.Fatalf("content-type = %q, want %q", cap.contentType, tc.wantCT)
			}
			if string(cap.body) != "raw-data" {
				t.Fatalf("body = %q, want raw-data", cap.body)
			}
			if tc.requestHeaders != nil {
				if cap.header.Get("X-Acct") != "acct" {
					t.Fatalf("X-Acct = %q, want acct", cap.header.Get("X-Acct"))
				}
			}
		})
	}
}

// TestCallEndpointFull_Priority3_QueryParamsFnError — query_params_fn returning error aborts.
func TestCallEndpointFull_Priority3_QueryParamsFnError(t *testing.T) {
	srv, cap := captureServer(t, `{}`)
	r := New()
	r.RegisterQueryParamsFn("test:qp-fail", func(*cmdctx.Ctx) (map[string]string, error) {
		return nil, errors.New("qp fn failed")
	})
	ctx := testCtx(srv.URL, nil)
	ctx.Resolver = r

	ep := &spec.EndpointSpec{Path: "/x", QueryParamsFn: "test:qp-fail"}
	_, _, err := callEndpointFull(ctx, ep, nil)
	if err == nil || !strings.Contains(err.Error(), "qp fn failed") {
		t.Fatalf("err = %v, want qp fn failed", err)
	}
	if cap.method != "" {
		t.Fatal("server received request after query_params_fn error")
	}
}

// ---------------------------------------------------------------------------
// TestEvalQueryParams — isolated tests for evalQueryParams
// ---------------------------------------------------------------------------

func TestEvalQueryParams(t *testing.T) {
	ctx := testCtx("http://unused", map[string]any{"q": "find"})
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
		params := evalQueryParams(ctx, map[string]string{"empty": "flags.missing"}, true)
		if _, ok := params["empty"]; ok {
			t.Fatalf("empty param should be omitted, got %v", params)
		}
	})
}

// ---------------------------------------------------------------------------
// TestBuildYamlEnvelope — isolated tests for buildYamlEnvelope
// ---------------------------------------------------------------------------

func TestBuildYamlEnvelope(t *testing.T) {
	ctx := testCtx("http://unused", nil)

	t.Run("flat_yaml_lifts_identity_fields", func(t *testing.T) {
		env, err := buildYamlEnvelope(ctx, "service", "identifier: id1\nname: svc1\n")
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
		env, err := buildYamlEnvelope(ctx, "service", "identifier: id1\nyaml: \"already wrapped\"\n")
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
	t.Run("unwraps_when_key_present", func(t *testing.T) {
		inner := map[string]any{"name": "x"}
		got := unwrapIfAlreadyWrapped(map[string]any{"project": inner}, "project")
		if m, ok := got.(map[string]any); !ok || m["name"] != "x" {
			t.Fatalf("got = %v, want inner map", got)
		}
	})

	t.Run("passthrough_when_key_absent", func(t *testing.T) {
		bare := map[string]any{"name": "y"}
		got := unwrapIfAlreadyWrapped(bare, "project")
		if m, ok := got.(map[string]any); !ok || m["name"] != "y" {
			t.Fatalf("got = %v, want bare map", got)
		}
	})
}

// ---------------------------------------------------------------------------
// TestResolveBody — isolated tests for resolveBody
// ---------------------------------------------------------------------------

func TestResolveBody(t *testing.T) {
	t.Run("body_params_nested", func(t *testing.T) {
		ctx := testCtx("http://unused", nil)
		ep := &spec.EndpointSpec{BodyParams: map[string]string{"name": `"hello"`, "opts.count": "3"}}
		body, err := resolveBody(ep, ctx)
		if err != nil {
			t.Fatal(err)
		}
		m := body.(map[string]any)
		if m["name"] != "hello" {
			t.Fatalf("name = %v, want hello", m["name"])
		}
		opts := m["opts"].(map[string]any)
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

	t.Run("body_fn_called", func(t *testing.T) {
		r := New()
		r.RegisterBodyFn("test:body", func(*cmdctx.Ctx) (any, error) {
			return map[string]any{"from": "fn"}, nil
		})
		ctx := testCtx("http://unused", nil)
		ctx.Resolver = r
		body, err := resolveBody(&spec.EndpointSpec{BodyFn: "test:body"}, ctx)
		if err != nil {
			t.Fatal(err)
		}
		if body.(map[string]any)["from"] != "fn" {
			t.Fatalf("from = %v, want fn", body.(map[string]any)["from"])
		}
	})

	t.Run("no_resolver_errors", func(t *testing.T) {
		ctx := testCtx("http://unused", nil)
		_, err := resolveBody(&spec.EndpointSpec{BodyFn: "test:missing"}, ctx)
		if err == nil || !strings.Contains(err.Error(), "no resolver") {
			t.Fatalf("err = %v, want no resolver", err)
		}
	})
}

// ---------------------------------------------------------------------------
// TestRunEndpointValidators — isolated tests for runEndpointValidators
// ---------------------------------------------------------------------------

func TestRunEndpointValidators(t *testing.T) {
	t.Run("none_registered_ok", func(t *testing.T) {
		ctx := testCtx("http://unused", nil)
		if err := runEndpointValidators(ctx, &spec.EndpointSpec{}, cmdctx.EndpointRequest{}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("missing_fn_errors", func(t *testing.T) {
		r := New()
		ctx := testCtx("http://unused", nil)
		ctx.Resolver = r
		err := runEndpointValidators(ctx, &spec.EndpointSpec{ValidatorsEndpoint: []string{"test:missing"}}, cmdctx.EndpointRequest{})
		if err == nil || !strings.Contains(err.Error(), "not registered") {
			t.Fatalf("err = %v, want not registered", err)
		}
	})

	t.Run("first_error_wins", func(t *testing.T) {
		r := New()
		r.RegisterEndpointValidatorFn("test:fail", func(_ *cmdctx.Ctx, _ cmdctx.EndpointRequest) error {
			return fmt.Errorf("boom")
		})
		ctx := testCtx("http://unused", nil)
		ctx.Resolver = r
		err := runEndpointValidators(ctx, &spec.EndpointSpec{ValidatorsEndpoint: []string{"test:fail"}}, cmdctx.EndpointRequest{})
		if err == nil || err.Error() != "boom" {
			t.Fatalf("err = %v, want boom", err)
		}
	})
}

// ---------------------------------------------------------------------------
// TestParseArrayFlag
// ---------------------------------------------------------------------------

func TestParseArrayFlag(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{`["a","b","c"]`, []string{"a", "b", "c"}},
		{`[invalid`, []string{"[invalid"}},     // invalid JSON falls back to comma split
		{"a, b, c", []string{"a", "b", "c"}},
		{"foo", []string{"foo"}},
		{" a , b ", []string{"a", "b"}},
		{"", []string{}},
	}
	for _, tc := range tests {
		got := parseArrayFlag(tc.in)
		if len(got) != len(tc.want) {
			t.Fatalf("parseArrayFlag(%q) = %v, want %v", tc.in, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("parseArrayFlag(%q)[%d] = %q, want %q", tc.in, i, got[i], tc.want[i])
			}
		}
	}
}

// ---------------------------------------------------------------------------
// TestApplyMutations — covers all field types and --set/--del error paths
// ---------------------------------------------------------------------------

func TestApplyMutations(t *testing.T) {
	// fieldPaths for a noun with scalar, tags, and set-type fields.
	fields := map[string]spec.FieldDef{
		"name":    {ID: "name", Expr: "it.name", MutablePath: "name"},
		"labels":  {ID: "labels", Expr: "it.labels", MutablePath: "labels", FieldType: "tags"},
		"modules": {ID: "modules", Expr: "it.modules", MutablePath: "modules", FieldType: "set"},
	}

	t.Run("set_scalar", func(t *testing.T) {
		m := map[string]any{}
		if err := applyMutations(m, map[string]string{"name": "new"}, nil, fields); err != nil {
			t.Fatal(err)
		}
		if m["name"] != "new" {
			t.Fatalf("name = %v, want new", m["name"])
		}
	})

	t.Run("set_unknown_field_errors", func(t *testing.T) {
		err := applyMutations(map[string]any{}, map[string]string{"bad": "x"}, nil, fields)
		if err == nil || !strings.Contains(err.Error(), "unknown or read-only") {
			t.Fatalf("err = %v, want unknown or read-only", err)
		}
	})

	t.Run("set_tag_creates_entry", func(t *testing.T) {
		m := map[string]any{}
		if err := applyMutations(m, map[string]string{"labels.env": "prod"}, nil, fields); err != nil {
			t.Fatal(err)
		}
		tags, ok := m["labels"].(map[string]any)
		if !ok || tags["env"] != "prod" {
			t.Fatalf("labels.env = %v, want prod", m["labels"])
		}
	})

	t.Run("set_tag_no_subkey_errors", func(t *testing.T) {
		err := applyMutations(map[string]any{}, map[string]string{"labels": "v"}, nil, fields)
		if err == nil || !strings.Contains(err.Error(), "require a key") {
			t.Fatalf("err = %v, want require a key", err)
		}
	})

	t.Run("set_set_field_adds_member", func(t *testing.T) {
		m := map[string]any{}
		if err := applyMutations(m, map[string]string{"modules.CD": ""}, nil, fields); err != nil {
			t.Fatal(err)
		}
		if !sliceContains(getDotPathSlice(m, "modules"), "CD") {
			t.Fatalf("modules should contain CD: %v", m["modules"])
		}
	})

	t.Run("set_set_field_dedup", func(t *testing.T) {
		m := map[string]any{"modules": []any{"CD"}}
		if err := applyMutations(m, map[string]string{"modules.CD": ""}, nil, fields); err != nil {
			t.Fatal(err)
		}
		s := getDotPathSlice(m, "modules")
		if len(s) != 1 {
			t.Fatalf("modules should have 1 entry, got %v", s)
		}
	})

	t.Run("set_set_field_no_member_errors", func(t *testing.T) {
		err := applyMutations(map[string]any{}, map[string]string{"modules": ""}, nil, fields)
		if err == nil || !strings.Contains(err.Error(), "require a member") {
			t.Fatalf("err = %v, want require a member", err)
		}
	})

	t.Run("del_scalar_sets_nil", func(t *testing.T) {
		m := map[string]any{"name": "old"}
		if err := applyMutations(m, nil, []string{"name"}, fields); err != nil {
			t.Fatal(err)
		}
		if m["name"] != nil {
			t.Fatalf("name = %v, want nil", m["name"])
		}
	})

	t.Run("del_unknown_field_errors", func(t *testing.T) {
		err := applyMutations(map[string]any{}, nil, []string{"bad"}, fields)
		if err == nil || !strings.Contains(err.Error(), "unknown or read-only") {
			t.Fatalf("err = %v, want unknown or read-only", err)
		}
	})

	t.Run("del_tag_removes_entry", func(t *testing.T) {
		m := map[string]any{"labels": map[string]any{"env": "prod", "team": "ops"}}
		if err := applyMutations(m, nil, []string{"labels.env"}, fields); err != nil {
			t.Fatal(err)
		}
		tags := getDotPathMap(m, "labels")
		if _, found := tags["env"]; found {
			t.Fatalf("labels.env should be deleted, got %v", tags)
		}
		if tags["team"] != "ops" {
			t.Fatalf("labels.team should be preserved, got %v", tags)
		}
	})

	t.Run("del_tag_no_subkey_errors", func(t *testing.T) {
		err := applyMutations(map[string]any{}, nil, []string{"labels"}, fields)
		if err == nil || !strings.Contains(err.Error(), "require a key") {
			t.Fatalf("err = %v, want require a key", err)
		}
	})

	t.Run("del_set_field_removes_member", func(t *testing.T) {
		m := map[string]any{"modules": []any{"CD", "CE"}}
		if err := applyMutations(m, nil, []string{"modules.CD"}, fields); err != nil {
			t.Fatal(err)
		}
		s := getDotPathSlice(m, "modules")
		if sliceContains(s, "CD") || !sliceContains(s, "CE") {
			t.Fatalf("modules should be [CE], got %v", s)
		}
	})

	t.Run("del_set_field_no_member_errors", func(t *testing.T) {
		err := applyMutations(map[string]any{}, nil, []string{"modules"}, fields)
		if err == nil || !strings.Contains(err.Error(), "require a member") {
			t.Fatalf("err = %v, want require a member", err)
		}
	})
}

// ---------------------------------------------------------------------------
// TestRunGetThenUpdate — GetPath override, UpdateBodyPick miss, ItemExpr fallback, GET error
// ---------------------------------------------------------------------------

func TestRunGetThenUpdate(t *testing.T) {
	getResp := `{"data":{"name":"old"},"name":"old"}`

	t.Run("get_path_override", func(t *testing.T) {
		srv, caps := sequenceServer(t, []string{getResp, `{}`})
		ctx := testCtx(srv.URL, nil)
		ctx.Noun = "widget"
		ctx.Resolver = testNounRegistry(t)

		ep := &spec.EndpointSpec{
			Path:           "/widgets/w1",
			Method:         "PUT",
			UpdateStrategy: spec.UpdateStrategyGetThenPut,
			UpdateBodyPick: "it.data",
			GetPath:        "/widgets/get/w1",
		}
		_, _, err := callEndpointFull(ctx, ep, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if (*caps)[0].path != "/widgets/get/w1" {
			t.Fatalf("GET path = %q, want /widgets/get/w1", (*caps)[0].path)
		}
	})

	t.Run("update_body_pick_miss_errors", func(t *testing.T) {
		srv, _ := sequenceServer(t, []string{`{}`, `{}`})
		ctx := testCtx(srv.URL, nil)
		ctx.Noun = "widget"
		ctx.Resolver = testNounRegistry(t)

		ep := &spec.EndpointSpec{
			Path:           "/widgets/w1",
			Method:         "PUT",
			UpdateStrategy: spec.UpdateStrategyGetThenPut,
			UpdateBodyPick: "it.nonexistent.deep.path",
		}
		_, _, err := callEndpointFull(ctx, ep, nil)
		if err == nil || !strings.Contains(err.Error(), "did not resolve") {
			t.Fatalf("err = %v, want did not resolve", err)
		}
	})

	t.Run("item_expr_fallback", func(t *testing.T) {
		srv, caps := sequenceServer(t, []string{getResp, `{}`})
		ctx := testCtx(srv.URL, nil)
		ctx.Noun = "widget"
		ctx.Resolver = testNounRegistry(t)

		// No UpdateBodyPick; use ItemExpr as fallback.
		ep := &spec.EndpointSpec{
			Path:           "/widgets/w1",
			Method:         "PUT",
			UpdateStrategy: spec.UpdateStrategyGetThenPut,
			ItemExpr:       "it.data",
		}
		_, _, err := callEndpointFull(ctx, ep, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		putBody := bodyMap(t, (*caps)[1].body)
		if putBody["name"] != "old" {
			t.Fatalf("PUT body name = %v, want old (from it.data)", putBody["name"])
		}
	})

	t.Run("get_failure_errors", func(t *testing.T) {
		// Server returns HTTP 404 — client surfaces an error.
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"error":"not found"}`)
		}))
		t.Cleanup(srv.Close)
		ctx := testCtx(srv.URL, nil)
		ctx.Noun = "widget"
		ctx.Resolver = testNounRegistry(t)

		ep := &spec.EndpointSpec{
			Path:           "/widgets/w1",
			Method:         "PUT",
			UpdateStrategy: spec.UpdateStrategyGetThenPut,
			UpdateBodyPick: "it",
		}
		_, _, err := callEndpointFull(ctx, ep, nil)
		if err == nil || !strings.Contains(err.Error(), "GET failed") {
			t.Fatalf("err = %v, want GET failed", err)
		}
	})
}

// ---------------------------------------------------------------------------
// TestRunGetThenPutKV — GetPath override, ItemExpr unwrap, GET failure
// ---------------------------------------------------------------------------

func TestRunGetThenPutKV(t *testing.T) {
	t.Run("get_path_override", func(t *testing.T) {
		kvResp := `[{"key":"a","value":"1"}]`
		srv, caps := sequenceServer(t, []string{kvResp, `{}`})
		ctx := testCtx(srv.URL, nil)
		ctx.Resolver = testNounRegistry(t)

		ep := &spec.EndpointSpec{
			Path:           "/kv",
			Method:         "PUT",
			UpdateStrategy: spec.UpdateStrategyGetThenPutKV,
			GetPath:        "/kv/get",
		}
		_, _, err := callEndpointFull(ctx, ep, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if (*caps)[0].path != "/kv/get" {
			t.Fatalf("GET path = %q, want /kv/get", (*caps)[0].path)
		}
	})

	t.Run("item_expr_unwrap", func(t *testing.T) {
		// KV list is nested under "data" key; ItemExpr unwraps it.
		nestedResp := `{"data":[{"key":"env","value":"prod"}]}`
		srv, caps := sequenceServer(t, []string{nestedResp, `{}`})
		ctx := testCtx(srv.URL, nil)
		ctx.Resolver = testNounRegistry(t)

		ep := &spec.EndpointSpec{
			Path:           "/kv",
			Method:         "PUT",
			UpdateStrategy: spec.UpdateStrategyGetThenPutKV,
			ItemExpr:       "it.data",
		}
		_, _, err := callEndpointFull(ctx, ep, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		putBody := bodyMap(t, (*caps)[1].body)
		pairsRaw, _ := putBody["metadata"].([]any)
		if len(pairsRaw) == 0 {
			t.Fatalf("expected pairs in PUT body, got: %s", (*caps)[1].body)
		}
	})

	t.Run("get_failure_errors", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{}`)
		}))
		t.Cleanup(srv.Close)
		ctx := testCtx(srv.URL, nil)
		ctx.Resolver = testNounRegistry(t)

		ep := &spec.EndpointSpec{
			Path:           "/kv",
			Method:         "PUT",
			UpdateStrategy: spec.UpdateStrategyGetThenPutKV,
		}
		_, _, err := callEndpointFull(ctx, ep, nil)
		if err == nil || !strings.Contains(err.Error(), "GET failed") {
			t.Fatalf("err = %v, want GET failed", err)
		}
	})
}

// ---------------------------------------------------------------------------
// TestFirstNonEmpty / TestFirstNonEmptyMap
// ---------------------------------------------------------------------------

func TestFirstNonEmpty(t *testing.T) {
	if got := firstNonEmpty("", "b", "c"); got != "b" {
		t.Fatalf("got %q, want b", got)
	}
	if got := firstNonEmpty("", ""); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
	if got := firstNonEmpty("a", "b"); got != "a" {
		t.Fatalf("got %q, want a", got)
	}
}

func TestFirstNonEmptyMap(t *testing.T) {
	m1 := map[string]string{"a": "1"}
	m2 := map[string]string{"b": "2"}
	if got := firstNonEmptyMap(nil, m1, m2); got["a"] != "1" {
		t.Fatalf("expected m1, got %v", got)
	}
	if got := firstNonEmptyMap(nil, nil); got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
	if got := firstNonEmptyMap(m2, m1); got["b"] != "2" {
		t.Fatalf("expected m2, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// TestResolveContentType
// ---------------------------------------------------------------------------

func TestResolveContentType(t *testing.T) {
	tests := []struct {
		method      string
		epContentType string
		want        string
	}{
		{"POST", "", "application/json"},
		{"PUT", "", "application/json"},
		{"PATCH", "", "application/merge-patch+json"},
		{"GET", "", ""},
		{"DELETE", "", ""},
		{"POST", "text/xml", "text/xml"},
	}
	for _, tc := range tests {
		ep := &spec.EndpointSpec{ContentType: tc.epContentType}
		if got := resolveContentType(ep, tc.method); got != tc.want {
			t.Fatalf("resolveContentType(%s, %q) = %q, want %q", tc.method, tc.epContentType, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// TestSeedEnvelopeScope
// ---------------------------------------------------------------------------

func TestSeedEnvelopeScope(t *testing.T) {
	t.Run("nil_auth_no_panic", func(t *testing.T) {
		env := map[string]any{}
		ctx := testCtx("http://unused", nil)
		ctx.Auth = nil
		seedEnvelopeScope(env, ctx) // must not panic
		if len(env) != 0 {
			t.Fatalf("env should be unchanged, got %v", env)
		}
	})

	t.Run("seeds_missing_org_and_project", func(t *testing.T) {
		env := map[string]any{}
		ctx := testCtx("http://unused", nil)
		seedEnvelopeScope(env, ctx)
		if env["orgIdentifier"] != "org" {
			t.Fatalf("orgIdentifier = %v, want org", env["orgIdentifier"])
		}
		if env["projectIdentifier"] != "proj" {
			t.Fatalf("projectIdentifier = %v, want proj", env["projectIdentifier"])
		}
	})

	t.Run("does_not_overwrite_existing", func(t *testing.T) {
		env := map[string]any{"orgIdentifier": "custom-org"}
		ctx := testCtx("http://unused", nil)
		seedEnvelopeScope(env, ctx)
		if env["orgIdentifier"] != "custom-org" {
			t.Fatalf("orgIdentifier should not be overwritten, got %v", env["orgIdentifier"])
		}
		if env["projectIdentifier"] != "proj" {
			t.Fatalf("projectIdentifier = %v, want proj", env["projectIdentifier"])
		}
	})
}

// ---------------------------------------------------------------------------
// TestBuildYamlEnvelope — add already-wrapped YAML case
// ---------------------------------------------------------------------------

func TestBuildYamlEnvelopeWrapped(t *testing.T) {
	ctx := testCtx("http://unused", nil)
	// File already has the wrapper key — inner fields should be lifted, yaml field set.
	raw := "service:\n  identifier: s1\n  name: My Service\n"
	env, err := buildYamlEnvelope(ctx, "service", raw)
	if err != nil {
		t.Fatal(err)
	}
	if env["identifier"] != "s1" {
		t.Fatalf("identifier = %v, want s1", env["identifier"])
	}
	if _, ok := env["yaml"].(string); !ok {
		t.Fatalf("yaml field missing or not string: %v", env["yaml"])
	}
}

// ---------------------------------------------------------------------------
// TestUnwrapIfAlreadyWrapped — non-map input passthrough
// ---------------------------------------------------------------------------

func TestUnwrapIfAlreadyWrappedNonMap(t *testing.T) {
	got := unwrapIfAlreadyWrapped("raw string", "key")
	if got != "raw string" {
		t.Fatalf("got %v, want raw string", got)
	}
}

// ---------------------------------------------------------------------------
// TestGetDotPathMap
// ---------------------------------------------------------------------------

func TestGetDotPathMap(t *testing.T) {
	m := map[string]any{
		"flat": map[string]any{"x": 1},
		"str":  "not-a-map",
		"nested": map[string]any{
			"inner": map[string]any{"y": 2},
		},
	}

	tests := []struct {
		path    string
		wantNil bool
		wantKey string
	}{
		{"flat", false, "x"},
		{"missing", true, ""},
		{"str", true, ""},            // value is not a map
		{"nested.inner", false, "y"},
		{"nested.missing", true, ""},
		{"str.x", true, ""},          // intermediate not a map
	}
	for _, tc := range tests {
		got := getDotPathMap(m, tc.path)
		if tc.wantNil {
			if got != nil {
				t.Fatalf("getDotPathMap(%q) = %v, want nil", tc.path, got)
			}
		} else {
			if got == nil {
				t.Fatalf("getDotPathMap(%q) = nil, want non-nil", tc.path)
			}
			if _, ok := got[tc.wantKey]; !ok {
				t.Fatalf("getDotPathMap(%q) missing key %q: %v", tc.path, tc.wantKey, got)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// TestGetDotPathSlice
// ---------------------------------------------------------------------------

func TestGetDotPathSlice(t *testing.T) {
	m := map[string]any{
		"list":  []any{"a", "b"},
		"strs":  []string{"c", "d"},
		"str":   "not-a-slice",
		"nested": map[string]any{
			"items": []any{"e"},
		},
	}

	tests := []struct {
		path     string
		wantNil  bool
		wantLen  int
	}{
		{"list", false, 2},
		{"strs", false, 2},       // []string converted to []any
		{"missing", true, 0},
		{"str", true, 0},         // not a slice
		{"nested.items", false, 1},
		{"nested.missing", true, 0},
	}
	for _, tc := range tests {
		got := getDotPathSlice(m, tc.path)
		if tc.wantNil {
			if got != nil {
				t.Fatalf("getDotPathSlice(%q) = %v, want nil", tc.path, got)
			}
		} else {
			if len(got) != tc.wantLen {
				t.Fatalf("getDotPathSlice(%q) len = %d, want %d: %v", tc.path, len(got), tc.wantLen, got)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// TestToInt64
// ---------------------------------------------------------------------------

func TestToInt64(t *testing.T) {
	tests := []struct {
		in      any
		want    int64
		wantErr bool
	}{
		{int64(5), 5, false},
		{float64(3.7), 3, false},
		{int(7), 7, false},
		{"x", 0, true},
	}
	for _, tc := range tests {
		got, err := toInt64(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("toInt64(%v) expected error, got nil", tc.in)
			}
		} else {
			if err != nil {
				t.Fatalf("toInt64(%v) unexpected error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("toInt64(%v) = %d, want %d", tc.in, got, tc.want)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// TestSliceContainsAndRemove
// ---------------------------------------------------------------------------

func TestSliceContainsAndRemove(t *testing.T) {
	s := []any{"a", "b", "c"}

	if !sliceContains(s, "b") {
		t.Fatal("sliceContains should find b")
	}
	if sliceContains(s, "z") {
		t.Fatal("sliceContains should not find z")
	}
	if sliceContains(nil, "a") {
		t.Fatal("sliceContains on nil should return false")
	}

	got := sliceRemove(s, "b")
	if sliceContains(got, "b") {
		t.Fatal("sliceRemove should remove b")
	}
	if !sliceContains(got, "a") || !sliceContains(got, "c") {
		t.Fatalf("sliceRemove should keep a and c, got %v", got)
	}

	// Noop when element absent.
	got2 := sliceRemove(s, "z")
	if len(got2) != len(s) {
		t.Fatalf("sliceRemove of absent element changed length: %v", got2)
	}
}

// ---------------------------------------------------------------------------
// TestRunEndpoint
// ---------------------------------------------------------------------------

func readOut(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("readOut: %v", err)
	}
	return string(b)
}

func outFile(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "out")
}

func TestRunEndpoint(t *testing.T) {
	t.Run("verb_list_guard", func(t *testing.T) {
		ctx := testCtx("http://unused", nil)
		ctx.VerbHandler = VerbList
		_, err := RunEndpoint(ctx, &spec.EndpointSpec{Path: "/x", Method: "GET"})
		if err == nil || !strings.Contains(err.Error(), "RunListEndpoint") {
			t.Fatalf("err = %v, want RunListEndpoint mention", err)
		}
	})

	t.Run("list_fields_get", func(t *testing.T) {
		ctx := testCtx("http://unused", map[string]any{"list-fields": true})
		ctx.Noun = "widget"
		ctx.Resolver = testNounRegistry(t)
		out := outFile(t)
		ctx.FormatFlags.OutFile = out
		_, err := RunEndpoint(ctx, &spec.EndpointSpec{Path: "/x", Method: "GET"})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(readOut(t, out), "name") {
			t.Fatalf("field table missing 'name': %s", readOut(t, out))
		}
	})

	t.Run("list_fields_update", func(t *testing.T) {
		ctx := testCtx("http://unused", map[string]any{"list-fields": true})
		ctx.Noun = "widget"
		ctx.Resolver = testNounRegistry(t)
		ctx.VerbHandler = VerbUpdate
		out := outFile(t)
		ctx.FormatFlags.OutFile = out
		_, err := RunEndpoint(ctx, &spec.EndpointSpec{Path: "/x", Method: "PUT"})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(readOut(t, out), "name") {
			t.Fatalf("mutable field table missing 'name': %s", readOut(t, out))
		}
	})

	t.Run("nil_result_text_header", func(t *testing.T) {
		// Server returns empty body → result == nil; TextHeader should be printed.
		srv, _ := captureServer(t, "")
		ctx := testCtx(srv.URL, nil)
		out := outFile(t)
		ctx.FormatFlags.OutFile = out
		ep := &spec.EndpointSpec{Path: "/x", Method: "DELETE", TextHeader: "done\n"}
		_, err := RunEndpoint(ctx, ep)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(readOut(t, out), "done") {
			t.Fatalf("output missing 'done': %s", readOut(t, out))
		}
	})

	t.Run("field_extract_raw", func(t *testing.T) {
		srv, _ := captureServer(t, `{"yaml":"service:\n  name: foo\n"}`)
		ctx := testCtx(srv.URL, nil)
		out := outFile(t)
		ctx.FormatFlags.OutFile = out
		ep := &spec.EndpointSpec{Path: "/x", Method: "GET", FieldExtract: "yaml"}
		_, err := RunEndpoint(ctx, ep)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(readOut(t, out), "service:") {
			t.Fatalf("raw YAML not written: %s", readOut(t, out))
		}
	})

	t.Run("field_extract_json", func(t *testing.T) {
		srv, _ := captureServer(t, `{"yaml":"service:\n  name: foo\n"}`)
		ctx := testCtx(srv.URL, nil)
		out := outFile(t)
		ctx.FormatFlags.OutFile = out
		ctx.FormatFlags.Format = "json"
		ep := &spec.EndpointSpec{Path: "/x", Method: "GET", FieldExtract: "yaml"}
		_, err := RunEndpoint(ctx, ep)
		if err != nil {
			t.Fatal(err)
		}
		body := readOut(t, out)
		var parsed any
		if jsonErr := json.Unmarshal([]byte(body), &parsed); jsonErr != nil {
			t.Fatalf("output is not valid JSON: %s", body)
		}
	})

	t.Run("verb_delete_with_text", func(t *testing.T) {
		srv, _ := captureServer(t, `{}`)
		ctx := testCtx(srv.URL, nil)
		ctx.VerbHandler = VerbDelete
		out := outFile(t)
		ctx.FormatFlags.OutFile = out
		ep := &spec.EndpointSpec{Path: "/x", Method: "DELETE", TextHeader: "deleted\n"}
		result, err := RunEndpoint(ctx, ep)
		if err != nil {
			t.Fatal(err)
		}
		if result != nil {
			t.Fatalf("result = %v, want nil", result)
		}
		if !strings.Contains(readOut(t, out), "deleted") {
			t.Fatalf("output missing 'deleted': %s", readOut(t, out))
		}
	})

	t.Run("verb_delete_no_text", func(t *testing.T) {
		srv, _ := captureServer(t, `{}`)
		ctx := testCtx(srv.URL, nil)
		ctx.VerbHandler = VerbDelete
		ep := &spec.EndpointSpec{Path: "/x", Method: "DELETE"}
		result, err := RunEndpoint(ctx, ep)
		if err != nil {
			t.Fatal(err)
		}
		if result != nil {
			t.Fatalf("result = %v, want nil", result)
		}
	})

	t.Run("verb_get_fields_flag", func(t *testing.T) {
		srv, _ := captureServer(t, `{"name":"widget-1"}`)
		ctx := testCtx(srv.URL, nil)
		ctx.VerbHandler = VerbGet
		ctx.Noun = "widget"
		ctx.Resolver = testNounRegistry(t)
		ctx.FormatFlags.Fields = "name"
		out := outFile(t)
		ctx.FormatFlags.OutFile = out
		ep := &spec.EndpointSpec{Path: "/x", Method: "GET"}
		_, err := RunEndpoint(ctx, ep)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(readOut(t, out), "widget-1") {
			t.Fatalf("fields output missing 'widget-1': %s", readOut(t, out))
		}
	})

	t.Run("format_default_json", func(t *testing.T) {
		srv, _ := captureServer(t, `{"name":"foo"}`)
		ctx := testCtx(srv.URL, nil)
		out := outFile(t)
		ctx.FormatFlags.OutFile = out
		ep := &spec.EndpointSpec{Path: "/x", Method: "GET"}
		_, err := RunEndpoint(ctx, ep)
		if err != nil {
			t.Fatal(err)
		}
		body := readOut(t, out)
		m := bodyMap(t, []byte(body))
		if m["name"] != "foo" {
			t.Fatalf("json output name = %v, want foo", m["name"])
		}
	})

	t.Run("format_yaml_raw", func(t *testing.T) {
		srv, _ := captureServer(t, `{"name":"foo"}`)
		ctx := testCtx(srv.URL, nil)
		ctx.FormatFlags.Format = "yaml"
		ctx.FormatFlags.Raw = true
		out := outFile(t)
		ctx.FormatFlags.OutFile = out
		// YamlPickExpr required even with Raw=true (checked before the Raw branch).
		ep := &spec.EndpointSpec{Path: "/x", Method: "GET", YamlPickExpr: "it"}
		_, err := RunEndpoint(ctx, ep)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(readOut(t, out), "name: foo") {
			t.Fatalf("yaml output missing 'name: foo': %s", readOut(t, out))
		}
	})

	t.Run("call_endpoint_error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
		srv.Close() // closed immediately — TCP dial will fail
		ctx := testCtx(srv.URL, nil)
		_, err := RunEndpoint(ctx, &spec.EndpointSpec{Path: "/x", Method: "GET"})
		if err == nil {
			t.Fatal("expected error from closed server, got nil")
		}
	})

	t.Run("nil_result_no_textfmt", func(t *testing.T) {
		srv, _ := captureServer(t, "")
		ctx := testCtx(srv.URL, nil)
		// No TextHeader/Footer/TextFormatter → result==nil, textFmt==nil → return nil, nil
		result, err := RunEndpoint(ctx, &spec.EndpointSpec{Path: "/x", Method: "DELETE"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != nil {
			t.Fatalf("result = %v, want nil", result)
		}
	})

	t.Run("field_extract_not_a_map", func(t *testing.T) {
		srv, _ := captureServer(t, `[1,2,3]`)
		ctx := testCtx(srv.URL, nil)
		_, err := RunEndpoint(ctx, &spec.EndpointSpec{Path: "/x", Method: "GET", FieldExtract: "x"})
		if err == nil || !strings.Contains(err.Error(), "not a JSON object") {
			t.Fatalf("err = %v, want 'not a JSON object'", err)
		}
	})

	t.Run("field_extract_field_missing", func(t *testing.T) {
		srv, _ := captureServer(t, `{"other":"v"}`)
		ctx := testCtx(srv.URL, nil)
		_, err := RunEndpoint(ctx, &spec.EndpointSpec{Path: "/x", Method: "GET", FieldExtract: "missing"})
		if err == nil || !strings.Contains(err.Error(), "not found") {
			t.Fatalf("err = %v, want 'not found'", err)
		}
	})

	t.Run("field_extract_field_not_string", func(t *testing.T) {
		srv, _ := captureServer(t, `{"count":42}`)
		ctx := testCtx(srv.URL, nil)
		_, err := RunEndpoint(ctx, &spec.EndpointSpec{Path: "/x", Method: "GET", FieldExtract: "count"})
		if err == nil || !strings.Contains(err.Error(), "not a string") {
			t.Fatalf("err = %v, want 'not a string'", err)
		}
	})

	t.Run("format_unsupported", func(t *testing.T) {
		srv, _ := captureServer(t, `{"name":"foo"}`)
		ctx := testCtx(srv.URL, nil)
		ctx.FormatFlags.Format = "csv"
		_, err := RunEndpoint(ctx, &spec.EndpointSpec{Path: "/x", Method: "GET"})
		if err == nil || !strings.Contains(err.Error(), "not supported") {
			t.Fatalf("err = %v, want 'not supported'", err)
		}
	})
}

// ---------------------------------------------------------------------------
// TestRunListEndpoint
// ---------------------------------------------------------------------------

func TestRunListEndpoint(t *testing.T) {
	paging := &spec.PagingSpec{PagingStrategy: spec.PagingStrategyFlatList, Countable: true}

	t.Run("no_paging_json", func(t *testing.T) {
		srv, _ := captureServer(t, `[{"name":"a"}]`)
		ctx := testCtx(srv.URL, nil)
		ctx.FormatFlags.Format = "json"
		out := outFile(t)
		ctx.FormatFlags.OutFile = out
		ep := &spec.EndpointSpec{Path: "/x", Method: "GET", ItemsExpr: "it"}
		if err := RunListEndpoint(ctx, ep); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(readOut(t, out), "name") {
			t.Fatalf("output missing 'name': %s", readOut(t, out))
		}
	})

	t.Run("no_paging_call_error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
		srv.Close()
		ctx := testCtx(srv.URL, nil)
		ep := &spec.EndpointSpec{Path: "/x", Method: "GET", ItemsExpr: "it"}
		if err := RunListEndpoint(ctx, ep); err == nil {
			t.Fatal("expected error from closed server")
		}
	})

	t.Run("paged_items_json", func(t *testing.T) {
		srv, _ := captureServer(t, `[{"name":"a"},{"name":"b"}]`)
		ctx := testCtx(srv.URL, nil)
		ctx.FormatFlags.Format = "json"
		out := outFile(t)
		ctx.FormatFlags.OutFile = out
		ep := &spec.EndpointSpec{Path: "/x", Method: "GET", ItemsExpr: "it", Paging: paging}
		if err := RunListEndpoint(ctx, ep); err != nil {
			t.Fatal(err)
		}
		body := readOut(t, out)
		if !strings.Contains(body, "name") {
			t.Fatalf("output missing items: %s", body)
		}
	})

	t.Run("paged_items_error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
		srv.Close()
		ctx := testCtx(srv.URL, nil)
		ep := &spec.EndpointSpec{Path: "/x", Method: "GET", ItemsExpr: "it", Paging: paging}
		if err := RunListEndpoint(ctx, ep); err == nil {
			t.Fatal("expected error from closed server")
		}
	})

	t.Run("paged_count", func(t *testing.T) {
		srv, _ := captureServer(t, `[{"name":"a"},{"name":"b"}]`)
		ctx := testCtx(srv.URL, nil)
		ctx.PagingFlags.Count = true
		out := outFile(t)
		ctx.FormatFlags.OutFile = out
		ep := &spec.EndpointSpec{Path: "/x", Method: "GET", ItemsExpr: "it", Paging: paging}
		if err := RunListEndpoint(ctx, ep); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(readOut(t, out), "2") {
			t.Fatalf("count output missing '2': %s", readOut(t, out))
		}
	})

	t.Run("paged_count_error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
		srv.Close()
		ctx := testCtx(srv.URL, nil)
		ctx.PagingFlags.Count = true
		ep := &spec.EndpointSpec{Path: "/x", Method: "GET", ItemsExpr: "it", Paging: paging}
		if err := RunListEndpoint(ctx, ep); err == nil {
			t.Fatal("expected error from closed server")
		}
	})

	t.Run("format_unsupported", func(t *testing.T) {
		srv, _ := captureServer(t, `[{"name":"a"}]`)
		ctx := testCtx(srv.URL, nil)
		ctx.FormatFlags.Format = "bad"
		ep := &spec.EndpointSpec{Path: "/x", Method: "GET", ItemsExpr: "it"}
		if err := RunListEndpoint(ctx, ep); err == nil || !strings.Contains(err.Error(), "unknown format") {
			t.Fatalf("err = %v, want 'unknown format'", err)
		}
	})
}
