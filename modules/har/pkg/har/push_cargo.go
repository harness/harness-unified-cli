// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package har

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/harness/harness-cli/pkg/cmdctx"
)

// pushCargoArtifact uploads a .crate file to the Harness Artifact Registry using
// the Cargo sparse registry upload protocol.
//
// ctx.Id      = "<registry>/<ignored-name>" — only the registry part is used
// ctx.Args[0] = local .crate file path
//
// The upload endpoint is:
//
//	PUT {registryURL}/pkg/{accountID}/{registry}/cargo/api/v1/crates/new
//
// Body format (Cargo publish wire format):
//
//	[4 bytes LE uint32: metadata JSON length]
//	[N bytes: metadata JSON {"name":"<name>","vers":"<version>"}]
//	[4 bytes LE uint32: .crate file length]
//	[N bytes: .crate file bytes]
func pushCargoArtifact(ctx *cmdctx.Ctx) error {
	if len(ctx.Args) == 0 {
		return fmt.Errorf("push cargo: local file path required as positional argument")
	}
	localFile := ctx.Args[0]

	if !strings.HasSuffix(strings.ToLower(localFile), ".crate") {
		return fmt.Errorf("push cargo: file must have .crate extension, got %q", filepath.Base(localFile))
	}

	fi, err := os.Stat(localFile)
	if err != nil {
		return fmt.Errorf("push cargo: cannot access %q: %w", localFile, err)
	}
	if fi.IsDir() {
		return fmt.Errorf("push cargo: %q is a directory, not a .crate file", localFile)
	}

	// Extract Cargo.toml from the .crate archive to get name and version.
	tomlBytes, err := readFileFromTarGz(localFile, "Cargo.toml")
	if err != nil {
		return fmt.Errorf("push cargo: extracting Cargo.toml from %q: %w", filepath.Base(localFile), err)
	}

	name, version, err := parseCargoToml(tomlBytes)
	if err != nil {
		return fmt.Errorf("push cargo: %w", err)
	}

	// Read the full .crate bytes.
	crateBytes, err := os.ReadFile(localFile)
	if err != nil {
		return fmt.Errorf("push cargo: reading %q: %w", localFile, err)
	}

	// Build the Cargo publish wire-format payload.
	payload, err := buildCargoPayload(name, version, crateBytes)
	if err != nil {
		return fmt.Errorf("push cargo: building payload: %w", err)
	}

	// The registry part of the id is all we need; the crate name/version come from the file.
	registry, _, err := parseRegistryAndName(ctx.Id)
	if err != nil {
		return fmt.Errorf("push cargo: %w", err)
	}

	subpath := fmt.Sprintf("%s/cargo/api/v1/crates/new", registry)
	uploadURL, err := buildPkgURL(ctx.Auth.RegistryURL, ctx.Auth.AccountID, subpath)
	if err != nil {
		return fmt.Errorf("push cargo: building URL: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Uploading %s (%s@%s) → %s ...\n",
		filepath.Base(localFile), name, version, registry)

	req, err := http.NewRequest("PUT", uploadURL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("push cargo: building request: %w", err)
	}
	setAuthHeader(req, ctx.Auth.PATToken)
	req.Header.Set("Content-Type", "application/octet-stream")
	req.ContentLength = int64(len(payload))

	if sums, sumErr := computeFileChecksums(localFile); sumErr == nil {
		setChecksumHeaders(req.Header, sums)
	}

	if _, err := doRequest(newHTTPClient(), req); err != nil {
		return fmt.Errorf("push cargo: upload failed: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Successfully pushed %s@%s to %s\n", name, version, registry)
	return nil
}

// parseCargoToml extracts the package name and version from the contents of a
// Cargo.toml file using simple line-by-line string parsing (no TOML library).
// It looks for lines of the form:
//
//	name = "my-crate"
//	version = "1.2.3"
//
// within the [package] section.
func parseCargoToml(data []byte) (name, version string, err error) {
	inPackage := false
	for _, rawLine := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(rawLine)

		// Track [package] section.
		if strings.HasPrefix(line, "[") {
			inPackage = line == "[package]"
			continue
		}

		if !inPackage {
			continue
		}

		if name == "" {
			if v := extractTomlStringValue(line, "name"); v != "" {
				name = v
			}
		}
		if version == "" {
			if v := extractTomlStringValue(line, "version"); v != "" {
				version = v
			}
		}

		if name != "" && version != "" {
			break
		}
	}

	if name == "" {
		return "", "", fmt.Errorf("Cargo.toml: package name not found")
	}
	if version == "" {
		return "", "", fmt.Errorf("Cargo.toml: package version not found")
	}
	return name, version, nil
}

// extractTomlStringValue parses a line like:  key = "value"
// Returns the unquoted value string, or "" if the line does not match.
func extractTomlStringValue(line, key string) string {
	// key must appear at the start of the (already trimmed) line.
	if !strings.HasPrefix(line, key) {
		return ""
	}
	rest := strings.TrimSpace(line[len(key):])
	if !strings.HasPrefix(rest, "=") {
		return ""
	}
	rest = strings.TrimSpace(rest[1:])
	// Strip surrounding double quotes.
	if len(rest) >= 2 && rest[0] == '"' && rest[len(rest)-1] == '"' {
		return rest[1 : len(rest)-1]
	}
	return ""
}

// buildCargoPayload constructs the binary upload body used by the Cargo
// sparse-registry publish protocol:
//
//	[4 LE bytes: len(metadataJSON)]
//	[metadataJSON bytes]
//	[4 LE bytes: len(crateBytes)]
//	[crateBytes]
func buildCargoPayload(name, version string, crateBytes []byte) ([]byte, error) {
	meta := map[string]string{
		"name": name,
		"vers": version,
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return nil, fmt.Errorf("marshaling metadata: %w", err)
	}

	metaLen := uint32(len(metaJSON))    // #nosec G115
	crateLen := uint32(len(crateBytes)) // #nosec G115

	buf := make([]byte, 4+metaLen+4+crateLen)
	binary.LittleEndian.PutUint32(buf[0:4], metaLen)
	copy(buf[4:], metaJSON)
	binary.LittleEndian.PutUint32(buf[4+metaLen:4+metaLen+4], crateLen)
	copy(buf[4+metaLen+4:], crateBytes)

	return buf, nil
}
