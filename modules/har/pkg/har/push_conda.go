// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package har

import (
	"archive/tar"
	"compress/bzip2"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/harness/harness-cli/pkg/cmdctx"
)

// condaIndexJSON holds the fields we need from info/index.json inside a conda package.
type condaIndexJSON struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Subdir  string `json:"subdir"`
}

// pushCondaArtifact implements "push artifact <registry/name> <local-file>" for conda packages.
//
// Supported formats:
//   - .tar.bz2  — fully supported
//   - .conda    — requires zstd decompression; not yet supported (no zstd dependency in go.mod)
//
// The upload is a raw PUT to:
//
//	{registryURL}/pkg/{accountID}/{registry}/conda/{subdir}/{filename}
//
// with headers:
//
//	X-File-Name: <filename>
//	X-Subdir:    <subdir from index.json>
func pushCondaArtifact(ctx *cmdctx.Ctx) error {
	if len(ctx.Args) == 0 {
		return fmt.Errorf("push conda requires a local file path: push artifact <registry/name> <local-file>")
	}
	localFile := ctx.Args[0]
	fileName := filepath.Base(localFile)

	lower := strings.ToLower(fileName)
	switch {
	case strings.HasSuffix(lower, ".conda"):
		return fmt.Errorf("push conda: .conda format requires zstd decompression which is not yet supported; please use .tar.bz2")
	case strings.HasSuffix(lower, ".tar.bz2"):
		// supported below
	default:
		return fmt.Errorf("push conda: unsupported file extension for %q; expected .conda or .tar.bz2", fileName)
	}

	fi, err := os.Stat(localFile)
	if err != nil {
		return fmt.Errorf("cannot access %q: %w", localFile, err)
	}
	if fi.IsDir() {
		return fmt.Errorf("%q is a directory; conda push requires a file", localFile)
	}

	// Parse metadata from .tar.bz2
	meta, err := condaMetaFromBZ2(localFile)
	if err != nil {
		return fmt.Errorf("reading conda metadata from %q: %w", localFile, err)
	}
	if meta.Subdir == "" {
		return fmt.Errorf("conda metadata in %q is missing 'subdir' field", localFile)
	}
	if meta.Name == "" {
		return fmt.Errorf("conda metadata in %q is missing 'name' field", localFile)
	}
	if meta.Version == "" {
		return fmt.Errorf("conda metadata in %q is missing 'version' field", localFile)
	}

	registry, _, err := parseRegistryAndName(ctx.Id)
	if err != nil {
		return err
	}

	// URL: {registryURL}/pkg/{accountID}/{registry}/conda/{subdir}/{filename}
	subpath := fmt.Sprintf("%s/conda/%s/%s", registry, meta.Subdir, fileName)
	uploadURL, err := buildPkgURL(ctx.Auth.RegistryURL, ctx.Auth.AccountID, subpath)
	if err != nil {
		return err
	}

	f, err := os.Open(localFile)
	if err != nil {
		return fmt.Errorf("opening %q: %w", localFile, err)
	}
	defer f.Close()

	fmt.Fprintf(os.Stderr, "Uploading conda package %s (subdir=%s, name=%s, version=%s) ...\n",
		fileName, meta.Subdir, meta.Name, meta.Version)

	req, err := http.NewRequest("PUT", uploadURL, f)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	setAuthHeader(req, ctx.Auth.PATToken)
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-File-Name", fileName)
	req.Header.Set("X-Subdir", meta.Subdir)
	req.ContentLength = fi.Size()

	if _, err := doRequest(newHTTPClient(), req); err != nil {
		return fmt.Errorf("conda upload failed: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Successfully pushed %s to %s\n", fileName, ctx.Id)
	return nil
}

// condaMetaFromBZ2 reads index.json from inside a .tar.bz2 conda package and returns
// the name, version, and subdir fields.
func condaMetaFromBZ2(filePath string) (*condaIndexJSON, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("opening file: %w", err)
	}
	defer f.Close()

	bzReader := bzip2.NewReader(f)
	tarReader := tar.NewReader(io.LimitReader(bzReader, 1<<30)) // 1 GiB safety limit

	for {
		hdr, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading tar: %w", err)
		}
		if strings.Contains(strings.ToLower(hdr.Name), "index.json") {
			var idx condaIndexJSON
			if err := json.NewDecoder(tarReader).Decode(&idx); err != nil {
				return nil, fmt.Errorf("decoding index.json: %w", err)
			}
			return &idx, nil
		}
	}
	return nil, fmt.Errorf("index.json not found in archive")
}
