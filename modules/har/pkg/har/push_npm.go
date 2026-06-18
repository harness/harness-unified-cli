// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package har

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"github.com/harness/harness-cli/pkg/cmdctx"
)

// npmMinimalPackageJSON holds the fields we need from package.json.
// All optional fields use interface{} to tolerate varied value types.
type npmMinimalPackageJSON struct {
	Name    string `json:"name"`
	Version string `json:"version"`

	Description          interface{} `json:"description,omitempty"`
	Homepage             interface{} `json:"homepage,omitempty"`
	Keywords             []string    `json:"keywords,omitempty"`
	Repository           interface{} `json:"repository,omitempty"`
	Author               interface{} `json:"author,omitempty"`
	License              interface{} `json:"license,omitempty"`
	Dependencies         interface{} `json:"dependencies,omitempty"`
	DevDependencies      interface{} `json:"devDependencies,omitempty"`
	PeerDependencies     interface{} `json:"peerDependencies,omitempty"`
	PeerDependenciesMeta interface{} `json:"peerDependenciesMeta,omitempty"`
	OptionalDependencies interface{} `json:"optionalDependencies,omitempty"`
	AcceptDependencies   interface{} `json:"acceptDependencies,omitempty"`
	BundleDependencies   interface{} `json:"bundleDependencies,omitempty"`
	Bin                  interface{} `json:"bin,omitempty"`
	Contributors         interface{} `json:"contributors,omitempty"`
	Bugs                 interface{} `json:"bugs,omitempty"`
	Engines              interface{} `json:"engines,omitempty"`
	Deprecated           interface{} `json:"deprecated,omitempty"`
	Directories          interface{} `json:"directories,omitempty"`
	Funding              interface{} `json:"funding,omitempty"`
	CPU                  interface{} `json:"cpu,omitempty"`
	OS                   interface{} `json:"os,omitempty"`
	Main                 interface{} `json:"main,omitempty"`
	Module               interface{} `json:"module,omitempty"`
	Types                interface{} `json:"types,omitempty"`
	Typings              interface{} `json:"typings,omitempty"`
	Exports              interface{} `json:"exports,omitempty"`
	Imports              interface{} `json:"imports,omitempty"`
	Files                interface{} `json:"files,omitempty"`
	Workspaces           interface{} `json:"workspaces,omitempty"`
	Scripts              interface{} `json:"scripts,omitempty"`
	Config               interface{} `json:"config,omitempty"`
	PublishConfig        interface{} `json:"publishConfig,omitempty"`
	SideEffects          interface{} `json:"sideEffects,omitempty"`
	HasShrinkwrap        interface{} `json:"_hasShrinkwrap,omitempty"`
	HasInstallScript     interface{} `json:"hasInstallScript,omitempty"`
	NodeVersion          interface{} `json:"_nodeVersion,omitempty"`
	NpmUser              interface{} `json:"_npmUser,omitempty"`
	NpmVersion           interface{} `json:"_npmVersion,omitempty"`
}

