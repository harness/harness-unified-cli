// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package har

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jedib0t/go-pretty/v6/table"
	"go.yaml.in/yaml/v3"

	"github.com/harness/harness-cli/pkg/cmdctx"
)

const executeArtifactFirewallScanHandlerID = "execute_artifact_firewall_scan"

// ---- request / response types ----

type artifactScanInput struct {
	PackageName string `json:"packageName"`
	Version     string `json:"version"`
}

type bulkEvalRequest struct {
	RegistryId string              `json:"registryId"`
	Artifacts  []artifactScanInput `json:"artifacts"`
}

type bulkEvalAcceptedData struct {
	EvaluationId *string `json:"evaluationId,omitempty"`
}

type bulkEvalAcceptedResp struct {
	Data *bulkEvalAcceptedData `json:"data,omitempty"`
}

type bulkEvalStatusData struct {
	Status *string         `json:"status,omitempty"`
	Error  *string         `json:"error,omitempty"`
	Scans  *[]bulkScanItem `json:"scans,omitempty"`
}

type bulkEvalStatusResp struct {
	Data *bulkEvalStatusData `json:"data,omitempty"`
}

type bulkScanItem struct {
	ScanId      *string `json:"scanId,omitempty"`
	ScanStatus  *string `json:"scanStatus,omitempty"`
	PackageName *string `json:"packageName,omitempty"`
	Version     *string `json:"version,omitempty"`
}

type fixVersionDetails struct {
	FixVersionAvailable bool    `json:"fixVersionAvailable"`
	CurrentVersion      string  `json:"currentVersion"`
	FixVersion          *string `json:"fixVersion,omitempty"`
}

type policyFailureDetail struct {
	Category   string          `json:"category"`
	PolicyName string          `json:"policyName"`
	PolicyRef  string          `json:"policyRef"`
	Config     json.RawMessage `json:"config,omitempty"`
}

// Inline JSON structs for per-category config payloads.
type securityVuln struct {
	CveId         string  `json:"cveId"`
	CvssScore     float64 `json:"cvssScore"`
	CvssThreshold float64 `json:"cvssThreshold"`
}

type securityConfig struct {
	Vulnerabilities []securityVuln `json:"vulnerabilities"`
}

type licenseConfig struct {
	BlockedLicense  string   `json:"blockedLicense"`
	AllowedLicenses []string `json:"allowedLicenses"`
}

type packageAgeConfig struct {
	PublishedOn         string `json:"publishedOn"`
	PackageAgeThreshold string `json:"packageAgeThreshold"`
}

type policySetFailureDetail struct {
	PolicySetName        string                `json:"policySetName"`
	PolicySetRef         string                `json:"policySetRef"`
	PolicyFailureDetails []policyFailureDetail `json:"policyFailureDetails"`
}

type artifactScanDetails struct {
	LastEvaluatedAt         *string                   `json:"lastEvaluatedAt,omitempty"`
	FixVersionDetails       *fixVersionDetails        `json:"fixVersionDetails,omitempty"`
	PolicySetFailureDetails *[]policySetFailureDetail `json:"policySetFailureDetails,omitempty"`
}

type scanDetailsResp struct {
	Data *artifactScanDetails `json:"data,omitempty"`
}

// ---- API helpers ----

func harV3URL(apiUrl, path string) string {
	return apiUrl + "/gateway/har/api/v3" + path
}

func doHAR(ctx context.Context, hc *http.Client, token, url, method string, body any, out any) error {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		bodyReader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return err
	}
	req.Header.Set("x-api-key", token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("API error %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	if out != nil && len(respBody) > 0 {
		return json.Unmarshal(respBody, out)
	}
	return nil
}

