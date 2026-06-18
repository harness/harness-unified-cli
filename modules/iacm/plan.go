// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package iacm

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/harness/harness-cli/pkg/auth"
	"github.com/harness/harness-cli/pkg/cmdctx"
	"github.com/harness/harness-cli/pkg/console"
	"github.com/harness/harness-cli/pkg/logstream"
)

const executeWorkspaceHandlerID = "execute_workspace"

// workspaceConfig mirrors the .harness/workspace.yaml file.
type workspaceConfig struct {
	Org       string `yaml:"org_id"`
	Project   string `yaml:"project_id"`
	Workspace string `yaml:"workspace_id"`
}

type workspace struct {
	Account          string                              `json:"account"`
	Org              string                              `json:"org"`
	Project          string                              `json:"project"`
	Identifier       string                              `json:"identifier"`
	RepositoryPath   string                              `json:"repository_path,omitempty"`
	DefaultPipelines map[string]*defaultPipelineOverride `json:"default_pipelines"`
}

type defaultPipelineOverride struct {
	ProjectPipeline   *string `json:"project_pipeline,omitempty"`
	WorkspacePipeline *string `json:"workspace_pipeline,omitempty"`
}

type remoteExecution struct {
	ID                   string `json:"id"`
	PipelineExecutionID  string `json:"pipeline_execution_id"`
	PipelineExecutionURL string `json:"pipeline_execution_url"`
}

type iacmAPIError struct {
	Message string `json:"message"`
}

func (e *iacmAPIError) Error() string { return e.Message }

// ---- handler ----

func executeWorkspaceHandler(ctx *cmdctx.Ctx) error {
	a := ctx.Auth

	workspaceID := ctx.Id
	orgID := a.OrgID
	projectID := a.ProjectID

	if workspaceID == "" {
		cfg, err := loadWorkspaceConfig()
		if err != nil {
			return err
		}
		workspaceID = cfg.Workspace
		if cfg.Org != "" {
			orgID = cfg.Org
		}
		if cfg.Project != "" {
			projectID = cfg.Project
		}
	}
	if workspaceID == "" {
		return errors.New("workspace-id is required (pass as argument or set in .harness/workspace.yaml)")
	}

	targets := cmdctx.GetStringSlice(ctx.FlagValues, "target")
	replacements := cmdctx.GetStringSlice(ctx.FlagValues, "replace")
	force := cmdctx.GetBool(ctx.FlagValues, "force")

	bgCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigs
		fmt.Println("\nInterrupted. Cleaning up...")
		cancel()
	}()

	hc := &http.Client{Timeout: 60 * time.Second}
	return executePlan(bgCtx, ctx, hc, a, orgID, projectID, workspaceID, targets, replacements, force)
}

func loadWorkspaceConfig() (*workspaceConfig, error) {
	wd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("getting working directory: %w", err)
	}
	data, err := os.ReadFile(filepath.Join(wd, ".harness", "workspace.yaml"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errors.New("workspace not found: pass a <workspace-id> argument or add .harness/workspace.yaml")
		}
		return nil, fmt.Errorf("reading workspace config: %w", err)
	}
	var cfg workspaceConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing workspace config: %w", err)
	}
	return &cfg, nil
}

func executePlan(
	bgCtx context.Context,
	cmdCtx *cmdctx.Ctx,
	hc *http.Client,
	a *auth.ResolvedAuth,
	orgID, projectID, workspaceID string,
	targets, replacements []string,
	force bool,
) error {
	fmt.Println("Fetching workspace information...")
	ws, err := getWorkspace(bgCtx, hc, a, orgID, projectID, workspaceID)
	if err != nil {
		return fmt.Errorf("fetching workspace: %w", err)
	}
	fmt.Printf("Workspace found: %s\n", ws.Identifier)

	defaultPipeline, err := getDefaultPipeline(ws.DefaultPipelines)
	if err != nil {
		return err
	}
	fmt.Printf("Default pipeline: %s\n", defaultPipeline)

	wd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting working directory: %w", err)
	}
	repoRoot, warning, err := resolveRepoRoot(wd, ws)
	if err != nil {
		return err
	}
	fmt.Println(warning)

	if !force && !console.PromptYesNo("Do you want to continue?") {
		return errors.New("cancelled")
	}

	fmt.Println("Zipping source code...")
	zipData, err := zipSourceCode(repoRoot)
	if err != nil {
		return fmt.Errorf("zipping source code: %w", err)
	}
	fmt.Printf("Source code zipped (%d bytes)\n", len(zipData))

	customArgs := map[string][]string{}
	if len(targets) > 0 {
		customArgs["target"] = targets
	}
	if len(replacements) > 0 {
		customArgs["replace"] = replacements
	}

	fmt.Println("Creating remote execution...")
	exec, err := createRemoteExecution(bgCtx, hc, a, orgID, projectID, workspaceID, customArgs)
	if err != nil {
		return fmt.Errorf("creating remote execution: %w", err)
	}

	fmt.Println("Uploading source code...")
	exec, err = uploadRemoteExecution(bgCtx, hc, a, orgID, projectID, workspaceID, exec.ID, zipData)
	if err != nil {
		return fmt.Errorf("uploading source code: %w", err)
	}

	fmt.Println("Triggering pipeline execution...")
	exec, err = triggerRemoteExecution(bgCtx, hc, a, orgID, projectID, workspaceID, exec.ID)
	if err != nil {
		return fmt.Errorf("triggering execution: %w", err)
	}
	execURL := exec.PipelineExecutionURL
	if execURL != "" && !strings.HasPrefix(execURL, "http") {
		execURL = "https://" + execURL
	}
	fmt.Printf("Pipeline execution: %s\n", execURL)

	fmt.Println("\n=== Pipeline Execution Logs ===")
	return logstream.FollowMulti(cmdCtx, hc, exec.PipelineExecutionID, "", "", map[string]bool{
		"IACMIntegrationStageStepPMS": true,
		"IACMPrepareExecution":        true,
	})
}