// npmVersionEntry is the per-version object inside "versions".
type npmVersionEntry struct {
	ID      string `json:"_id"`
	Name    string `json:"name"`
	Version string `json:"version"`

	Description          interface{} `json:"description,omitempty"`
	Author               interface{} `json:"author,omitempty"`
	Homepage             interface{} `json:"homepage,omitempty"`
	License              interface{} `json:"license,omitempty"`
	Repository           interface{} `json:"repository,omitempty"`
	Keywords             []string    `json:"keywords,omitempty"`
	Dependencies         interface{} `json:"dependencies,omitempty"`
	DevDependencies      interface{} `json:"devDependencies,omitempty"`
	PeerDependencies     interface{} `json:"peerDependencies,omitempty"`
	PeerDependenciesMeta interface{} `json:"peerDependenciesMeta,omitempty"`
	OptionalDependencies interface{} `json:"optionalDependencies,omitempty"`
	AcceptDependencies   interface{} `json:"acceptDependencies,omitempty"`
	BundleDependencies   interface{} `json:"bundleDependencies,omitempty"`
	Bin                  interface{} `json:"bin,omitempty"`
	Contributors         interface{} `json:"contributors,omitempty"`
	Bugs                 interface{} `json:"bugs,omitempty"`
	Engines              interface{} `json:"engines,omitempty"`
	Deprecated           interface{} `json:"deprecated,omitempty"`
	Directories          interface{} `json:"directories,omitempty"`
	Funding              interface{} `json:"funding,omitempty"`
	CPU                  interface{} `json:"cpu,omitempty"`
	OS                   interface{} `json:"os,omitempty"`
	Main                 interface{} `json:"main,omitempty"`
	Module               interface{} `json:"module,omitempty"`
	Types                interface{} `json:"types,omitempty"`
	Typings              interface{} `json:"typings,omitempty"`
	Exports              interface{} `json:"exports,omitempty"`
	Imports              interface{} `json:"imports,omitempty"`
	Files                interface{} `json:"files,omitempty"`
	Workspaces           interface{} `json:"workspaces,omitempty"`
	Scripts              interface{} `json:"scripts,omitempty"`
	Config               interface{} `json:"config,omitempty"`
	PublishConfig        interface{} `json:"publishConfig,omitempty"`
	SideEffects          interface{} `json:"sideEffects,omitempty"`
	HasShrinkwrap        interface{} `json:"_hasShrinkwrap,omitempty"`
	HasInstallScript     interface{} `json:"hasInstallScript,omitempty"`
	NodeVersion          interface{} `json:"_nodeVersion,omitempty"`
	NpmUser              interface{} `json:"_npmUser,omitempty"`
	NpmVersion           interface{} `json:"_npmVersion,omitempty"`
	Readme               string      `json:"readme,omitempty"`
	Dist                 struct{}    `json:"dist"`
}

// npmAttachment is the binary attachment embedded in the upload payload.
type npmAttachment struct {
	ContentType string `json:"content_type"`
	Data        string `json:"data"`
	Length      int    `json:"length"`
}

// npmUploadPayload is the full JSON body sent to the npm upload endpoint.
type npmUploadPayload struct {
	ID          string                      `json:"_id"`
	Name        string                      `json:"name"`
	Description interface{}                 `json:"description,omitempty"`
	DistTags    map[string]string           `json:"dist-tags"`
	Versions    map[string]*npmVersionEntry `json:"versions"`
	Readme      string                      `json:"readme,omitempty"`
	License     interface{}                 `json:"license,omitempty"`
	Homepage    interface{}                 `json:"homepage,omitempty"`
	Keywords    []string                    `json:"keywords,omitempty"`
	Repository  interface{}                 `json:"repository,omitempty"`
	Author      interface{}                 `json:"author,omitempty"`
	Bugs        interface{}                 `json:"bugs,omitempty"`
	Attachments map[string]*npmAttachment   `json:"_attachments"`
}