// getRegistryUUID fetches the registry via the HAR v1 API and returns its UUID.
func getRegistryUUID(ctx context.Context, hc *http.Client, apiUrl, token, accountID, orgID, projectID, registryID string) (string, error) {
	scope := accountID
	if orgID != "" {
		scope += "/" + orgID
		if projectID != "" {
			scope += "/" + projectID
		}
	}
	u, err := url.Parse(apiUrl + fmt.Sprintf("/har/api/v1/registry/%s/%s/+", scope, registryID))
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("x-api-key", token)

	resp, err := hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("registry lookup error %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var envelope struct {
		Data struct {
			Uuid *string `json:"uuid"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return "", fmt.Errorf("parsing registry response: %w", err)
	}
	if envelope.Data.Uuid == nil || *envelope.Data.Uuid == "" {
		return "", fmt.Errorf("registry %q: UUID not found in response", registryID)
	}
	return *envelope.Data.Uuid, nil
}

// ---- handler ----

func executeArtifactFirewallScanHandler(cmdCtx *cmdctx.Ctx) error {
	a := cmdCtx.Auth

	if len(cmdCtx.IdParts) < 3 {
		return fmt.Errorf("artifact:firewall_scan requires <registry/name/version>, got %q", cmdCtx.Id)
	}
	registryID := cmdCtx.IdParts[0]
	packageName := cmdCtx.IdParts[1]
	version := cmdCtx.IdParts[2]

	ctx := context.Background()
	hc := &http.Client{Timeout: 60 * time.Second}

	// 1. Look up registry UUID.
	fmt.Printf("Fetching registry details for: %s\n", registryID)
	registryUUID, err := getRegistryUUID(ctx, hc, a.APIUrl, a.PATToken, a.AccountID, a.OrgID, a.ProjectID, registryID)
	if err != nil {
		return fmt.Errorf("fetching registry: %w", err)
	}
	fmt.Printf("Registry UUID: %s\n", registryUUID)

	// 2. Initiate bulk scan evaluation.
	fmt.Printf("Initiating evaluation for %s@%s\n", packageName, version)
	evalURL := buildEvalURL(a.APIUrl, a.AccountID, a.OrgID, a.ProjectID)
	var initResp bulkEvalAcceptedResp
	if err := doHAR(ctx, hc, a.PATToken, evalURL, "POST", bulkEvalRequest{
		RegistryId: registryUUID,
		Artifacts:  []artifactScanInput{{PackageName: packageName, Version: version}},
	}, &initResp); err != nil {
		return fmt.Errorf("initiating evaluation: %w", err)
	}
	if initResp.Data == nil || initResp.Data.EvaluationId == nil {
		return fmt.Errorf("invalid response from evaluation API: missing evaluationId")
	}
	evaluationID := *initResp.Data.EvaluationId
	fmt.Printf("Evaluation ID: %s\n", evaluationID)

	// 3. Poll for completion.
	fmt.Println("Waiting for evaluation to complete...")
	statusURL := buildEvalStatusURL(a.APIUrl, evaluationID, a.AccountID, a.OrgID, a.ProjectID)
	var statusData *bulkEvalStatusData
	for i := 0; i < 120; i++ {
		var statusResp bulkEvalStatusResp
		if err := doHAR(ctx, hc, a.PATToken, statusURL, "GET", nil, &statusResp); err != nil {
			return fmt.Errorf("polling evaluation status: %w", err)
		}
		if statusResp.Data == nil || statusResp.Data.Status == nil {
			return fmt.Errorf("invalid response from evaluation status API")
		}
		switch *statusResp.Data.Status {
		case "SUCCESS":
			statusData = statusResp.Data
			goto done
		case "FAILURE":
			msg := "evaluation failed"
			if statusResp.Data.Error != nil {
				msg = *statusResp.Data.Error
			}
			return fmt.Errorf("%s", msg)
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("timeout waiting for evaluation to complete")

done:
	if statusData.Scans == nil || len(*statusData.Scans) == 0 {
		fmt.Println("No scan results returned.")
		return nil
	}

	scan := (*statusData.Scans)[0]
	scanStatus := ""
	if scan.ScanStatus != nil {
		scanStatus = *scan.ScanStatus
	}
	scanID := ""
	if scan.ScanId != nil {
		scanID = *scan.ScanId
	}

	// 4. Print summary.
	fmt.Println()
	fmt.Println("Scan Result")
	fmt.Printf("  Package:           %s\n", packageName)
	fmt.Printf("  Version:           %s\n", version)
	fmt.Printf("  Evaluation Status: %s\n", displayVal(scanStatus))
	fmt.Printf("  Evaluation ID:     %s\n", displayVal(scanID))
	fmt.Println()
	switch scanStatus {
	case "BLOCKED":
		fmt.Println("BLOCKED: This artifact version is blocked by the firewall.")
	case "WARN":
		fmt.Println("WARN: This artifact version has firewall warnings.")
	case "ALLOWED":
		fmt.Println("ALLOWED: This artifact version is allowed by the firewall.")
	}

	// 5. Fetch and print detailed scan info.
	if scanID == "" {
		return nil
	}
	fmt.Println()
	fmt.Println("Fetching detailed scan information...")
	detailURL := buildScanDetailsURL(a.APIUrl, scanID, a.AccountID)
	var detailResp scanDetailsResp
	if err := doHAR(ctx, hc, a.PATToken, detailURL, "GET", nil, &detailResp); err != nil {
		fmt.Printf("  (could not fetch scan details: %v)\n", err)
		return nil
	}
	if detailResp.Data != nil {
		printScanDetails(detailResp.Data)
	}
	return nil
}

// ---- URL builders ----

func buildEvalURL(apiUrl, accountID, orgID, projectID string) string {
	u, _ := url.Parse(harV3URL(apiUrl, "/scans/bulk-evaluate"))
	q := u.Query()
	q.Set("account_identifier", accountID)
	if orgID != "" {
		q.Set("org_identifier", orgID)
	}
	if projectID != "" {
		q.Set("project_identifier", projectID)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func buildEvalStatusURL(apiUrl, evaluationID, accountID, orgID, projectID string) string {
	u, _ := url.Parse(harV3URL(apiUrl, "/scans/bulk-evaluate/"+url.PathEscape(evaluationID)))
	q := u.Query()
	q.Set("account_identifier", accountID)
	if orgID != "" {
		q.Set("org_identifier", orgID)
	}
	if projectID != "" {
		q.Set("project_identifier", projectID)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func buildScanDetailsURL(apiUrl, scanID, accountID string) string {
	u, _ := url.Parse(harV3URL(apiUrl, "/scans/"+url.PathEscape(scanID)+"/details"))
	q := u.Query()
	q.Set("account_identifier", accountID)
	u.RawQuery = q.Encode()
	return u.String()
}

// ---- output helpers ----

func displayVal(s string) string {
	if s == "" {
		return "(not set)"
	}
	return s
}

func fmtTimestampMs(s string) string {
	if s == "" {
		return "(not set)"
	}
	ms, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return s
	}
	return time.UnixMilli(ms).Format("2006-01-02 15:04:05 MST")
}

func printScanDetails(d *artifactScanDetails) {
	fmt.Println()
	fmt.Println("Evaluation Details:")
	fmt.Println(strings.Repeat("=", 60))

	if d.LastEvaluatedAt != nil && *d.LastEvaluatedAt != "" {
		fmt.Printf("Last Evaluated: %s\n", fmtTimestampMs(*d.LastEvaluatedAt))
	}

	// Show fix info if any security violation present.
	hasSecViolation := false
	if d.PolicySetFailureDetails != nil {
		for _, ps := range *d.PolicySetFailureDetails {
			for _, f := range ps.PolicyFailureDetails {
				if f.Category == "Security" {
					hasSecViolation = true
					break
				}
			}
		}
	}
	if hasSecViolation && d.FixVersionDetails != nil {
		fv := d.FixVersionDetails
		fmt.Println()
		fmt.Println("Security Fix Information:")
		fmt.Printf("  Fix Available:   %v\n", fv.FixVersionAvailable)
		fmt.Printf("  Current Version: %s\n", fv.CurrentVersion)
		if fv.FixVersion != nil {
			fmt.Printf("  Fix Version:     %s\n", *fv.FixVersion)
		}
	}

	if d.PolicySetFailureDetails == nil || len(*d.PolicySetFailureDetails) == 0 {
		return
	}

	fmt.Println()
	fmt.Println("Policy Set Violations:")

	for psIdx, ps := range *d.PolicySetFailureDetails {
		fmt.Println()
		fmt.Printf("Policy Set %d: %s\n", psIdx+1, ps.PolicySetName)
		fmt.Printf("Policy Set Ref: %s\n", ps.PolicySetRef)
		fmt.Println(strings.Repeat("-", 60))

		for i, f := range ps.PolicyFailureDetails {
			fmt.Printf("\n  %d.%d %s\n", psIdx+1, i+1, f.Category)
			fmt.Printf("      Policy Name: %s\n", f.PolicyName)
			fmt.Printf("      Policy Ref:  %s\n", f.PolicyRef)

			switch f.Category {
			case "Security":
				var sc securityConfig
				if err := json.Unmarshal(f.Config, &sc); err == nil && len(sc.Vulnerabilities) > 0 {
					fmt.Println("\n      Vulnerabilities:")
					fmt.Printf("        %-24s %10s %15s\n", "CVE ID", "CVSS Score", "CVSS Threshold")
					fmt.Printf("        %-24s %10s %15s\n", strings.Repeat("-", 24), strings.Repeat("-", 10), strings.Repeat("-", 15))
					for _, v := range sc.Vulnerabilities {
						fmt.Printf("        %-24s %10.1f %15.1f\n", v.CveId, v.CvssScore, v.CvssThreshold)
					}
				}
			case "License":
				var lc licenseConfig
				if err := json.Unmarshal(f.Config, &lc); err == nil {
					fmt.Printf("\n      Blocked License:  %s\n", lc.BlockedLicense)
					if len(lc.AllowedLicenses) > 0 {
						fmt.Printf("      Allowed Licenses: %s\n", strings.Join(lc.AllowedLicenses, ", "))
					}
				}
			case "PackageAge":
				var pa packageAgeConfig
				if err := json.Unmarshal(f.Config, &pa); err == nil {
					fmt.Printf("\n      Published On:          %s\n", fmtTimestampMs(pa.PublishedOn))
					fmt.Printf("      Package Age Threshold: %s\n", pa.PackageAgeThreshold)
				}
			}
		}
	}
}

// ---- registry:firewall_scan (audit) handler ----

const executeRegistryFirewallScanHandlerID = "execute_registry_firewall_scan"

type dependency struct {
	Name    string
	Version string
}

type auditScanResult struct {
	PackageName string `json:"packageName"`
	Version     string `json:"version"`
	ScanID      string `json:"scanId"`
	ScanStatus  string `json:"scanStatus"`
}

func executeRegistryFirewallScanHandler(cmdCtx *cmdctx.Ctx) error {
	a := cmdCtx.Auth
	registryID := cmdCtx.Id
	filePath := cmdctx.GetString(cmdCtx.FlagValues, "file")

	ctx := context.Background()
	hc := &http.Client{Timeout: 60 * time.Second}

	// 1. Registry UUID.
	fmt.Printf("Fetching registry details for: %s\n", registryID)
	registryUUID, err := getRegistryUUID(ctx, hc, a.APIUrl, a.PATToken, a.AccountID, a.OrgID, a.ProjectID, registryID)
	if err != nil {
		return fmt.Errorf("fetching registry: %w", err)
	}
	fmt.Printf("Registry UUID: %s\n", registryUUID)

	// 2. Parse lock file.
	fmt.Printf("Parsing dependency file: %s\n", filepath.Base(filePath))
	deps, err := parseLockFile(filePath)
	if err != nil {
		return fmt.Errorf("parsing lock file: %w", err)
	}
	if len(deps) == 0 {
		fmt.Println("No dependencies found in file.")
		return nil
	}
	sort.Slice(deps, func(i, j int) bool { return deps[i].Name < deps[j].Name })
	fmt.Printf("Found %d dependencies.\n", len(deps))

	// 3. Batch evaluate (50 per batch).
	const batchSize = 50
	totalBatches := (len(deps) + batchSize - 1) / batchSize
	allResults := make([]auditScanResult, 0, len(deps))

	for i := 0; i < totalBatches; i++ {
		start := i * batchSize
		end := start + batchSize
		if end > len(deps) {
			end = len(deps)
		}
		batch := deps[start:end]

		fmt.Printf("Processing batch %d/%d (%d packages)...\n", i+1, totalBatches, len(batch))
		artifacts := make([]artifactScanInput, 0, len(batch))
		for _, d := range batch {
			artifacts = append(artifacts, artifactScanInput{PackageName: d.Name, Version: d.Version})
		}

		evalURL := buildEvalURL(a.APIUrl, a.AccountID, a.OrgID, a.ProjectID)
		var initResp bulkEvalAcceptedResp
		if err := doHAR(ctx, hc, a.PATToken, evalURL, "POST", bulkEvalRequest{
			RegistryId: registryUUID,
			Artifacts:  artifacts,
		}, &initResp); err != nil {
			return fmt.Errorf("batch %d: initiating evaluation: %w", i+1, err)
		}
		if initResp.Data == nil || initResp.Data.EvaluationId == nil {
			return fmt.Errorf("batch %d: missing evaluationId in response", i+1)
		}
		evaluationID := *initResp.Data.EvaluationId

		// Poll.
		statusURL := buildEvalStatusURL(a.APIUrl, evaluationID, a.AccountID, a.OrgID, a.ProjectID)
		var statusData *bulkEvalStatusData
		for poll := 0; poll < 120; poll++ {
			var statusResp bulkEvalStatusResp
			if err := doHAR(ctx, hc, a.PATToken, statusURL, "GET", nil, &statusResp); err != nil {
				return fmt.Errorf("batch %d: polling status: %w", i+1, err)
			}
			if statusResp.Data == nil || statusResp.Data.Status == nil {
				return fmt.Errorf("batch %d: invalid status response", i+1)
			}
			switch *statusResp.Data.Status {
			case "SUCCESS":
				statusData = statusResp.Data
				goto batchDone
			case "FAILURE":
				msg := fmt.Sprintf("batch %d evaluation failed", i+1)
				if statusResp.Data.Error != nil {
					msg = *statusResp.Data.Error
				}
				return fmt.Errorf("%s", msg)
			}
			time.Sleep(2 * time.Second)
		}
		return fmt.Errorf("batch %d: timeout waiting for evaluation", i+1)

	batchDone:
		if statusData.Scans != nil {
			for _, s := range *statusData.Scans {
				r := auditScanResult{}
				if s.PackageName != nil {
					r.PackageName = *s.PackageName
				}
				if s.Version != nil {
					r.Version = *s.Version
				}
				if s.ScanId != nil {
					r.ScanID = *s.ScanId
				}
				if s.ScanStatus != nil {
					r.ScanStatus = *s.ScanStatus
				}
				allResults = append(allResults, r)
			}
		}
	}

	// 4. Display results.
	sort.Slice(allResults, func(i, j int) bool { return allResults[i].PackageName < allResults[j].PackageName })
	return printAuditResults(cmdCtx, allResults)
}

func printAuditResults(cmdCtx *cmdctx.Ctx, results []auditScanResult) error {
	var blocked, warn, allowed, unknown int
	for _, r := range results {
		switch r.ScanStatus {
		case "BLOCKED":
			blocked++
		case "WARN":
			warn++
		case "ALLOWED":
			allowed++
		default:
			unknown++
		}
	}

	fmt.Println()
	fmt.Printf("Scan Results (%d dependencies):\n", len(results))
	if blocked > 0 {
		fmt.Printf("  BLOCKED:  %d\n", blocked)
	}
	if warn > 0 {
		fmt.Printf("  WARN:     %d\n", warn)
	}
	if allowed > 0 {
		fmt.Printf("  ALLOWED:  %d\n", allowed)
	}
	if unknown > 0 {
		fmt.Printf("  UNKNOWN:  %d\n", unknown)
	}
	fmt.Println()

	// JSON output when requested.
	if cmdctx.GetString(cmdCtx.FlagValues, "format") == "json" {
		b, err := json.MarshalIndent(results, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(b))
		return nil
	}

	// Table.
	tw := table.NewWriter()
	tw.SetOutputMirror(os.Stdout)
	tw.AppendHeader(table.Row{"Package", "Version", "Status"})
	for _, r := range results {
		tw.AppendRow(table.Row{r.PackageName, r.Version, r.ScanStatus})
	}
	tw.Render()
	return nil
}

// ---- lock file parsers (ported from old-hc fw_audit.go) ----

func parseLockFile(filePath string) ([]dependency, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("reading file: %w", err)
	}
	name := filepath.Base(filePath)
	switch {
	case name == "package.json":
		return parsePackageJSON(data)
	case strings.HasSuffix(name, "package-lock.json"):
		return parsePackageLock(data)
	case strings.HasSuffix(name, "pnpm-lock.yaml"):
		return parsePnpmLock(data)
	case strings.HasSuffix(name, "yarn.lock"):
		return parseYarnLock(data)
	case strings.HasSuffix(name, "requirements.txt"):
		return parseRequirementsTxt(data)
	case strings.HasSuffix(name, "pyproject.toml"):
		return parsePyProjectToml(data)
	case name == "Pipfile.lock":
		return parsePipfileLock(data)
	case name == "poetry.lock":
		return parsePoetryLock(data)
	case strings.HasSuffix(name, "pom.xml"):
		return parsePomXml(data)
	case strings.HasSuffix(name, "build.gradle"), strings.HasSuffix(name, "build.gradle.kts"):
		return parseBuildGradle(data)
	default:
		return nil, fmt.Errorf("unsupported file %q (supported: package.json, package-lock.json, pnpm-lock.yaml, yarn.lock, requirements.txt, pyproject.toml, Pipfile.lock, poetry.lock, pom.xml, build.gradle)", name)
	}
}

func parsePackageJSON(data []byte) ([]dependency, error) {
	var pj struct {
		Dependencies         map[string]string `json:"dependencies"`
		DevDependencies      map[string]string `json:"devDependencies"`
		PeerDependencies     map[string]string `json:"peerDependencies"`
		OptionalDependencies map[string]string `json:"optionalDependencies"`
	}
	if err := json.Unmarshal(data, &pj); err != nil {
		return nil, fmt.Errorf("parsing package.json: %w", err)
	}
	seen := map[string]bool{}
	var deps []dependency
	cleanVer := func(v string) string {
		v = strings.TrimSpace(v)
		for _, pfx := range []string{"^", "~", ">=", ">", "<=", "<", "="} {
			v = strings.TrimPrefix(v, pfx)
		}
		if i := strings.Index(v, " "); i != -1 {
			v = v[:i]
		}
		return v
	}
	for _, m := range []map[string]string{pj.Dependencies, pj.DevDependencies, pj.PeerDependencies, pj.OptionalDependencies} {
		for name, ver := range m {
			if seen[name] {
				continue
			}
			seen[name] = true
			deps = append(deps, dependency{Name: name, Version: cleanVer(ver)})
		}
	}
	return deps, nil
}

func parsePackageLock(data []byte) ([]dependency, error) {
	var lf struct {
		LockfileVersion int `json:"lockfileVersion"`
		Dependencies    map[string]struct {
			Version string `json:"version"`
		} `json:"dependencies"`
		Packages map[string]struct {
			Version string `json:"version"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(data, &lf); err != nil {
		return nil, fmt.Errorf("parsing package-lock.json: %w", err)
	}
	seen := map[string]bool{}
	var deps []dependency
	if lf.LockfileVersion >= 2 && len(lf.Packages) > 0 {
		for path, pkg := range lf.Packages {
			if path == "" {
				continue
			}
			name := strings.TrimPrefix(path, "node_modules/")
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			deps = append(deps, dependency{Name: name, Version: pkg.Version})
		}
	} else {
		for name, dep := range lf.Dependencies {
			if seen[name] {
				continue
			}
			seen[name] = true
			deps = append(deps, dependency{Name: name, Version: dep.Version})
		}
	}
	return deps, nil
}

func parsePnpmLock(data []byte) ([]dependency, error) {
	var lf struct {
		Packages map[string]struct{} `yaml:"packages"`
	}
	if err := yaml.Unmarshal(data, &lf); err != nil {
		return nil, fmt.Errorf("parsing pnpm-lock.yaml: %w", err)
	}
	seen := map[string]bool{}
	var deps []dependency
	for pkgPath := range lf.Packages {
		parts := strings.Split(pkgPath, "/")
		var name, version string
		if strings.HasPrefix(pkgPath, "@") && len(parts) >= 2 {
			name = parts[0] + "/" + parts[1]
			if len(parts) > 2 {
				version = strings.TrimPrefix(parts[2], "@")
			}
		} else if len(parts) >= 1 {
			np := strings.Split(parts[0], "@")
			if len(np) >= 2 {
				name, version = np[0], np[1]
			} else {
				name = parts[0]
			}
		}
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		deps = append(deps, dependency{Name: name, Version: version})
	}
	return deps, nil
}

func parseYarnLock(data []byte) ([]dependency, error) {
	seen := map[string]bool{}
	var deps []dependency
	var curPkg, curVer string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.HasPrefix(line, " ") && strings.Contains(line, "@") && strings.HasSuffix(line, ":") {
			pkgLine := strings.TrimSuffix(line, ":")
			first := strings.TrimSpace(strings.Split(pkgLine, ",")[0])
			if at := strings.LastIndex(first, "@"); at > 0 {
				curPkg = strings.Trim(first[:at], "\"")
			} else {
				curPkg = strings.Trim(first, "\"")
			}
			curVer = ""
		} else if strings.HasPrefix(line, "version ") && curPkg != "" {
			curVer = strings.Trim(strings.TrimPrefix(line, "version "), "\"")
			if !seen[curPkg] {
				seen[curPkg] = true
				deps = append(deps, dependency{Name: curPkg, Version: curVer})
			}
			curPkg = ""
		}
	}
	return deps, nil
}

func parseRequirementsTxt(data []byte) ([]dependency, error) {
	seen := map[string]bool{}
	var deps []dependency
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "-") ||
			strings.HasPrefix(line, "git+") || strings.HasPrefix(line, "http") {
			continue
		}
		var name, ver string
		for _, sep := range []string{"==", ">=", "<=", "~=", ">", "<"} {
			if strings.Contains(line, sep) {
				parts := strings.SplitN(line, sep, 2)
				name = strings.TrimSpace(parts[0])
				ver = strings.TrimSpace(parts[1])
				if i := strings.Index(ver, ","); i != -1 {
					ver = ver[:i]
				}
				if i := strings.Index(ver, ";"); i != -1 {
					ver = ver[:i]
				}
				ver = strings.TrimSpace(ver)
				break
			}
		}
		if name == "" {
			name = line
			ver = "latest"
		}
		if i := strings.Index(name, "["); i != -1 {
			name = name[:i]
		}
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		deps = append(deps, dependency{Name: name, Version: ver})
	}
	return deps, nil
}

