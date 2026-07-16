// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package hbase

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/google/uuid"
)

// RunID is a UUID generated once at process startup. It is sent as
// X-Harness-CLI-Run-ID on every outgoing API call to correlate all
// requests from a single CLI invocation.
var RunID = uuid.New().String()

// overridden at build time via ldflags: -X github.com/harness/cli/pkg/hbase.Version=x.y.z
var Version = "0.1.0-dev"

// overridden at build time via ldflags: -X github.com/harness/cli/pkg/hbase.BuildTime=yyyymmddhhmmZ
var BuildTime = ""

// TimeoutExitCode is the exit code used when a command is killed by --timeout.
const TimeoutExitCode = 124

const (
	HarnessHome         = "~/.harness"
	ConfigFileName      = "config.yaml"
	CredentialsFileName = "credentials"

	// EnvCheckSpecs triggers spec validation mode when set to "1".
	EnvCheckSpecs = "HARNESS_CHECKSPECS"

	// EnvDebugCompletion enables debug logging for completion invocations, writing to CompletionDebugLogFile.
	EnvDebugCompletion     = "HARNESS_DEBUG_COMPLETION"
	CompletionDebugLogFile = "/tmp/harness-completion-debug.log"

	// EnvPipelineID is set by the Harness platform when running inside a pipeline.
	EnvPipelineID = "HARNESS_PIPELINEID"

	// EnvNoUpdateCheck disables the background update check when set to "1".
	EnvNoUpdateCheck = "HARNESS_NO_UPDATE_CHECK"

	// EnvNoTelemetry disables all usage telemetry when set to "1".
	EnvNoTelemetry = "HARNESS_NO_TELEMETRY"

	// EnvInstallType identifies how the CLI was installed (e.g. "script"),
	// set by the installer before invoking --post-install. See
	// [telemetry.ResolveInstallType] for the whitelist and default.
	EnvInstallType = "HARNESS_INSTALL_TYPE"

	// EnvLogFile redirects all log output to the specified file path.
	EnvLogFile = "HARNESS_CLI_LOGFILE"

	// Env var names for env-var auth mode.
	EnvAPIKey      = "HARNESS_API_KEY"
	EnvAPIJWT      = "HARNESS_API_JWT"
	EnvAccount     = "HARNESS_ACCOUNT"
	EnvAPIURL      = "HARNESS_API_URL"
	EnvOrg         = "HARNESS_ORG"
	EnvProject     = "HARNESS_PROJECT"
	EnvRegistryURL = "HARNESS_REGISTRY_URL"
	EnvProfile     = "HARNESS_PROFILE"

	// EnvSSOAuthServerURL overrides the SSO authorization server base URL
	// (default https://id.harness.io).
	EnvSSOAuthServerURL = "HARNESS_SSO_AUTH_SERVER_URL"

	// EnvMCPBaseURL overrides the MCP gateway base URL
	// (default https://mcp.harness.io/cli).
	EnvMCPBaseURL = "HARNESS_MCP_BASE_URL"

	// Defaults applied when env vars are not set.
	DefaultAPIURL      = "https://app.harness.io"
	DefaultRegistryURL = "https://pkg.harness.io"
)

func GetCredentialsFilePath() string {
	return ExpandHomeDir(filepath.Join(HarnessHome, CredentialsFileName))
}

// IsDev reports whether this is a development build (Version ends with "-dev").
func IsDev() bool {
	return strings.HasSuffix(Version, "-dev")
}

// IsPipelineExecution reports whether the CLI is running as a step inside a Harness pipeline.
func IsPipelineExecution() bool {
	return os.Getenv(EnvPipelineID) != ""
}

func GetHarnessHomeDir() string {
	return ExpandHomeDir(HarnessHome)
}

func GetConfigFilePath() string {
	return ExpandHomeDir(filepath.Join(HarnessHome, ConfigFileName))
}

// EnsureHarnessHome creates ~/.harness with 0700 permissions if it does not exist.
// Returns an error if the directory cannot be created or if the path exists but is not a directory.
func EnsureHarnessHome() error {
	dir := GetHarnessHomeDir()
	info, err := os.Stat(dir)
	if os.IsNotExist(err) {
		if mkErr := os.MkdirAll(dir, 0700); mkErr != nil {
			return fmt.Errorf("cannot create harness home directory %q: %w", dir, mkErr)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("cannot access harness home directory %q: %w", dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("harness home path %q exists but is not a directory", dir)
	}
	return nil
}

func GetHomeDir() string {
	homeVar, err := os.UserHomeDir()
	if err != nil {
		return "/"
	}
	return homeVar
}

func ExpandHomeDir(pathStr string) string {
	if pathStr != "~" && !strings.HasPrefix(pathStr, "~/") && (!strings.HasPrefix(pathStr, `~\`) || runtime.GOOS != "windows") {
		return filepath.Clean(pathStr)
	}
	homeDir := GetHomeDir()
	if pathStr == "~" {
		return homeDir
	}
	return filepath.Clean(filepath.Join(homeDir, pathStr[2:]))
}
