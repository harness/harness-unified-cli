// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package har

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/harness/harness-cli/pkg/cmdctx"
)

const puppetTarReadLimit = 1 << 30 // 1 GiB safety cap while scanning tarball

// puppetMetadata holds the fields we need from metadata.json inside a Puppet module tarball.
type puppetMetadata struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// pushPuppetArtifact uploads a Puppet module .tar.gz to a Harness Artifact Registry.
//
// ctx.Id      = "<registry>" or "<registry/anything>" — only the registry portion is used.
// ctx.Args[0] = local .tar.gz or .tgz file path
//
// Upload endpoint: PUT {registryURL}/pkg/{accountID}/{registry}/puppet/upload
// Body: multipart/form-data with a single field named "file".
func pushPuppetArtifact(ctx *cmdctx.Ctx) error {
	if len(ctx.Args) == 0 {
		return fmt.Errorf("push puppet requires a local file path: push artifact <registry> <local-file.tar.gz>")
	}
	localFile := ctx.Args[0]

	lower := strings.ToLower(localFile)
	if !strings.HasSuffix(lower, ".tar.gz") && !strings.HasSuffix(lower, ".tgz") {
		return fmt.Errorf("puppet package must be a .tar.gz or .tgz file, got %q", filepath.Base(localFile))
	}

	fi, err := os.Stat(localFile)
	if err != nil {
		return fmt.Errorf("cannot access %q: %w", localFile, err)
	}
	if fi.IsDir() {
		return fmt.Errorf("%q is a directory; puppet push requires a .tar.gz file", localFile)
	}

	meta, err := extractPuppetMetadata(localFile)
	if err != nil {
		return fmt.Errorf("extracting metadata.json from %q: %w", filepath.Base(localFile), err)
	}
	if meta.Name == "" || meta.Version == "" {
		return fmt.Errorf("metadata.json must contain non-empty 'name' and 'version'")
	}

	registry := strings.SplitN(ctx.Id, "/", 2)[0]
	if registry == "" {
		return fmt.Errorf("registry name is required")
	}

	fileData, err := os.ReadFile(localFile)
	if err != nil {
		return fmt.Errorf("reading %q: %w", localFile, err)
	}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", filepath.Base(localFile))
	if err != nil {
		return fmt.Errorf("creating multipart field: %w", err)
	}
	if _, err := part.Write(fileData); err != nil {
		return fmt.Errorf("writing multipart data: %w", err)
	}
	if err := mw.Close(); err != nil {
		return fmt.Errorf("closing multipart writer: %w", err)
	}

	subpath := fmt.Sprintf("%s/puppet/upload", registry)
	uploadURL, err := buildPkgURL(ctx.Auth.RegistryURL, ctx.Auth.AccountID, subpath)
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "Uploading Puppet module %s@%s to registry %s ...\n", meta.Name, meta.Version, registry)

	req, err := http.NewRequest("PUT", uploadURL, &body)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	setAuthHeader(req, ctx.Auth.PATToken)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	if sums, sumErr := computeFileChecksums(localFile); sumErr == nil {
		setChecksumHeaders(req.Header, sums)
	}

	if _, err := doRequest(newHTTPClient(), req); err != nil {
		return fmt.Errorf("puppet upload failed: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Successfully pushed Puppet module '%s@%s' to registry '%s'\n", meta.Name, meta.Version, registry)
	return nil
}

// extractPuppetMetadata locates and parses the top-level metadata.json inside a
// Puppet module .tar.gz. Puppet modules are packaged as "<owner>-<name>-<version>/metadata.json"
// so the file lives exactly one directory level deep.
func extractPuppetMetadata(path string) (*puppetMetadata, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening tarball: %w", err)
	}
	defer f.Close()

	gzr, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("creating gzip reader: %w", err)
	}
	defer gzr.Close()

	tr := tar.NewReader(io.LimitReader(gzr, puppetTarReadLimit))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading tar: %w", err)
		}
		if hdr.FileInfo().IsDir() {
			continue
		}
		if filepath.Base(hdr.Name) != "metadata.json" {
			continue
		}
		// Only accept metadata.json exactly one directory deep (e.g. "module-1.0.0/metadata.json").
		// Skip nested copies such as "module-1.0.0/spec/fixtures/metadata.json".
		if strings.Count(strings.Trim(hdr.Name, "/"), "/") != 1 {
			continue
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			return nil, fmt.Errorf("reading metadata.json: %w", err)
		}
		var meta puppetMetadata
		if err := json.Unmarshal(data, &meta); err != nil {
			return nil, fmt.Errorf("parsing metadata.json: %w", err)
		}
		return &meta, nil
	}
	return nil, fmt.Errorf("metadata.json not found at top level of tarball")
}