func parsePyProjectToml(data []byte) ([]dependency, error) {
	seen := map[string]bool{}
	var deps []dependency
	lines := strings.Split(string(data), "\n")
	inDeps, inOptional, inPoetry := false, false, false
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "[") {
			inDeps, inOptional, inPoetry = false, false, false
			if t == "[tool.poetry.dependencies]" {
				inPoetry = true
			} else if strings.HasPrefix(t, "[project.optional-dependencies") {
				inOptional = true
			}
			continue
		}
		if t == "dependencies = [" {
			inDeps = true
			continue
		}
		if t == "]" {
			inDeps, inOptional = false, false
			continue
		}
		var name, ver string
		if inPoetry {
			if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "python") {
				continue
			}
			if strings.Contains(t, "=") {
				parts := strings.SplitN(t, "=", 2)
				name = strings.TrimSpace(parts[0])
				vp := strings.TrimSpace(parts[1])
				vp = strings.Trim(vp, "\"'^~>=<")
				if strings.HasPrefix(vp, "{") {
					ver = "latest"
					if i := strings.Index(vp, "version"); i != -1 {
						sub := vp[i:]
						if qi := strings.Index(sub, "\""); qi != -1 {
							if qi2 := strings.Index(sub[qi+1:], "\""); qi2 != -1 {
								ver = sub[qi+1 : qi+1+qi2]
							}
						}
					}
				} else {
					ver = strings.Trim(vp, "\"'^~>=<,}")
				}
			}
		} else if inDeps || inOptional {
			if t == "" || strings.HasPrefix(t, "#") {
				continue
			}
			dl := strings.Trim(t, "\",")
			for _, sep := range []string{"==", ">=", "~="} {
				if strings.Contains(dl, sep) {
					parts := strings.SplitN(dl, sep, 2)
					name = strings.TrimSpace(parts[0])
					ver = strings.TrimSpace(parts[1])
					if i := strings.Index(ver, ","); i != -1 {
						ver = ver[:i]
					}
					break
				}
			}
			if name == "" {
				name = dl
				ver = "latest"
			}
			if i := strings.Index(ver, ";"); i != -1 {
				ver = strings.TrimSpace(ver[:i])
			}
			ver = strings.Trim(ver, "\"',")
		}
		if i := strings.Index(name, "["); i != -1 {
			name = name[:i]
		}
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		deps = append(deps, dependency{Name: name, Version: ver})
	}
	return deps, nil
}

