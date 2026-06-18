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

// pushRpmArtifact uploads an RPM package to a Harness Artifact Registry.
//
// ctx.Id      = "<registry>" (only the registry name is needed; there is no per-package name)
// ctx.Args[0] = local .rpm file path
//
// The upload endpoint is POST {registryURL}/pkg/{accountID}/{registry}/rpm/
func pushRpmArtifact(ctx *cmdctx.Ctx) error {
	if len(ctx.Args) == 0 {
		return fmt.Errorf("push rpm requires a local file path: push artifact <registry> <local-file>")
	}
	localFile := ctx.Args[0]

	if !strings.HasSuffix(strings.ToLower(localFile), ".rpm") {
		return fmt.Errorf("file %q does not have a .rpm extension", localFile)
	}

	fi, err := os.Stat(localFile)
	if err != nil {
		return fmt.Errorf("cannot access %q: %w", localFile, err)
	}
	if fi.IsDir() {
		return fmt.Errorf("%q is a directory; rpm push requires a file", localFile)
	}

	// For RPM the id is just the registry name (no per-package sub-name).
	// Support either "registry" or "registry/anything" — only the registry portion is used.
	registry := strings.SplitN(ctx.Id, "/", 2)[0]
	if registry == "" {
		return fmt.Errorf("registry name is required")
	}

	// Build upload URL: POST {registryURL}/pkg/{accountID}/{registry}/rpm/
	subpath := fmt.Sprintf("%s/rpm/", registry)
	uploadURL, err := buildPkgURL(ctx.Auth.RegistryURL, ctx.Auth.AccountID, subpath)
	if err != nil {
		return err
	}

	// Read the RPM file into memory.
	fileBytes, err := os.ReadFile(localFile)
	if err != nil {
		return fmt.Errorf("reading %q: %w", localFile, err)
	}

	// Build multipart body with a single field named "file".
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", filepath.Base(localFile))
	if err != nil {
		return fmt.Errorf("creating multipart field: %w", err)
	}
	if _, err := io.Copy(part, bytes.NewReader(fileBytes)); err != nil {
		return fmt.Errorf("writing multipart data: %w", err)
	}
	if err := mw.Close(); err != nil {
		return fmt.Errorf("closing multipart writer: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Uploading %s → %s/rpm/ ...\n", filepath.Base(localFile), registry)

	req, err := http.NewRequest("POST", uploadURL, &body)
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

	fmt.Fprintf(os.Stderr, "Successfully pushed %s to %s\n", filepath.Base(localFile), registry)
	return nil
}
