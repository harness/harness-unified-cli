// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/harness/harness-cli/pkg/auth"
	hclient "github.com/harness/harness-cli/pkg/client"
	"github.com/harness/harness-cli/pkg/cmdctx"
	"github.com/harness/harness-cli/pkg/console"
	"github.com/harness/harness-cli/pkg/format"
	"github.com/harness/harness-cli/pkg/hbase"
)

type checkResult struct {
	OK     bool   `json:"ok"`
	Warn   bool   `json:"warn,omitempty"`  // soft warning: not OK but not a blocking error
	Name   string `json:"name,omitempty"`
	Error  string `json:"error,omitempty"`  // short message shown in the row
	Detail string `json:"detail,omitempty"` // actionable message shown at the bottom
}

type statusChecks struct {
	Profile checkResult  `json:"Profile"`
	API     checkResult  `json:"API"`
	User    checkResult  `json:"User"`
	Account checkResult  `json:"Account"`
	Org     *checkResult `json:"Org,omitempty"`
	Project *checkResult `json:"Project,omitempty"`
}

type statusResult struct {
	Source      string       `json:"Source"`
	Profile     string       `json:"Profile"`
	APIUrl      string       `json:"APIUrl"`
	RegistryURL string       `json:"RegistryURL,omitempty"`
	AccountID   string       `json:"AccountID"`
	OrgID       string       `json:"OrgID,omitempty"`
	ProjectID   string       `json:"ProjectID,omitempty"`
	ProjectURL  string       `json:"ProjectURL,omitempty"`
	IsSAT       bool         `json:"IsSAT,omitempty"`
	TokenType   string       `json:"TokenType,omitempty"`
	SATIdentity string       `json:"SATIdentity,omitempty"` // "username (email)" from token/validate
	Status      statusChecks `json:"Status"`
	CurrentUser any          `json:"CurrentUser,omitempty"`
}

func profileName(source string) string {
	if s, ok := strings.CutPrefix(source, "profile:"); ok {
		return s
	}
	return source
}

func StatusHandler(ctx *cmdctx.Ctx) error {
	profileFlag := cmdctx.GetString(ctx.FlagValues, "profile")
	jsonMode := ctx.FormatFlags.Format == "json"
	tokenStatus := cmdctx.GetBool(ctx.FlagValues, "token-status")

	r := runStatusChecks(profileFlag)

	if jsonMode {
		out, _ := json.MarshalIndent(r, "", "  ")
		fmt.Println(string(out))
	} else {
		printStatus(r)
		if tokenStatus {
			printTokenStatus(profileFlag)
		}
	}
	return checkErrors(r)
}

