// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package har

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/harness/harness-cli/pkg/cmdctx"
)

// pushComposerArtifact uploads a Composer package (.zip) to the registry.
//
// ctx.Id      = "<registry>/<name>" (only registry portion is used for the URL)
// ctx.Args[0] = local .zip file path
//
// Upload endpoint: PUT {registryURL}/pkg/{accountID}/{registry}/composer/packages/upload
func pushComposerArtifact(ctx *cmdctx.Ctx) error {
	if len(ctx.Args) == 0 {
		return fmt.Errorf("push composer artifact requires a local file path: push artifact <registry/name> <local-file>")
	}
	localFile := ctx.Args[0]

	if !strings.HasSuffix(strings.ToLower(localFile), ".zip") {
		return fmt.Errorf("composer package must be a .zip file, got %q", filepath.Base(localFile))
	}

	registry, _, err := parseRegistryAndName(ctx.Id)
	if err != nil {
		return err
	}

	fi, err := os.Stat(localFile)
	if err != nil {
		return fmt.Errorf("cannot access %q: %w", localFile, err)
	}
	if fi.IsDir() {
		return fmt.Errorf("%q is a directory; composer push requires a .zip file", localFile)
	}

	subpath := fmt.Sprintf("%s/composer/packages/upload", registry)
	uploadURL, err := buildPkgURL(ctx.Auth.RegistryURL, ctx.Auth.AccountID, subpath)
	if err != nil {
		return err
	}

	f, err := os.Open(localFile)
	if err != nil {
		return fmt.Errorf("opening %q: %w", localFile, err)
	}
	defer f.Close()

	fmt.Fprintf(os.Stderr, "Uploading %s → %s/composer/packages/upload ...\n", filepath.Base(localFile), registry)

	req, err := http.NewRequest("PUT", uploadURL, f)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	setAuthHeader(req, ctx.Auth.PATToken)
	req.Header.Set("Content-Type", "application/octet-stream")
	req.ContentLength = fi.Size()

	if sums, sumErr := computeFileChecksums(localFile); sumErr == nil {
		setChecksumHeaders(req.Header, sums)
	}

	if _, err := doRequest(newHTTPClient(), req); err != nil {
		return fmt.Errorf("upload failed: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Successfully pushed %s to %s\n", filepath.Base(localFile), ctx.Id)
	return nil
}
