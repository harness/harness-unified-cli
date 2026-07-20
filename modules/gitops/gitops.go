// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package gitops

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/harness/cli/pkg/client"
	"github.com/harness/cli/pkg/cmdctx"
	"github.com/harness/cli/pkg/registry"
)

const installWorkflowID = "gitops_agent_install"

// ModuleInit registers gitops workflows. Commands are declared in gitops.spec.yaml.
func ModuleInit(reg registry.ModuleRegistrar) {
	reg.RegisterWorkflow(installWorkflowID, executeAgentInstall)
}

// installInput mirrors the subset of v1AgentYamlQuery exposed via -f install.yaml.
type installInput struct {
	Namespace                  string         `yaml:"namespace"`
	DisasterRecoveryIdentifier string         `yaml:"disasterRecoveryIdentifier"`
	SkipCrds                   bool           `yaml:"skipCrds"`
	CaData                     string         `yaml:"caData"`
	PrivateKey                 string         `yaml:"privateKey"`
	Proxy                      map[string]any `yaml:"proxy"`
	ArgocdSettings             map[string]any `yaml:"argocdSettings"`
}

// executeAgentInstall implements "execute gitops_agent:install". It fetches the
// Helm override.yaml (or, with --method yaml, the plain k8s manifest) for an
// existing agent and prints the commands to install it. It never touches a
// cluster itself. This is a workflow (not a spec-only endpoint) because these
// endpoints respond with a literal YAML string body, which client.DoRequest's
// unconditional json.Unmarshal cannot decode — client.DoRaw is required instead.
func executeAgentInstall(ctx *cmdctx.Ctx) error {
	agentID := ctx.Id
	if agentID == "" {
		return errors.New("execute gitops_agent:install requires a positional <id> argument")
	}
	method := cmdctx.GetString(ctx.FlagValues, "method")
	if method == "" {
		method = "helm"
	}
	if method != "helm" && method != "yaml" {
		return fmt.Errorf("invalid --method %q: must be \"helm\" or \"yaml\"", method)
	}

	a := *ctx.Auth
	switch ctx.Level {
	case "org":
		a.ProjectID = ""
	case "account":
		a.OrgID, a.ProjectID = "", ""
	}
	qp := map[string]string{"accountIdentifier": a.AccountID, "orgIdentifier": a.OrgID, "projectIdentifier": a.ProjectID}

	c := client.New(ctx)
	agentResp, _, err := c.Get(fmt.Sprintf("/gitops/api/v1/agents/%s", agentID), qp)
	if err != nil {
		return fmt.Errorf("fetching agent %q: %w (has it been created with 'harness create gitops_agent'?)", agentID, err)
	}
	namespace := ""
	if m, ok := agentResp.(map[string]any); ok {
		if md, ok := m["metadata"].(map[string]any); ok {
			namespace, _ = md["namespace"].(string)
		}
	}

	var input installInput
	if filePath := cmdctx.GetString(ctx.FlagValues, "file"); filePath != "" {
		body, err := cmdctx.SlurpInputFile(ctx.FlagValues)
		if err != nil {
			return err
		}
		if err := yaml.Unmarshal([]byte(body), &input); err != nil {
			return fmt.Errorf("parsing -f install file: %w", err)
		}
		if input.Namespace != "" {
			namespace = input.Namespace
		}
	}
	if namespace == "" {
		return fmt.Errorf("namespace is required: set it via -f install.yaml, or it must already be set on the agent (see 'harness get gitops_agent %s')", agentID)
	}

	reqBody := map[string]any{
		"accountIdentifier": a.AccountID,
		"orgIdentifier":     a.OrgID,
		"projectIdentifier": a.ProjectID,
		"agentIdentifier":   agentID,
		"namespace":         namespace,
	}
	if input.SkipCrds {
		reqBody["skipCrds"] = true
	}
	for k, v := range map[string]string{"disasterRecoveryIdentifier": input.DisasterRecoveryIdentifier, "caData": input.CaData, "privateKey": input.PrivateKey} {
		if v != "" {
			reqBody[k] = v
		}
	}
	if input.Proxy != nil {
		reqBody["proxy"] = input.Proxy
	}
	if input.ArgocdSettings != nil {
		reqBody["argocdSettings"] = input.ArgocdSettings
	}
	outPath := cmdctx.GetString(ctx.FlagValues, "output_file")
	if outPath == "" {
		return fmt.Errorf("output file is required: set it via --output_file")
	}
	if !strings.HasSuffix(outPath, ".yaml") && !strings.HasSuffix(outPath, ".yaml.txt") {
		return fmt.Errorf("output file must end with .yaml or .yaml.txt")
	}
	
	path := fmt.Sprintf("/gitops/api/v1/agents/%s/helm-overrides", agentID)

	resp, err := c.DoRaw(client.Request{Method: "POST", Path: path, Body: reqBody})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading API response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("API error %d: %s", resp.StatusCode, client.APIErrorMessage(resp.StatusCode, respBody))
	}
	// Despite the OpenAPI doc annotating these routes as application/yaml, the
	// gRPC-gateway actually wraps the string response as JSON: {"value": "..."}.
	var wrapped struct {
		Value string `json:"value"`
	}
	content := respBody
	if json.Unmarshal(respBody, &wrapped) == nil && wrapped.Value != "" {
		content = []byte(wrapped.Value)
	}
	if err := os.WriteFile(outPath, content, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", outPath, err)
	}

	fmt.Printf("\nWrote %s install artifact to %s\n\nInstall the agent with:\n", method, outPath)
	fmt.Println(" Connect to your Kubernetes cluster and install the agent with:")
	if method == "helm" {
		fmt.Println("  helm repo add gitops-agent https://harness.github.io/gitops-helm/")
		fmt.Println("  helm repo update gitops-agent")
		fmt.Printf("  helm install argocd gitops-agent/gitops-helm --values %s --namespace %s\n", outPath, namespace)
	} else {
		fmt.Printf("  kubectl apply -f %s -n %s\n", outPath, namespace)
	}
	fmt.Printf("\nAfter installing, check status with: harness get gitops_agent %s\n", agentID)
	return nil
}