func runStatusChecks(profileFlag string) statusResult {
	// Determine the source before resolution so error display is correct.
	var anticipatedSource string
	if profileFlag != "" {
		anticipatedSource = "profile:" + profileFlag
	} else if os.Getenv(hbase.EnvAPIKey) != "" {
		anticipatedSource = auth.SourceEnv
	} else if env := os.Getenv(hbase.EnvProfile); env != "" {
		anticipatedSource = "profile:" + env
	} else {
		anticipatedSource = "profile:default"
	}

	skip := checkResult{OK: false, Error: "skipped"}
	r := statusResult{Source: anticipatedSource, Profile: profileName(anticipatedSource)}

	resolved, loadErr := auth.Load(profileFlag)
	if loadErr != nil {
		r.Status.Profile = checkResult{OK: false, Error: loadErr.Error()}
		r.Status.API = skip
		r.Status.User = skip
		r.Status.Account = skip
		r.Status.Org = &skip
		r.Status.Project = &skip
		return r
	}
	if err := auth.CheckAndUpdateAccessToken(resolved, time.Now()); err != nil {
		r.Status.Profile = checkResult{OK: false, Error: err.Error()}
		r.Status.API = skip
		r.Status.User = skip
		r.Status.Account = skip
		r.Status.Org = &skip
		r.Status.Project = &skip
		return r
	}
	r.Source = resolved.Source
	r.Profile = profileName(resolved.Source)
	r.APIUrl = resolved.APIUrl
	r.RegistryURL = resolved.RegistryURL
	r.AccountID = resolved.AccountID
	r.OrgID = resolved.OrgID
	r.ProjectID = resolved.ProjectID
	r.IsSAT = auth.TokenType(resolved.PATToken) == auth.TokenKindSAT
	switch {
	case resolved.AuthType == auth.AuthTypeSSO:
		r.TokenType = "SSO"
	case auth.TokenType(resolved.PATToken) == auth.TokenKindSAT:
		r.TokenType = "SAT"
	default:
		r.TokenType = "PAT"
	}
	r.Status.Profile = checkResult{OK: true}

	if err := checkAPIUrl(resolved.APIUrl); err != nil {
		r.Status.API = checkResult{OK: false, Error: err.Error()}
		r.Status.User = skip
		r.Status.Account = skip
		r.Status.Org = &skip
		r.Status.Project = &skip
		return r
	}
	r.Status.API = checkResult{OK: true}

	if resolved.AuthType != auth.AuthTypeSSO {
		if err := auth.ValidatePATFormat(resolved.PATToken); err != nil {
			r.Status.User = checkResult{OK: false, Error: err.Error()}
			r.Status.Account = skip
			r.Status.Org = &skip
			r.Status.Project = &skip
			return r
		}
	}

	c := &http.Client{Timeout: 10 * time.Second}
	isSAT := auth.TokenType(resolved.PATToken) == auth.TokenKindSAT
	if isSAT {
		identity, err := validateSATToken(c, resolved)
		if err != nil {
			r.Status.User = checkResult{OK: false, Error: err.Error()}
			r.Status.Account = skip
			r.Status.Org = &skip
			r.Status.Project = &skip
			return r
		}
		r.SATIdentity = identity
		r.Status.User = checkResult{OK: true}
	} else {
		currentUser, err := fetchCurrentUser(c, resolved)
		if err != nil {
			r.Status.User = checkResult{OK: false, Error: err.Error()}
			r.Status.Account = skip
			r.Status.Org = &skip
			r.Status.Project = &skip
			return r
		}
		r.CurrentUser = currentUser
		r.Status.User = checkResult{OK: true}
	}

	// softErr wraps a 403 as a warning for SAT tokens — the SA may lack enumeration
	// permissions but still have resource-level access.
	softErr := func(err error) checkResult {
		if isSAT && err != nil && strings.Contains(err.Error(), "403") {
			return checkResult{Warn: true, Error: "access denied (403) — SA may lack enumeration permissions"}
		}
		if err != nil {
			return checkResult{OK: false, Error: err.Error()}
		}
		return checkResult{}
	}

	accountName, err := checkAccount(c, resolved)
	if err != nil {
		cr := softErr(err)
		if cr.Warn {
			r.Status.Account = cr
		} else {
			r.Status.Account = cr
			r.Status.Org = &skip
			r.Status.Project = &skip
			return r
		}
	} else {
		r.Status.Account = checkResult{OK: true, Name: accountName}
	}

	if resolved.OrgID == "" {
		orgResult := notSetResult(resolved.Source, hbase.EnvOrg, "org")
		projectResult := notSetResult(resolved.Source, hbase.EnvProject, "project")
		r.Status.Org = &orgResult
		r.Status.Project = &projectResult
		return r
	}
	orgName, err := checkOrg(c, resolved)
	if err != nil {
		cr := softErr(err)
		if cr.Warn {
			orgWarn := cr
			r.Status.Org = &orgWarn
			projWarn := checkResult{Warn: true, Error: "access denied (403) — SA may lack enumeration permissions"}
			r.Status.Project = &projWarn
			return r
		}
		r.Status.Org = &checkResult{OK: false, Error: err.Error()}
		r.Status.Project = &skip
		return r
	}
	r.Status.Org = &checkResult{OK: true, Name: orgName}

	if resolved.ProjectID == "" {
		projectResult := notSetResult(resolved.Source, hbase.EnvProject, "project")
		r.Status.Project = &projectResult
		return r
	}
	projectName, err := checkProject(c, resolved)
	if err != nil {
		cr := softErr(err)
		if cr.Warn {
			projWarn := cr
			r.Status.Project = &projWarn
			return r
		}
		r.Status.Project = &checkResult{OK: false, Error: err.Error()}
		return r
	}
	r.Status.Project = &checkResult{OK: true, Name: projectName}
	uiBase := resolved.UIUrl
	if uiBase == "" {
		uiBase = resolved.APIUrl
	}
	r.ProjectURL = fmt.Sprintf("%s/ng/account/%s/all/orgs/%s/projects/%s/overview",
		uiBase, resolved.AccountID, resolved.OrgID, resolved.ProjectID)

	return r
}

func notSetResult(source, envVar, noun string) checkResult {
	if source == auth.SourceEnv {
		return checkResult{
			OK:     false,
			Error:  "not set",
			Detail: fmt.Sprintf("%s is not set", envVar),
		}
	}
	return checkResult{
		OK:     false,
		Error:  "not set",
		Detail: fmt.Sprintf("profile has no %s — run 'harness auth setscope' to configure it", noun),
	}
}

func statusValue(ok, warn bool, value, errMsg string) string {
	var icon string
	switch {
	case ok:
		icon = console.GreenCheck()
	case warn:
		icon = console.YellowWarning()
	default:
		icon = console.RedX()
	}
	if value == "" {
		return fmt.Sprintf("%s %s", icon, errMsg)
	}
	if errMsg == "" {
		return fmt.Sprintf("%s %s", icon, value)
	}
	return fmt.Sprintf("%s %s — %s", icon, value, errMsg)
}

