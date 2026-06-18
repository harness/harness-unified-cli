// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package har

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/harness/harness-cli/pkg/cmdctx"
)

// pushGoArtifact uploads a Go module package to Harness Artifact Registry.
//
// ctx.Id      = "<registry>/<name>" where name is not used directly (registry identifies the target)
// ctx.Args[0] = local directory path containing go.mod (option a), or ignored when all three
//
//	--mod-file / --info-file / --zip-file flags are supplied (option b).
//
// Required flag: --version  (e.g. "v1.2.3")
// Optional flags: --mod-file, --info-file, --zip-file  (pre-built files; skips local generation)
//
// Upload endpoint: POST {registryURL}/pkg/{accountID}/{registry}/go/upload
// Multipart fields: mod, info, zip
func pushGoArtifact(ctx *cmdctx.Ctx) error {
	registry, _, err := parseRegistryAndName(ctx.Id)
	if err != nil {
		return err
	}

	version := cmdctx.GetString(ctx.FlagValues, "version")
	if version == "" {
		return fmt.Errorf("--version is required for go artifact push")
	}

	modFile := cmdctx.GetString(ctx.FlagValues, "mod-file")
	infoFile := cmdctx.GetString(ctx.FlagValues, "info-file")
	zipFile := cmdctx.GetString(ctx.FlagValues, "zip-file")

	var modData, infoData, zipData []byte

	allProvided := modFile != "" && infoFile != "" && zipFile != ""

	if allProvided {
		// Option (b): use caller-supplied files directly.
		modData, err = os.ReadFile(modFile)
		if err != nil {
			return fmt.Errorf("reading --mod-file %q: %w", modFile, err)
		}
		infoData, err = os.ReadFile(infoFile)
		if err != nil {
			return fmt.Errorf("reading --info-file %q: %w", infoFile, err)
		}
		zipData, err = os.ReadFile(zipFile)
		if err != nil {
			return fmt.Errorf("reading --zip-file %q: %w", zipFile, err)
		}
	} else {
		// Option (a): generate from a source directory.
		if len(ctx.Args) == 0 {
			return fmt.Errorf("push go artifact requires a source directory: push artifact <registry/name> <dir> --version <version>")
		}
		srcDir := ctx.Args[0]

		modData, infoData, zipData, err = buildGoPackageFiles(srcDir, version)
		if err != nil {
			return err
		}
	}

	// Build multipart body in memory.
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)

	type field struct {
		name     string
		filename string
		data     []byte
	}
	fields := []field{
		{"mod", version + ".mod", modData},
		{"info", version + ".info", infoData},
		{"zip", version + ".zip", zipData},
	}

	for _, f := range fields {
		part, createErr := mw.CreateFormFile(f.name, f.filename)
		if createErr != nil {
			return fmt.Errorf("creating multipart field %q: %w", f.name, createErr)
		}
		if _, copyErr := io.Copy(part, bytes.NewReader(f.data)); copyErr != nil {
			return fmt.Errorf("writing multipart field %q: %w", f.name, copyErr)
		}
	}

	if closeErr := mw.Close(); closeErr != nil {
		return fmt.Errorf("finalizing multipart body: %w", closeErr)
	}

	// Build upload URL: POST /pkg/{accountID}/{registry}/go/upload
	subpath := fmt.Sprintf("%s/go/upload", registry)
	uploadURL, err := buildPkgURL(ctx.Auth.RegistryURL, ctx.Auth.AccountID, subpath)
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "Uploading Go module %s to %s ...\n", version, registry)

	req, err := http.NewRequest("POST", uploadURL, &body)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	setAuthHeader(req, ctx.Auth.PATToken)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	if _, doErr := doRequest(newHTTPClient(), req); doErr != nil {
		return fmt.Errorf("upload failed: %w", doErr)
	}

	fmt.Fprintf(os.Stderr, "Successfully pushed Go module %s to %s\n", version, ctx.Id)
	return nil
}

