// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package har

import (
	"bufio"
	"bytes"
	"fmt"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/harness/harness-cli/pkg/cmdctx"
)

// pythonPackageMeta holds the name and version extracted from a Python package.
type pythonPackageMeta struct {
	Name    string
	Version string
}

// pushPythonArtifact uploads one or more Python packages (.whl or .tar.gz) to the registry.
//
// ctx.Id      = "<registry>" (only registry is needed; package name/version come from metadata)
// ctx.Args[0] = local file path or directory
//
// The upload endpoint is POST {registryURL}/pkg/{accountID}/{registry}/python/
func pushPythonArtifact(ctx *cmdctx.Ctx) error {
	if len(ctx.Args) == 0 {
		return fmt.Errorf("push python requires a local file path or directory")
	}
	localPath := ctx.Args[0]

	// registry is the first segment of ctx.Id; there is no /name for python uploads
	registry := strings.SplitN(ctx.Id, "/", 2)[0]
	if registry == "" {
		return fmt.Errorf("invalid id %q: expected <registry>", ctx.Id)
	}

	fi, err := os.Stat(localPath)
	if err != nil {
		return fmt.Errorf("cannot access %q: %w", localPath, err)
	}

	var files []string
	if fi.IsDir() {
		files, err = scanPythonPackageDir(localPath)
		if err != nil {
			return err
		}
		if len(files) == 0 {
			return fmt.Errorf("no .whl or .tar.gz files found in %q", localPath)
		}
	} else {
		if err := validatePythonPackageFile(localPath); err != nil {
			return err
		}
		files = []string{localPath}
	}

	maxConcurrent := cmdctx.GetInt(ctx.FlagValues, "max-concurrent-uploads")
	if maxConcurrent <= 0 {
		maxConcurrent = defaultMaxConcurrentUploads
	}

	sem := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup
	errs := make([]error, len(files))

	for i, f := range files {
		wg.Add(1)
		go func(i int, f string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if err := uploadPythonFile(ctx, registry, f); err != nil {
				errs[i] = fmt.Errorf("uploading %s: %w", filepath.Base(f), err)
			}
		}(i, f)
	}
	wg.Wait()

	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

// uploadPythonFile extracts metadata from f, then POSTs it as multipart to the registry.
func uploadPythonFile(ctx *cmdctx.Ctx, registry, filePath string) error {
	meta, err := extractPythonMeta(filePath)
	if err != nil {
		return fmt.Errorf("extracting metadata: %w", err)
	}

	fileBytes, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("reading %q: %w", filePath, err)
	}

	// Build multipart body: fields name, version, content
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)

	if err := mw.WriteField("name", meta.Name); err != nil {
		return fmt.Errorf("writing name field: %w", err)
	}
	if err := mw.WriteField("version", meta.Version); err != nil {
		return fmt.Errorf("writing version field: %w", err)
	}

	part, err := mw.CreateFormFile("content", filepath.Base(filePath))
	if err != nil {
		return fmt.Errorf("creating content field: %w", err)
	}
	if _, err := part.Write(fileBytes); err != nil {
		return fmt.Errorf("writing file bytes: %w", err)
	}
	if err := mw.Close(); err != nil {
		return fmt.Errorf("closing multipart writer: %w", err)
	}

	// POST {registryURL}/pkg/{accountID}/{registry}/python/
	subpath := registry + "/python/"
	uploadURL, err := buildPkgURL(ctx.Auth.RegistryURL, ctx.Auth.AccountID, subpath)
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "Uploading %s (%s %s) ...\n", filepath.Base(filePath), meta.Name, meta.Version)

	req, err := http.NewRequest("POST", uploadURL, &body)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	setAuthHeader(req, ctx.Auth.PATToken)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	if sums, sumErr := computeFileChecksums(filePath); sumErr == nil {
		setChecksumHeaders(req.Header, sums)
	}

	if _, err := doRequest(newHTTPClient(), req); err != nil {
		return fmt.Errorf("upload failed: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Successfully pushed %s to %s\n", filepath.Base(filePath), registry)
	return nil
}

// extractPythonMeta reads name and version from a .whl or .tar.gz Python package.
func extractPythonMeta(filePath string) (*pythonPackageMeta, error) {
	switch {
	case strings.HasSuffix(filePath, ".whl"):
		data, err := readFileFromZip(filePath, ".dist-info/METADATA")
		if err != nil {
			return nil, fmt.Errorf("reading METADATA from whl: %w", err)
		}
		return parsePythonMetadata(data)
	case strings.HasSuffix(filePath, ".tar.gz"):
		data, err := readFileFromTarGz(filePath, "PKG-INFO")
		if err != nil {
			return nil, fmt.Errorf("reading PKG-INFO from tar.gz: %w", err)
		}
		return parsePythonMetadata(data)
	default:
		return nil, fmt.Errorf("unsupported python package format: %q (expected .whl or .tar.gz)", filepath.Base(filePath))
	}
}

// parsePythonMetadata parses RFC 822-style metadata and returns name + version.
func parsePythonMetadata(data []byte) (*pythonPackageMeta, error) {
	meta := &pythonPackageMeta{}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "Name: ") {
			meta.Name = strings.TrimPrefix(line, "Name: ")
		} else if strings.HasPrefix(line, "Version: ") {
			meta.Version = strings.TrimPrefix(line, "Version: ")
		}
		if meta.Name != "" && meta.Version != "" {
			return meta, nil
		}
	}
	return nil, fmt.Errorf("Name and Version not found in package metadata")
}

// validatePythonPackageFile returns an error if filePath is not a .whl or .tar.gz file.
func validatePythonPackageFile(filePath string) error {
	if strings.HasSuffix(filePath, ".whl") || strings.HasSuffix(filePath, ".tar.gz") {
		return nil
	}
	return fmt.Errorf("unsupported file %q: must be .whl or .tar.gz", filepath.Base(filePath))
}

// scanPythonPackageDir returns all .whl and .tar.gz files directly inside dir (non-recursive).
func scanPythonPackageDir(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading directory %q: %w", dir, err)
	}

	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".whl") || strings.HasSuffix(name, ".tar.gz") {
			files = append(files, filepath.Join(dir, name))
		}
	}
	return files, nil
}