func printStatus(r statusResult) {
	var rows []format.LabeledValue
	add := func(label, value string) {
		rows = append(rows, format.LabeledValue{Label: label, Value: value})
	}

	sv := func(c checkResult, value string) string {
		return statusValue(c.OK, c.Warn, value, c.Error)
	}

	profileSuffix := ""
	if r.TokenType == "SSO" || r.TokenType == "SAT" {
		profileSuffix = fmt.Sprintf(" (%s token)", r.TokenType)
	}
	if r.Source == auth.SourceEnv {
		add("Mode", sv(r.Status.Profile, "env vars"+profileSuffix))
	} else {
		add("Profile", sv(r.Status.Profile, r.Profile+profileSuffix))
	}
	add("APIUrl", sv(r.Status.API, r.APIUrl))
	if r.RegistryURL != "" {
		add("RegistryUrl", fmt.Sprintf("%s %s", console.GreenCheck(), r.RegistryURL))
	}

	userLabel := "User"
	userVal := ""
	if r.IsSAT {
		userLabel = "Token"
		if r.Status.User.OK {
			userVal = r.SATIdentity
		}
	} else {
		if email, uuid := currentUserFields(r.CurrentUser); email != "" {
			userVal = fmt.Sprintf("%s (%s)", email, uuid)
		}
	}
	add(userLabel, sv(r.Status.User, userVal))
	add("Account", sv(r.Status.Account, func() string {
		if r.Status.Account.OK {
			return fmt.Sprintf("%s (%s)", r.Status.Account.Name, r.AccountID)
		}
		return r.AccountID
	}()))
	if r.OrgID != "" || r.Status.Org != nil {
		org := r.Status.Org
		if org == nil {
			org = &checkResult{OK: false, Error: "skipped"}
		}
		add("Org", sv(*org, func() string {
			if org.OK {
				return fmt.Sprintf("%s (%s)", org.Name, r.OrgID)
			}
			return r.OrgID
		}()))
	}
	if r.ProjectID != "" || r.Status.Project != nil {
		proj := r.Status.Project
		if proj == nil {
			proj = &checkResult{OK: false, Error: "skipped"}
		}
		add("Project", sv(*proj, func() string {
			if proj.OK {
				return fmt.Sprintf("%s (%s)", proj.Name, r.ProjectID)
			}
			return r.ProjectID
		}()))
		if r.ProjectURL != "" {
			add("ProjectURL", r.ProjectURL)
		}
	}

	format.WriteLabeledValues(os.Stdout, rows)
}

func printTokenStatus(profileFlag string) {
	resolved, err := auth.Load(profileFlag)
	if err != nil || resolved.AuthType != auth.AuthTypeSSO {
		return
	}
	fmt.Println()
	printTokenExpiry(resolved.SSOToken, resolved.RefreshToken)
}

func checkErrors(r statusResult) error {
	failed := func(c checkResult) bool { return !c.OK && !c.Warn && c.Error != "skipped" }
	msg := func(c checkResult) string {
		if c.Detail != "" {
			return c.Detail
		}
		return c.Error
	}
	if failed(r.Status.Profile) {
		return fmt.Errorf("\nError: %s", msg(r.Status.Profile))
	}
	if failed(r.Status.API) {
		return fmt.Errorf("\nError: %s", msg(r.Status.API))
	}
	if failed(r.Status.User) {
		return fmt.Errorf("\nError: %s", msg(r.Status.User))
	}
	if failed(r.Status.Account) {
		return fmt.Errorf("\nError: %s", msg(r.Status.Account))
	}
	if r.Status.Org != nil && failed(*r.Status.Org) {
		return fmt.Errorf("\nError: %s", msg(*r.Status.Org))
	}
	if r.Status.Project != nil && failed(*r.Status.Project) {
		return fmt.Errorf("\nError: %s", msg(*r.Status.Project))
	}
	return nil
}

func currentUserFields(u any) (email, uuid string) {
	m, ok := u.(map[string]any)
	if !ok {
		return
	}
	data, ok := m["data"].(map[string]any)
	if !ok {
		return
	}
	email, _ = data["email"].(string)
	uuid, _ = data["uuid"].(string)
	return
}

func checkAPIUrl(apiURL string) error {
	if err := auth.ValidateAPIURL(apiURL); err != nil {
		return err
	}
	u, _ := url.Parse(apiURL)
	_, err := net.DialTimeout("tcp", u.Hostname()+":443", 5*time.Second)
	if err != nil {
		if _, ok := errors.AsType[*net.DNSError](err); ok {
			return fmt.Errorf("cannot resolve host %q — check your API URL", u.Hostname())
		}
		return fmt.Errorf("cannot reach %q — %s", u.Hostname(), err)
	}
	return nil
}

