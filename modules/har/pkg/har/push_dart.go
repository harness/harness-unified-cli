// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package har

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/harness/harness-cli/pkg/cmdctx"
)

// pushDartArtifact uploads a Dart .tar.gz package to Harness Artifact Registry.
//
// ctx.Id      = "<registry>/<name>" (name is unused; name/version come from pubspec.yaml)
// ctx.Args[0] = local .tar.gz or .tgz file path
//
// Upload endpoint: POST {registryURL}/pkg/{accountID}/{registry}/pub/api/packages/versions/new/upload/{uploadId}
func pushDartArtifact(ctx *cmdctx.Ctx) error {
	if len(ctx.Args) == 0 {
		return fmt.Errorf("push dart artifact requires a local file path: push artifact <registry/name> <local-file>")
	}
	localFile := ctx.Args[0]

	// Validate extension
	lower := strings.ToLower(localFile)
	if !strings.HasSuffix(lower, ".tar.gz") && !strings.HasSuffix(lower, ".tgz") {
		return fmt.Errorf("dart package file must be a .tar.gz or .tgz file, got: %s", filepath.Ext(localFile))
	}

	// Stat the file
	fi, err := os.Stat(localFile)
	if err != nil {
		return fmt.Errorf("cannot access %q: %w", localFile, err)
	}
	if fi.IsDir() {
		return fmt.Errorf("%q is a directory; dart push requires a file", localFile)
	}

	// Extract and parse pubspec.yaml from the tarball
	pubspecBytes, err := readFileFromTarGz(localFile, "pubspec.yaml")
	if err != nil {
		return fmt.Errorf("failed to extract pubspec.yaml from tarball: %w", err)
	}

	pkgName, pkgVersion, err := parsePubspecNameVersion(pubspecBytes)
	if err != nil {
		return err
	}
	if pkgName == "" || pkgVersion == "" {
		return fmt.Errorf("pubspec.yaml must contain non-empty 'name' and 'version'")
	}

	// Parse registry from ctx.Id (ignore the name portion — name comes from pubspec)
	registry, _, err := parseRegistryAndName(ctx.Id)
	if err != nil {
		return err
	}

	// Generate upload ID using crypto/rand
	uploadID, err := generateUploadID()
	if err != nil {
		return fmt.Errorf("failed to generate upload ID: %w", err)
	}

	// Build upload URL: /pkg/{accountID}/{registry}/pub/api/packages/versions/new/upload/{uploadId}
	subpath := fmt.Sprintf("%s/pub/api/packages/versions/new/upload/%s", registry, uploadID)
	uploadURL, err := buildPkgURL(ctx.Auth.RegistryURL, ctx.Auth.AccountID, subpath)
	if err != nil {
		return err
	}

	// Open the package file
	f, err := os.Open(localFile)
	if err != nil {
		return fmt.Errorf("opening %q: %w", localFile, err)
	}
	defer f.Close()

	// Build multipart body in memory using bytes.Buffer
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	part, err := mw.CreateFormFile("file", filepath.Base(localFile))
	if err != nil {
		return fmt.Errorf("creating multipart form file: %w", err)
	}
	fileBytes, err := os.ReadFile(localFile)
	if err != nil {
		return fmt.Errorf("reading %q: %w", localFile, err)
	}
	if _, err := part.Write(fileBytes); err != nil {
		return fmt.Errorf("writing file to multipart: %w", err)
	}
	if err := mw.Close(); err != nil {
		return fmt.Errorf("closing multipart writer: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Uploading Dart package %s@%s to registry %s ...\n", pkgName, pkgVersion, registry)

	req, err := http.NewRequest("POST", uploadURL, &buf)
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

	fmt.Fprintf(os.Stderr, "Successfully pushed Dart package '%s@%s' to registry '%s'\n", pkgName, pkgVersion, registry)
	return nil
}

// parsePubspecNameVersion extracts the name and version fields from pubspec.yaml bytes
// using simple line-by-line string parsing (no YAML library dependency).
func parsePubspecNameVersion(data []byte) (name, version string, err error) {
	for _, line := range strings.Split(string(data), "\n") {
		if name == "" {
			if v, ok := extractPubspecField(line, "name"); ok {
				name = v
			}
		}
		if version == "" {
			if v, ok := extractPubspecField(line, "version"); ok {
				version = v
			}
		}
		if name != "" && version != "" {
			break
		}
	}
	if name == "" {
		return "", "", fmt.Errorf("pubspec.yaml is missing required field 'name'")
	}
	if version == "" {
		return "", "", fmt.Errorf("pubspec.yaml is missing required field 'version'")
	}
	return name, version, nil
}

// extractPubspecField checks if line matches "key: value" and returns the trimmed value.
func extractPubspecField(line, key string) (string, bool) {
	prefix := key + ":"
	if !strings.HasPrefix(strings.TrimSpace(line), prefix) {
		return "", false
	}
	// Only match top-level keys (no leading whitespace)
	if line[0] == ' ' || line[0] == '\t' {
		return "", false
	}
	value := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), prefix))
	// Strip optional surrounding quotes
	value = strings.Trim(value, `"'`)
	return value, true
}

// generateUploadID returns a random hex string suitable for use as an upload ID.
func generateUploadID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