// pushNpmArtifact implements "push artifact <registry/name> <local.tgz>" for npm packages.
//
// Steps:
//  1. Read the .tgz file from ctx.Args[0].
//  2. Extract package.json (and optionally a README) from the tarball.
//  3. Build the npm upload JSON payload with a base64-encoded tarball attachment.
//  4. POST to {registryURL}/pkg/{accountID}/{registry}/npm/{name}.
func pushNpmArtifact(ctx *cmdctx.Ctx) error {
	if len(ctx.Args) == 0 {
		return fmt.Errorf("push npm artifact requires a local file path: push artifact <registry/name> <local-file>")
	}
	localFile := ctx.Args[0]

	registry, name, err := parseRegistryAndName(ctx.Id)
	if err != nil {
		return err
	}

	// --- 1. Read the tarball bytes -----------------------------------------
	tgzData, err := os.ReadFile(localFile)
	if err != nil {
		return fmt.Errorf("reading %q: %w", localFile, err)
	}

	// --- 2. Extract package.json -------------------------------------------
	pkgJSONBytes, err := readFileFromTarGz(localFile, "/package.json")
	if err != nil {
		// Fallback: try the exact path "package/package.json" (standard npm layout)
		pkgJSONBytes, err = readFileFromTarGz(localFile, "package.json")
		if err != nil {
			return fmt.Errorf("extracting package.json from %q: %w", localFile, err)
		}
	}

	var pkg npmMinimalPackageJSON
	if err := json.Unmarshal(pkgJSONBytes, &pkg); err != nil {
		return fmt.Errorf("parsing package.json: %w", err)
	}
	if pkg.Name == "" || pkg.Version == "" {
		return fmt.Errorf("package.json must contain non-empty 'name' and 'version'")
	}

	// --- 3. Optionally extract README (best-effort) -------------------------
	var readme string
	for _, candidate := range []string{"/README.md", "/README", "/readme.md", "/readme"} {
		data, rerr := readFileFromTarGz(localFile, candidate)
		if rerr == nil {
			readme = string(data)
			break
		}
	}

	// --- 4. Build upload payload -------------------------------------------
	tarballName := pkg.Name + "-" + pkg.Version + ".tgz"
	b64Data := base64.StdEncoding.EncodeToString(tgzData)

	versionEntry := &npmVersionEntry{
		ID:                   pkg.Name + "@" + pkg.Version,
		Name:                 pkg.Name,
		Version:              pkg.Version,
		Description:          pkg.Description,
		Author:               pkg.Author,
		Homepage:             pkg.Homepage,
		License:              pkg.License,
		Repository:           pkg.Repository,
		Keywords:             pkg.Keywords,
		Dependencies:         pkg.Dependencies,
		DevDependencies:      pkg.DevDependencies,
		PeerDependencies:     pkg.PeerDependencies,
		PeerDependenciesMeta: pkg.PeerDependenciesMeta,
		OptionalDependencies: pkg.OptionalDependencies,
		AcceptDependencies:   pkg.AcceptDependencies,
		BundleDependencies:   pkg.BundleDependencies,
		Bin:                  pkg.Bin,
		Contributors:         pkg.Contributors,
		Bugs:                 pkg.Bugs,
		Engines:              pkg.Engines,
		Deprecated:           pkg.Deprecated,
		Directories:          pkg.Directories,
		Funding:              pkg.Funding,
		CPU:                  pkg.CPU,
		OS:                   pkg.OS,
		Main:                 pkg.Main,
		Module:               pkg.Module,
		Types:                pkg.Types,
		Typings:              pkg.Typings,
		Exports:              pkg.Exports,
		Imports:              pkg.Imports,
		Files:                pkg.Files,
		Workspaces:           pkg.Workspaces,
		Scripts:              pkg.Scripts,
		Config:               pkg.Config,
		PublishConfig:        pkg.PublishConfig,
		SideEffects:          pkg.SideEffects,
		HasShrinkwrap:        pkg.HasShrinkwrap,
		HasInstallScript:     pkg.HasInstallScript,
		NodeVersion:          pkg.NodeVersion,
		NpmUser:              pkg.NpmUser,
		NpmVersion:           pkg.NpmVersion,
		Readme:               readme,
	}

	payload := &npmUploadPayload{
		ID:          pkg.Name,
		Name:        pkg.Name,
		Description: pkg.Description,
		DistTags:    map[string]string{"latest": pkg.Version},
		Versions:    map[string]*npmVersionEntry{pkg.Version: versionEntry},
		Readme:      readme,
		License:     pkg.License,
		Homepage:    pkg.Homepage,
		Keywords:    pkg.Keywords,
		Repository:  pkg.Repository,
		Author:      pkg.Author,
		Bugs:        pkg.Bugs,
		Attachments: map[string]*npmAttachment{
			tarballName: {
				ContentType: "application/octet-stream",
				Data:        b64Data,
				Length:      len(tgzData),
			},
		},
	}

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encoding upload payload: %w", err)
	}

	// --- 5. POST to the npm upload endpoint --------------------------------
	subpath := fmt.Sprintf("%s/npm/%s", registry, name)
	uploadURL, err := buildPkgURL(ctx.Auth.RegistryURL, ctx.Auth.AccountID, subpath)
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "Uploading npm package %s@%s to registry %s ...\n", pkg.Name, pkg.Version, registry)

	req, err := http.NewRequest("POST", uploadURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	setAuthHeader(req, ctx.Auth.PATToken)
	req.Header.Set("Content-Type", "application/json")

	if sums, sumErr := computeFileChecksums(localFile); sumErr == nil {
		setChecksumHeaders(req.Header, sums)
	}

	if _, err := doRequest(newHTTPClient(), req); err != nil {
		return fmt.Errorf("npm upload failed: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Successfully pushed %s@%s to %s\n", pkg.Name, pkg.Version, ctx.Id)
	return nil
}