// validateSATToken calls POST /ng/api/token/validate and returns a display identity
// string of the form "username (email)" parsed from the response.
func validateSATToken(c *http.Client, a *auth.ResolvedAuth) (identity string, err error) {
	u := fmt.Sprintf("%s/ng/api/token/validate?accountIdentifier=%s", a.APIUrl, a.AccountID)
	req, err := http.NewRequest("POST", u, strings.NewReader(a.PATToken))
	if err != nil {
		return "", fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("x-api-key", a.PATToken)
	resp, err := c.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	switch resp.StatusCode {
	case 200:
		var result struct {
			Data struct {
				Username string `json:"username"`
				Email    string `json:"email"`
			} `json:"data"`
		}
		if jerr := json.Unmarshal(body, &result); jerr == nil {
			u, e := result.Data.Username, result.Data.Email
			if u != "" && e != "" {
				identity = fmt.Sprintf("%s (%s)", u, e)
			} else if u != "" {
				identity = u
			}
		}
		return identity, nil
	case 401:
		return "", fmt.Errorf("token rejected (401)")
	case 403:
		return "", fmt.Errorf("access denied (403)")
	default:
		return "", fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
}

func fetchCurrentUser(c *http.Client, a *auth.ResolvedAuth) (any, error) {
	url := fmt.Sprintf("%s/ng/api/user/currentUser?accountIdentifier=%s", a.APIUrl, a.AccountID)
	body, status, err := doGet(c, url, a)
	if err != nil {
		return nil, err
	}
	switch status {
	case 200:
		var result any
		if err := json.Unmarshal(body, &result); err != nil {
			return nil, fmt.Errorf("decoding response: %w", err)
		}
		return result, nil
	default:
		return nil, fmt.Errorf("API error %d: %s", status, apiErrMsg(status, body))
	}
}

func checkAccount(c *http.Client, a *auth.ResolvedAuth) (string, error) {
	url := fmt.Sprintf("%s/ng/api/accounts/%s?accountIdentifier=%s", a.APIUrl, a.AccountID, a.AccountID)
	return checkResource(c, a, url, "account", a.AccountID, "access denied (403) — check account ID or RBAC permissions", "data", "name")
}

func checkOrg(c *http.Client, a *auth.ResolvedAuth) (string, error) {
	url := fmt.Sprintf("%s/ng/api/organizations/%s?accountIdentifier=%s", a.APIUrl, a.OrgID, a.AccountID)
	return checkResource(c, a, url, "org", a.OrgID, "access denied (403)", "data", "organization", "name")
}

func checkProject(c *http.Client, a *auth.ResolvedAuth) (string, error) {
	url := fmt.Sprintf("%s/ng/api/projects/%s?accountIdentifier=%s&orgIdentifier=%s",
		a.APIUrl, a.ProjectID, a.AccountID, a.OrgID)
	return checkResource(c, a, url, "project", a.ProjectID, "access denied (403)", "data", "project", "name")
}

func checkResource(c *http.Client, a *auth.ResolvedAuth, url, entityType, entityID, forbidden string, jsonPath ...string) (string, error) {
	body, status, err := doGet(c, url, a)
	if err != nil {
		return "", err
	}
	switch status {
	case 200:
		name := jsonStringAt(body, jsonPath...)
		if name == "" {
			name = entityID
		}
		return name, nil
	case 403:
		return "", fmt.Errorf("%s", forbidden)
	case 404:
		return "", fmt.Errorf("%s %q not found (404)", entityType, entityID)
	default:
		return "", fmt.Errorf("API error %d: %s", status, apiErrMsg(status, body))
	}
}

func doGet(c *http.Client, url string, a *auth.ResolvedAuth) ([]byte, int, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("building request: %w", err)
	}
	if a.AuthType == auth.AuthTypeSSO {
		req.Header.Set("Authorization", "Bearer "+a.SSOToken)
	} else {
		req.Header.Set("x-api-key", a.PATToken)
	}

	resp, err := c.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("reading response: %w", err)
	}
	return body, resp.StatusCode, nil
}

func apiErrMsg(status int, body []byte) string {
	return hclient.APIErrorMessage(status, body)
}

func jsonStringAt(data []byte, keys ...string) string {
	var m any
	if err := json.Unmarshal(data, &m); err != nil {
		return ""
	}
	for _, k := range keys {
		mm, ok := m.(map[string]any)
		if !ok {
			return ""
		}
		m = mm[k]
	}
	s, _ := m.(string)
	return s
}