// ---- API helpers ----

func apiURL(a *auth.ResolvedAuth, path string) string {
	return a.APIUrl + path
}

func doIACM(ctx context.Context, hc *http.Client, a *auth.ResolvedAuth, method, path string, body any, out any) error {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		bodyReader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, apiURL(a, path), bodyReader)
	if err != nil {
		return err
	}
	req.Header.Set("x-api-key", a.PATToken)
	req.Header.Set("harness-account", a.AccountID)
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
		var apiErr iacmAPIError
		if json.Unmarshal(respBody, &apiErr) == nil && apiErr.Message != "" {
			return fmt.Errorf("API error %d: %s", resp.StatusCode, apiErr.Message)
		}
		return fmt.Errorf("API error %d: %s", resp.StatusCode, string(respBody))
	}
	if out != nil && len(respBody) > 0 {
		return json.Unmarshal(respBody, out)
	}
	return nil
}

func getWorkspace(ctx context.Context, hc *http.Client, a *auth.ResolvedAuth, org, project, workspaceID string) (*workspace, error) {
	path := fmt.Sprintf("/gateway/iacm/api/orgs/%s/projects/%s/workspaces/%s", org, project, workspaceID)
	var ws workspace
	if err := doIACM(ctx, hc, a, "GET", path, nil, &ws); err != nil {
		return nil, err
	}
	return &ws, nil
}

func createRemoteExecution(ctx context.Context, hc *http.Client, a *auth.ResolvedAuth, org, project, workspaceID string, customArgs map[string][]string) (*remoteExecution, error) {
	path := fmt.Sprintf("/gateway/iacm/api/orgs/%s/projects/%s/workspaces/%s/remote-executions", org, project, workspaceID)
	body := map[string]any{"custom_arguments": customArgs}
	var exec remoteExecution
	if err := doIACM(ctx, hc, a, "POST", path, body, &exec); err != nil {
		return nil, err
	}
	return &exec, nil
}

func uploadRemoteExecution(ctx context.Context, hc *http.Client, a *auth.ResolvedAuth, org, project, workspaceID, execID string, data []byte) (*remoteExecution, error) {
	path := fmt.Sprintf("/gateway/iacm/api/orgs/%s/projects/%s/workspaces/%s/remote-executions/%s/upload", org, project, workspaceID, execID)

	h := sha256.New()
	h.Write(data)
	checksum := fmt.Sprintf("sha256=%x", h.Sum(nil))

	req, err := http.NewRequestWithContext(ctx, "POST", apiURL(a, path), bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-api-key", a.PATToken)
	req.Header.Set("harness-account", a.AccountID)
	req.Header.Set("Content-Digest", checksum)
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("upload error %d: %s", resp.StatusCode, string(body))
	}
	var exec remoteExecution
	if err := json.Unmarshal(body, &exec); err != nil {
		return nil, err
	}
	return &exec, nil
}

func triggerRemoteExecution(ctx context.Context, hc *http.Client, a *auth.ResolvedAuth, org, project, workspaceID, execID string) (*remoteExecution, error) {
	path := fmt.Sprintf("/gateway/iacm/api/orgs/%s/projects/%s/workspaces/%s/remote-executions/%s/execute", org, project, workspaceID, execID)
	var exec remoteExecution
	if err := doIACM(ctx, hc, a, "POST", path, nil, &exec); err != nil {
		return nil, err
	}
	return &exec, nil
}

// ---- workspace helpers ----

func getDefaultPipeline(pipelines map[string]*defaultPipelineOverride) (string, error) {
	dp, ok := pipelines["plan"]
	if !ok {
		return "", errors.New("workspace has no default pipeline configured for 'plan'")
	}
	if dp.WorkspacePipeline != nil {
		return *dp.WorkspacePipeline, nil
	}
	if dp.ProjectPipeline != nil {
		return *dp.ProjectPipeline, nil
	}
	return "", errors.New("workspace has no default pipeline configured for 'plan'")
}

func resolveRepoRoot(wd string, ws *workspace) (string, string, error) {
	if ws.RepositoryPath == "" {
		return wd, fmt.Sprintf("No folder path configured; uploading: %s", wd), nil
	}
	repoPath := filepath.Clean(ws.RepositoryPath)
	cleanWD := filepath.Clean(wd)

	if strings.HasSuffix(cleanWD, repoPath) {
		root := filepath.Clean(strings.TrimSuffix(cleanWD, ws.RepositoryPath))
		if _, err := os.Stat(root); err != nil {
			return "", "", fmt.Errorf("finding repo root: %w", err)
		}
		return root, fmt.Sprintf("Folder path: %s\nUploading: %s", repoPath, root), nil
	}
	candidate := filepath.Join(wd, repoPath)
	if _, err := os.Stat(candidate); os.IsNotExist(err) {
		return "", "", fmt.Errorf("configured folder path %q not found in current directory", repoPath)
	}
	return wd, fmt.Sprintf("Folder path: %s\nUploading: %s", repoPath, wd), nil
}

func zipSourceCode(root string) ([]byte, error) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	err := filepath.Walk(root, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if fi.IsDir() && fi.Name() == ".git" {
			return filepath.SkipDir
		}
		if fi.IsDir() {
			return nil
		}
		hdr := &tar.Header{
			Name:    rel,
			Size:    fi.Size(),
			Mode:    int64(fi.Mode()),
			ModTime: fi.ModTime(),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("creating archive: %w", err)
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
