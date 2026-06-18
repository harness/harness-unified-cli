// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package har

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/harness/harness-cli/pkg/cmdctx"
)

// pushSwiftArtifact uploads a Swift package (.zip) to the Harness Artifact Registry.
//
// ctx.Id      = "<registry>/<anything>" — only the registry portion is used.
// ctx.Args[0] = local .zip file path
// ctx.Args[1] = target path in "scope/name/version" format
// --metadata-path (optional) = path to a metadata JSON file
//
// Upload endpoint: PUT {registryURL}/pkg/{accountID}/{registry}/swift/{scope}/{name}/{version}
func pushSwiftArtifact(ctx *cmdctx.Ctx) error {
	if len(ctx.Args) < 2 {
		return fmt.Errorf("push swift requires a local .zip file and a target path: push artifact <registry/anything> <file.zip> <scope/name/version>")
	}

	localFile := ctx.Args[0]
	targetPackagePath := ctx.Args[1]

	// Validate .zip extension
	if !strings.HasSuffix(strings.ToLower(localFile), ".zip") {
		return fmt.Errorf("swift push only supports .zip files, got %q", localFile)
	}

	// Validate file exists and is not a directory
	fi, err := os.Stat(localFile)
	if err != nil {
		return fmt.Errorf("cannot access %q: %w", localFile, err)
	}
	if fi.IsDir() {
		return fmt.Errorf("%q is a directory; swift push requires a .zip file", localFile)
	}

	// Parse target path: scope/name/version
	scope, packageName, version, err := parseSwiftTargetPath(targetPackagePath)
	if err != nil {
		return err
	}

	// Parse registry from ctx.Id
	registry, _, err := parseRegistryAndName(ctx.Id)
	if err != nil {
		return err
	}

	// Optional metadata file
	metadataPath := cmdctx.GetString(ctx.FlagValues, "metadata-path")

	// Build multipart body
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)

	// Add source-archive field
	archivePart, err := mw.CreateFormFile("source-archive", filepath.Base(localFile))
	if err != nil {
		return fmt.Errorf("creating multipart field source-archive: %w", err)
	}
	archiveFile, err := os.Open(localFile)
	if err != nil {
		return fmt.Errorf("opening %q: %w", localFile, err)
	}
	defer archiveFile.Close()
	if _, err = io.Copy(archivePart, archiveFile); err != nil {
		return fmt.Errorf("reading %q: %w", localFile, err)
	}

	// Add optional metadata field
	if metadataPath != "" {
		metadataPart, err := mw.CreateFormFile("metadata", filepath.Base(metadataPath))
		if err != nil {
			return fmt.Errorf("creating multipart field metadata: %w", err)
		}
		metadataFile, err := os.Open(metadataPath)
		if err != nil {
			return fmt.Errorf("opening metadata file %q: %w", metadataPath, err)
		}
		defer metadataFile.Close()
		if _, err = io.Copy(metadataPart, metadataFile); err != nil {
			return fmt.Errorf("reading metadata file %q: %w", metadataPath, err)
		}
	}

	if err := mw.Close(); err != nil {
		return fmt.Errorf("finalising multipart body: %w", err)
	}

	// Build upload URL: {registryURL}/pkg/{accountID}/{registry}/swift/{scope}/{name}/{version}
	subpath := fmt.Sprintf("%s/swift/%s/%s/%s", registry, scope, packageName, version)
	uploadURL, err := buildPkgURL(ctx.Auth.RegistryURL, ctx.Auth.AccountID, subpath)
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "Uploading %s → %s/%s/%s/%s ...\n",
		filepath.Base(localFile), registry, scope, packageName, version)

	req, err := http.NewRequest("PUT", uploadURL, &body)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	setAuthHeader(req, ctx.Auth.PATToken)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Accept", "application/vnd.swift.registry.v1+json")

	if _, err := doRequest(newHTTPClient(), req); err != nil {
		return fmt.Errorf("swift upload failed: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Successfully pushed %s to %s/%s/%s/%s\n",
		filepath.Base(localFile), registry, scope, packageName, version)
	return nil
}

// parseSwiftTargetPath parses a "scope/name/version" string into its three components.
func parseSwiftTargetPath(input string) (scope, name, version string, err error) {
	if strings.TrimSpace(input) == "" {
		return "", "", "", fmt.Errorf("target path cannot be empty")
	}
	parts := strings.Split(input, "/")
	if len(parts) != 3 {
		return "", "", "", fmt.Errorf(
			"target path must be <scope>/<name>/<version>, got %q (%d part(s))",
			input, len(parts),
		)
	}
	scope = strings.TrimSpace(parts[0])
	name = strings.TrimSpace(parts[1])
	version = strings.TrimSpace(parts[2])
	if scope == "" {
		return "", "", "", fmt.Errorf("target path scope is empty in %q", input)
	}
	if name == "" {
		return "", "", "", fmt.Errorf("target path name is empty in %q", input)
	}
	if version == "" {
		return "", "", "", fmt.Errorf("target path version is empty in %q", input)
	}
	return scope, name, version, nil
}