func parsePipfileLock(data []byte) ([]dependency, error) {
	var lf struct {
		Default map[string]struct {
			Version string `json:"version"`
		} `json:"default"`
		Develop map[string]struct {
			Version string `json:"version"`
		} `json:"develop"`
	}
	if err := json.Unmarshal(data, &lf); err != nil {
		return nil, fmt.Errorf("parsing Pipfile.lock: %w", err)
	}
	seen := map[string]bool{}
	var deps []dependency
	addPipfile := func(m map[string]struct {
		Version string `json:"version"`
	}) {
		for name, pkg := range m {
			if seen[name] {
				continue
			}
			seen[name] = true
			deps = append(deps, dependency{Name: name, Version: strings.TrimPrefix(pkg.Version, "==")})
		}
	}
	addPipfile(lf.Default)
	addPipfile(lf.Develop)
	return deps, nil
}

func parsePoetryLock(data []byte) ([]dependency, error) {
	seen := map[string]bool{}
	var deps []dependency
	var curName, curVer string
	inPkg := false
	for _, line := range strings.Split(string(data), "\n") {
		t := strings.TrimSpace(line)
		if t == "[[package]]" {
			if curName != "" && !seen[curName] {
				seen[curName] = true
				deps = append(deps, dependency{Name: curName, Version: curVer})
			}
			inPkg, curName, curVer = true, "", ""
			continue
		}
		if inPkg {
			if strings.HasPrefix(t, "name = ") {
				curName = strings.Trim(strings.TrimPrefix(t, "name = "), "\"")
			} else if strings.HasPrefix(t, "version = ") {
				curVer = strings.Trim(strings.TrimPrefix(t, "version = "), "\"")
			} else if strings.HasPrefix(t, "[") && t != "[[package]]" {
				inPkg = false
			}
		}
	}
	if curName != "" && !seen[curName] {
		deps = append(deps, dependency{Name: curName, Version: curVer})
	}
	return deps, nil
}

