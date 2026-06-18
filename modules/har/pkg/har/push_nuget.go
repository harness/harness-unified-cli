// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package har

import (
	"bytes"
	"fmt"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/harness/harness-cli/pkg/cmdctx"
)

// pushNugetArtifact uploads a .nupkg file to a Harness NuGet registry.
//
// ctx.Id      = "<registry>/<name>" (name is ignored for nuget; only registry is used)
// ctx.Args[0] = local .nupkg file path
//
// The upload endpoint is: PUT {registryURL}/pkg/{accountID}/{registry}/nuget/
// Body: multipart/form-data with a single field named "package" containing the file.
func pushNugetArtifact(ctx *cmdctx.Ctx) error {
	if len(ctx.Args) == 0 {
		return fmt.Errorf("push nuget requires a local file path: push artifact <registry/name> <local-file>")
	}
	localFile := ctx.Args[0]

	if !strings.HasSuffix(strings.ToLower(localFile), ".nupkg") {
		return fmt.Errorf("nuget push requires a .nupkg file, got %q", localFile)
	}

	fi, err := os.Stat(localFile)
	if err != nil {
		return fmt.Errorf("cannot access %q: %w", localFile, err)
	}
	if fi.IsDir() {
		return fmt.Errorf("%q is a directory; nuget push requires a .nupkg file", localFile)
	}

	registry, _, err := parseRegistryAndName(ctx.Id)
	if err != nil {
		return err
	}

	// URL: PUT {registryURL}/pkg/{accountID}/{registry}/nuget/
	subpath := fmt.Sprintf("%s/nuget/", registry)
	uploadURL, err := buildPkgURL(ctx.Auth.RegistryURL, ctx.Auth.AccountID, subpath)
	if err != nil {
		return err
	}

	// Read file content
	fileData, err := os.ReadFile(localFile)
	if err != nil {
		return fmt.Errorf("reading %q: %w", localFile, err)
	}

	// Build multipart body
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("package", filepath.Base(localFile))
	if err != nil {
		return fmt.Errorf("creating multipart field: %w", err)
	}
	if _, err := part.Write(fileData); err != nil {
		return fmt.Errorf("writing multipart data: %w", err)
	}
	if err := mw.Close(); err != nil {
		return fmt.Errorf("closing multipart writer: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Uploading %s → %s/nuget/ ...\n", filepath.Base(localFile), registry)

	req, err := http.NewRequest("PUT", uploadURL, &buf)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	setAuthHeader(req, ctx.Auth.PATToken)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	if sums, sumErr := computeFileChecksums(localFile); sumErr == nil {
		setChecksumHeaders(req.Header, sums)
	}

	if _, err := doRequest(newHTTPClient(), req); err != nil {
		return fmt.Errorf("upload failed: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Successfully pushed %s to %s\n", filepath.Base(localFile), ctx.Id)
	return nil
}