// buildGoPackageFiles generates the three Go module proxy files (.mod, .info, .zip)
// from a source directory.  It reads the go.mod file to obtain the module path, then:
//   - .mod  – the raw go.mod content
//   - .info – JSON {"Version":"<version>","Time":"<RFC3339>"}
//   - .zip  – a zip archive where every file is stored as <module>@<version>/<relpath>
func buildGoPackageFiles(srcDir, version string) (modData, infoData, zipData []byte, err error) {
	// Validate directory.
	di, statErr := os.Stat(srcDir)
	if statErr != nil {
		return nil, nil, nil, fmt.Errorf("cannot access source directory %q: %w", srcDir, statErr)
	}
	if !di.IsDir() {
		return nil, nil, nil, fmt.Errorf("%q is not a directory", srcDir)
	}

	// Read go.mod.
	goModPath := filepath.Join(srcDir, "go.mod")
	modData, err = os.ReadFile(goModPath)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("reading go.mod from %q: %w", srcDir, err)
	}

	// Extract module path from go.mod (first "module ..." line).
	modulePath, parseErr := parseModulePath(modData)
	if parseErr != nil {
		return nil, nil, nil, fmt.Errorf("parsing go.mod: %w", parseErr)
	}

	// Build .info JSON.
	type infoJSON struct {
		Version string `json:"Version"`
		Time    string `json:"Time"`
	}
	info := infoJSON{
		Version: version,
		Time:    time.Now().UTC().Format(time.RFC3339),
	}
	infoData, err = json.Marshal(info)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("marshalling info JSON: %w", err)
	}

	// Build .zip archive in memory.
	var zipBuf bytes.Buffer
	zw := zip.NewWriter(&zipBuf)
	prefix := fmt.Sprintf("%s@%s/", modulePath, version)

	walkErr := filepath.Walk(srcDir, func(path string, fi os.FileInfo, walkEntryErr error) error {
		if walkEntryErr != nil {
			return walkEntryErr
		}
		if fi.IsDir() {
			return nil
		}

		rel, relErr := filepath.Rel(srcDir, path)
		if relErr != nil {
			return fmt.Errorf("computing relative path for %q: %w", path, relErr)
		}

		entryName := prefix + filepath.ToSlash(rel)

		w, createErr := zw.Create(entryName)
		if createErr != nil {
			return fmt.Errorf("creating zip entry %q: %w", entryName, createErr)
		}

		f, openErr := os.Open(path)
		if openErr != nil {
			return fmt.Errorf("opening %q: %w", path, openErr)
		}
		defer f.Close()

		if _, copyErr := io.Copy(w, f); copyErr != nil {
			return fmt.Errorf("writing zip entry %q: %w", entryName, copyErr)
		}
		return nil
	})
	if walkErr != nil {
		return nil, nil, nil, fmt.Errorf("creating zip archive: %w", walkErr)
	}

	if closeErr := zw.Close(); closeErr != nil {
		return nil, nil, nil, fmt.Errorf("finalising zip archive: %w", closeErr)
	}
	zipData = zipBuf.Bytes()

	return modData, infoData, zipData, nil
}

// parseModulePath extracts the module path from raw go.mod content by scanning for the
// first "module <path>" directive line.  It does not require golang.org/x/mod/modfile.
func parseModulePath(goModContent []byte) (string, error) {
	for _, line := range strings.Split(string(goModContent), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "module ") {
			continue
		}
		path := strings.TrimSpace(strings.TrimPrefix(line, "module "))
		// Strip inline comments.
		if idx := strings.Index(path, "//"); idx >= 0 {
			path = strings.TrimSpace(path[:idx])
		}
		if path == "" {
			return "", fmt.Errorf("empty module path in go.mod")
		}
		return path, nil
	}
	return "", fmt.Errorf("no module directive found in go.mod")
}