func parsePomXml(data []byte) ([]dependency, error) {
	type pomDep struct {
		GroupId    string `xml:"groupId"`
		ArtifactId string `xml:"artifactId"`
		Version    string `xml:"version"`
	}
	var pom struct {
		XMLName      xml.Name `xml:"project"`
		Dependencies struct {
			Dependency []pomDep `xml:"dependency"`
		} `xml:"dependencies"`
		DependencyManagement struct {
			Dependencies struct {
				Dependency []pomDep `xml:"dependency"`
			} `xml:"dependencies"`
		} `xml:"dependencyManagement"`
	}
	if err := xml.Unmarshal(data, &pom); err != nil {
		return nil, fmt.Errorf("parsing pom.xml: %w", err)
	}
	props := map[string]string{}
	propsRe := regexp.MustCompile(`<([a-zA-Z0-9._-]+)>([^<]+)</[a-zA-Z0-9._-]+>`)
	propsSection := regexp.MustCompile(`(?s)<properties>(.*?)</properties>`)
	if m := propsSection.FindSubmatch(data); len(m) > 1 {
		for _, pm := range propsRe.FindAllSubmatch(m[1], -1) {
			if len(pm) >= 3 {
				props[string(pm[1])] = string(pm[2])
			}
		}
	}
	resolveVer := func(v string) string {
		if strings.HasPrefix(v, "${") && strings.HasSuffix(v, "}") {
			if r, ok := props[v[2:len(v)-1]]; ok {
				return r
			}
		}
		return v
	}
	seen := map[string]bool{}
	var deps []dependency
	all := append(pom.Dependencies.Dependency, pom.DependencyManagement.Dependencies.Dependency...)
	for _, d := range all {
		if d.GroupId == "" || d.ArtifactId == "" {
			continue
		}
		name := d.GroupId + ":" + d.ArtifactId
		ver := resolveVer(d.Version)
		if ver == "" {
			ver = "latest"
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		deps = append(deps, dependency{Name: name, Version: ver})
	}
	return deps, nil
}

func parseBuildGradle(data []byte) ([]dependency, error) {
	content := string(data)
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`(?:implementation|api|compile|runtimeOnly|testImplementation|testCompile|compileOnly)\s*\(\s*['"]([^'"]+):([^'"]+):([^'"]+)['"]\s*\)`),
		regexp.MustCompile(`(?:implementation|api|compile|runtimeOnly|testImplementation|testCompile|compileOnly)\s*\(\s*group:\s*['"]([^'"]+)['"]\s*,\s*name:\s*['"]([^'"]+)['"]\s*,\s*version:\s*['"]([^'"]+)['"]\s*\)`),
		regexp.MustCompile(`(?:implementation|api|compile|runtimeOnly|testImplementation|testCompile|compileOnly)\s+['"]([^'"]+):([^'"]+):([^'"]+)['"]`),
		regexp.MustCompile(`(?:implementation|api|compile|runtimeOnly|testImplementation|testCompile|compileOnly)\s*\(\s*"([^"]+):([^"]+):([^"]+)"\s*\)`),
	}
	seen := map[string]bool{}
	var deps []dependency
	for _, p := range patterns {
		for _, m := range p.FindAllStringSubmatch(content, -1) {
			if len(m) < 4 {
				continue
			}
			name := m[1] + ":" + m[2]
			if seen[name] {
				continue
			}
			seen[name] = true
			deps = append(deps, dependency{Name: name, Version: m[3]})
		}
	}
	return deps, nil
}
