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
	"github.com/harness/harness-cli/pkg/cmdctx"
	"github.com/harness/harness-cli/pkg/console"
	"github.com/harness/harness-cli/pkg/format"
	"github.com/harness/harness-cli/pkg/hbase"
)

type checkResult struct {
	OK     bool   `json:"ok"`
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

	r := runStatusChecks(profileFlag)

	if jsonMode {
		out, _ := json.MarshalIndent(r, "", "  ")
		fmt.Println(string(out))
	} else {
		printStatus(r)
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
	r.Source = resolved.Source
	r.Profile = profileName(resolved.Source)
	r.APIUrl = resolved.APIUrl
	r.RegistryURL = resolved.RegistryURL
	r.AccountID = resolved.AccountID
	r.OrgID = resolved.OrgID
	r.ProjectID = resolved.ProjectID
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

	accountName, err := checkAccount(c, resolved)
	if err != nil {
		r.Status.Account = checkResult{OK: false, Error: err.Error()}
		r.Status.Org = &skip
		r.Status.Project = &skip
		return r
	}
	r.Status.Account = checkResult{OK: true, Name: accountName}

	if resolved.OrgID == "" {
		orgResult := notSetResult(resolved.Source, hbase.EnvOrg, "org")
		projectResult := notSetResult(resolved.Source, hbase.EnvProject, "project")
		r.Status.Org = &orgResult
		r.Status.Project = &projectResult
		return r
	}
	orgName, err := checkOrg(c, resolved)
	if err != nil {
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
		r.Status.Project = &checkResult{OK: false, Error: err.Error()}
		return r
	}
	r.Status.Project = &checkResult{OK: true, Name: projectName}
	r.ProjectURL = fmt.Sprintf("%s/ng/account/%s/all/orgs/%s/projects/%s/overview",
		resolved.APIUrl, resolved.AccountID, resolved.OrgID, resolved.ProjectID)

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

func statusValue(ok bool, value, errMsg string) string {
	icon := console.GreenCheck()
	if !ok {
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

	if r.Source == auth.SourceEnv {
		add("Mode", statusValue(r.Status.Profile.OK, "env vars", r.Status.Profile.Error))
	} else {
		add("Profile", statusValue(r.Status.Profile.OK, r.Profile, r.Status.Profile.Error))
	}
	add("APIUrl", statusValue(r.Status.API.OK, r.APIUrl, r.Status.API.Error))
	if r.RegistryURL != "" {
		add("RegistryUrl", fmt.Sprintf("%s %s", console.GreenCheck(), r.RegistryURL))
	}

	userVal := ""
	if email, uuid := currentUserFields(r.CurrentUser); email != "" {
		userVal = fmt.Sprintf("%s (%s)", email, uuid)
	}
	add("User", statusValue(r.Status.User.OK, userVal, r.Status.User.Error))
	add("Account", statusValue(r.Status.Account.OK, func() string {
		if r.Status.Account.OK {
			return fmt.Sprintf("%s (%s)", r.Status.Account.Name, r.AccountID)
		}
		return r.AccountID
	}(), r.Status.Account.Error))
	if r.OrgID != "" || r.Status.Org != nil {
		org := r.Status.Org
		if org == nil {
			org = &checkResult{OK: false, Error: "skipped"}
		}
		add("Org", statusValue(org.OK, func() string {
			if org.OK {
				return fmt.Sprintf("%s (%s)", org.Name, r.OrgID)
			}
			return r.OrgID
		}(), org.Error))
	}
	if r.ProjectID != "" || r.Status.Project != nil {
		proj := r.Status.Project
		if proj == nil {
			proj = &checkResult{OK: false, Error: "skipped"}
		}
		add("Project", statusValue(proj.OK, func() string {
			if proj.OK {
				return fmt.Sprintf("%s (%s)", proj.Name, r.ProjectID)
			}
			return r.ProjectID
		}(), proj.Error))
		if r.ProjectURL != "" {
			add("ProjectURL", r.ProjectURL)
		}
	}

	format.WriteLabeledValues(os.Stdout, rows)
}

func checkErrors(r statusResult) error {
	failed := func(c checkResult) bool { return !c.OK && c.Error != "skipped" }
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
	case 401:
		return nil, fmt.Errorf("token rejected (401)")
	case 403:
		return nil, fmt.Errorf("access denied (403)")
	default:
		return nil, fmt.Errorf("unexpected status %d", status)
	}
}

func checkAccount(c *http.Client, a *auth.ResolvedAuth) (string, error) {
	url := fmt.Sprintf("%s/ng/api/accounts/%s?accountIdentifier=%s", a.APIUrl, a.AccountID, a.AccountID)
	body, status, err := doGet(c, url, a)
	if err != nil {
		return "", err
	}
	switch status {
	case 200:
		name := jsonStringAt(body, "data", "name")
		if name == "" {
			name = a.AccountID
		}
		return name, nil
	case 401:
		return "", fmt.Errorf("token rejected (401)")
	case 403:
		return "", fmt.Errorf("access denied (403) — check account ID or RBAC permissions")
	case 404:
		return "", fmt.Errorf("account %q not found (404)", a.AccountID)
	default:
		return "", fmt.Errorf("unexpected status %d", status)
	}
}

func checkOrg(c *http.Client, a *auth.ResolvedAuth) (string, error) {
	url := fmt.Sprintf("%s/ng/api/organizations/%s?accountIdentifier=%s", a.APIUrl, a.OrgID, a.AccountID)
	body, status, err := doGet(c, url, a)
	if err != nil {
		return "", err
	}
	switch status {
	case 200:
		name := jsonStringAt(body, "data", "organization", "name")
		if name == "" {
			name = a.OrgID
		}
		return name, nil
	case 401:
		return "", fmt.Errorf("token rejected (401)")
	case 403:
		return "", fmt.Errorf("access denied (403)")
	case 404:
		return "", fmt.Errorf("org %q not found (404)", a.OrgID)
	default:
		return "", fmt.Errorf("unexpected status %d", status)
	}
}

func checkProject(c *http.Client, a *auth.ResolvedAuth) (string, error) {
	url := fmt.Sprintf("%s/ng/api/projects/%s?accountIdentifier=%s&orgIdentifier=%s",
		a.APIUrl, a.ProjectID, a.AccountID, a.OrgID)
	body, status, err := doGet(c, url, a)
	if err != nil {
		return "", err
	}
	switch status {
	case 200:
		name := jsonStringAt(body, "data", "project", "name")
		if name == "" {
			name = a.ProjectID
		}
		return name, nil
	case 401:
		return "", fmt.Errorf("token rejected (401)")
	case 403:
		return "", fmt.Errorf("access denied (403)")
	case 404:
		return "", fmt.Errorf("project %q not found (404)", a.ProjectID)
	default:
		return "", fmt.Errorf("unexpected status %d", status)
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
