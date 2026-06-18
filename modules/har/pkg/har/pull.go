// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package har

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/harness/harness-cli/pkg/cmdctx"
)

const pullArtifactHandlerID = "pull_artifact"

// pullArtifactHandler implements "pull artifact <registry/name> <local-dest> --version <v>".
//
// ctx.Id      = "<registry>/<name>", e.g. "my-registry/myapp"
// ctx.Args[0] = local destination directory
//
// The download endpoint is GET {registryURL}/pkg/{account}/{registry}/files/{name}/{version}/{filename}
func pullArtifactHandler(ctx *cmdctx.Ctx) error {
	// Determine package type.
	// For pull we default to "generic" when --package-type is not set, because
	// the most common pull use-case is generic artifacts. Users can override
	// with --package-type for other formats.
	pkgType := cmdctx.GetString(ctx.FlagValues, "package-type")
	if pkgType == "" {
		// Try to infer from the --filename flag as a secondary hint.
		filenameHint := cmdctx.GetString(ctx.FlagValues, "filename")
		if filenameHint != "" {
			pkgType = inferPackageType(filenameHint)
		}
	}
	if pkgType == "" {
		pkgType = "generic"
	}

	switch pkgType {
	case "generic":
		return pullGenericArtifact(ctx)
	case "maven":
		return pullMavenArtifact(ctx)
	case "npm":
		return pullNpmArtifact(ctx)
	case "python":
		return pullPythonArtifact(ctx)
	case "nuget":
		return pullNugetArtifact(ctx)
	case "rpm":
		return pullRpmArtifact(ctx)
	case "cargo":
		return pullCargoArtifact(ctx)
	case "conda":
		return pullCondaArtifact(ctx)
	case "composer":
		return pullComposerArtifact(ctx)
	case "dart":
		return pullDartArtifact(ctx)
	case "swift":
		return pullSwiftArtifact(ctx)
	case "go":
		return pullGoArtifact(ctx)
	case "helm":
		return pullHelmArtifact(ctx)
	case "docker":
		return pullDockerArtifact(ctx)
	default:
		return fmt.Errorf("unknown package type %q; supported types: generic, maven, npm, python, nuget, rpm, cargo, conda, composer, dart, swift, go, helm, docker", pkgType)
	}
}

// pullGenericArtifact handles pull for generic artifacts.
func pullGenericArtifact(ctx *cmdctx.Ctx) error {
	if len(ctx.Args) == 0 {
		return fmt.Errorf("pull artifact requires a destination path: pull artifact <registry/name> <local-dest> --version <version>")
	}
	localDest := ctx.Args[0]

	registry, name, err := parseRegistryAndName(ctx.Id)
	if err != nil {
		return err
	}

	version := cmdctx.GetString(ctx.FlagValues, "version")
	if version == "" {
		return fmt.Errorf("--version is required for generic artifact pull")
	}

	registryFilename := cmdctx.GetString(ctx.FlagValues, "filename")
	if registryFilename == "" {
		return fmt.Errorf("--filename is required: specify the filename to download from the registry")
	}

	fi, err := os.Stat(localDest)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("destination directory %q does not exist", localDest)
		}
		return fmt.Errorf("cannot access destination %q: %w", localDest, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("destination %q is not a directory", localDest)
	}

	subpath := fmt.Sprintf("%s/files/%s/%s/%s", registry, name, version, registryFilename)
	downloadURL, err := buildPkgURL(ctx.Auth.RegistryURL, ctx.Auth.AccountID, subpath)
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "Downloading %s/%s/%s from %s ...\n", name, version, registryFilename, registry)

	req, err := http.NewRequest("GET", downloadURL, nil)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	setAuthHeader(req, ctx.Auth.PATToken)

	resp, err := newHTTPClient().Do(req)
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("download failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	outPath := filepath.Join(localDest, registryFilename)
	outFile, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("creating %q: %w", outPath, err)
	}
	defer outFile.Close()

	written, err := io.Copy(outFile, resp.Body)
	if err != nil {
		return fmt.Errorf("writing to %q: %w", outPath, err)
	}

	fmt.Fprintf(os.Stderr, "Saved %s (%d bytes)\n", outPath, written)
	return nil
}

// Stub handlers for package types not yet implemented.

func pullMavenArtifact(_ *cmdctx.Ctx) error {
	return fmt.Errorf("pull maven artifact: not yet implemented")
}

func pullNpmArtifact(_ *cmdctx.Ctx) error {
	return fmt.Errorf("pull npm artifact: not yet implemented")
}

func pullPythonArtifact(_ *cmdctx.Ctx) error {
	return fmt.Errorf("pull python artifact: not yet implemented")
}

func pullNugetArtifact(_ *cmdctx.Ctx) error {
	return fmt.Errorf("pull nuget artifact: not yet implemented")
}

func pullRpmArtifact(_ *cmdctx.Ctx) error {
	return fmt.Errorf("pull rpm artifact: not yet implemented")
}

func pullCargoArtifact(_ *cmdctx.Ctx) error {
	return fmt.Errorf("pull cargo artifact: not yet implemented")
}

func pullCondaArtifact(_ *cmdctx.Ctx) error {
	return fmt.Errorf("pull conda artifact: not yet implemented")
}

func pullComposerArtifact(_ *cmdctx.Ctx) error {
	return fmt.Errorf("pull composer artifact: not yet implemented")
}

func pullDartArtifact(_ *cmdctx.Ctx) error {
	return fmt.Errorf("pull dart artifact: not yet implemented")
}

func pullSwiftArtifact(_ *cmdctx.Ctx) error {
	return fmt.Errorf("pull swift artifact: not yet implemented")
}

func pullGoArtifact(_ *cmdctx.Ctx) error {
	return fmt.Errorf("pull go artifact: not yet implemented")
}

func pullHelmArtifact(_ *cmdctx.Ctx) error {
	return fmt.Errorf("pull helm artifact: not yet implemented")
}

func pullDockerArtifact(_ *cmdctx.Ctx) error {
	return fmt.Errorf("pull docker artifact: not yet implemented")
}
