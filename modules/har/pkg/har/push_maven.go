// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package har

import (
	"archive/zip"
	"bytes"
	"crypto/md5"
	"crypto/sha1"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/harness/harness-cli/pkg/cmdctx"
)

const defaultMaxConcurrentUploads = 4

// pomXML is a minimal representation of a Maven POM file used for coordinate extraction.
type pomXML struct {
	XMLName    xml.Name   `xml:"project"`
	GroupID    string     `xml:"groupId"`
	ArtifactID string     `xml:"artifactId"`
	Version    string     `xml:"version"`
	Parent     *pomParent `xml:"parent"`
}

type pomParent struct {
	GroupID string `xml:"groupId"`
	Version string `xml:"version"`
}

// mavenCoords holds the three Maven coordinates extracted from a POM file.
type mavenCoords struct {
	GroupID    string
	ArtifactID string
	Version    string
}

// mavenMetadataXML is a minimal representation of maven-metadata.xml.
type mavenMetadataXML struct {
	XMLName    xml.Name        `xml:"metadata"`
	GroupID    string          `xml:"groupId"`
	ArtifactID string          `xml:"artifactId"`
	Versioning mavenVersioning `xml:"versioning"`
}

type mavenVersioning struct {
	Release     string   `xml:"release,omitempty"`
	Versions    []string `xml:"versions>version"`
	LastUpdated string   `xml:"lastUpdated,omitempty"`
}

// pushMavenArtifact implements "push artifact" for Maven (.jar/.war) packages.
//
// Required:
//
//	ctx.Args[0]           = local .jar or .war file path
//	--pom-file flag       = path to the project-level pom.xml or .pom file
//
// The function:
//  1. Validates the jar/war and pom file paths.
//  2. Parses the POM to extract groupId, artifactId, version.
//  3. Computes MD5 and SHA1 checksums for both files in memory.
//  4. Uploads jar, pom, jar.md5, jar.sha1, pom.md5, pom.sha1 sequentially via PUT.
//  5. Fetches or creates maven-metadata.xml and uploads the updated version.
func pushMavenArtifact(ctx *cmdctx.Ctx) error {
	if len(ctx.Args) == 0 {
		return fmt.Errorf("push maven artifact requires a local file path: push artifact <registry/name> <local-file> --pom-file <pom>")
	}
	localFile := ctx.Args[0]

	pomPath := cmdctx.GetString(ctx.FlagValues, "pom-file")
	if pomPath == "" {
		return fmt.Errorf("--pom-file is required for maven artifact push")
	}

	registry, _, err := parseRegistryAndName(ctx.Id)
	if err != nil {
		return err
	}

	// Validate jar/war file
	lower := strings.ToLower(localFile)
	if !strings.HasSuffix(lower, ".jar") && !strings.HasSuffix(lower, ".war") {
		return fmt.Errorf("maven artifact must be a .jar or .war file, got %q", filepath.Base(localFile))
	}
	if fi, err := os.Stat(localFile); err != nil {
		return fmt.Errorf("cannot access artifact file %q: %w", localFile, err)
	} else if fi.IsDir() {
		return fmt.Errorf("artifact file path %q is a directory", localFile)
	}

	// Validate pom file
	pomLower := strings.ToLower(pomPath)
	if !strings.HasSuffix(pomLower, ".pom") && !strings.HasSuffix(pomLower, ".xml") {
		return fmt.Errorf("pom file must be a .pom or .xml file, got %q", filepath.Base(pomPath))
	}
	if fi, err := os.Stat(pomPath); err != nil {
		return fmt.Errorf("cannot access pom file %q: %w", pomPath, err)
	} else if fi.IsDir() {
		return fmt.Errorf("pom file path %q is a directory", pomPath)
	}

	// Parse POM to get Maven coordinates
	coords, err := parsePomFile(pomPath)
	if err != nil {
		return fmt.Errorf("failed to parse pom file: %w", err)
	}

	// Cross-validate coords embedded in the jar/war against the provided pom
	jarCoords, err := parseMavenArtifactCoords(localFile)
	if err != nil {
		return fmt.Errorf("failed to read maven coords from artifact: %w", err)
	}
	if err := compareMavenCoords(coords, jarCoords); err != nil {
		return fmt.Errorf("artifact and pom file do not match: %w", err)
	}

	// SNAPSHOT versions are not supported
	if strings.HasSuffix(strings.ToUpper(coords.Version), "-SNAPSHOT") {
		return fmt.Errorf("SNAPSHOT version upload is not supported")
	}

	maxConcurrent := cmdctx.GetInt(ctx.FlagValues, "max-concurrent-uploads")
	if maxConcurrent <= 0 {
		maxConcurrent = defaultMaxConcurrentUploads
	}

	groupPath := strings.ReplaceAll(coords.GroupID, ".", "/")

	// Compute the canonical pom filename as Maven expects: artifactId-version.pom
	pomFilename := coords.ArtifactID + "-" + coords.Version + ".pom"
	jarFilename := filepath.Base(localFile)

	// Pre-compute checksums (needed before building the job list)
	jarMD5, jarSHA1, err := computeChecksums(localFile)
	if err != nil {
		return fmt.Errorf("failed to compute checksums for %s: %w", jarFilename, err)
	}
	pomMD5, pomSHA1, err := computeChecksums(pomPath)
	if err != nil {
		return fmt.Errorf("failed to compute checksums for pom: %w", err)
	}

	type uploadJob struct {
		label       string
		fromDisk    bool
		localPath   string
		filename    string
		content     []byte
		contentType string
	}

	jobs := []uploadJob{
		{label: jarFilename, fromDisk: true, localPath: localFile, filename: jarFilename, contentType: "application/octet-stream"},
		{label: pomFilename, fromDisk: true, localPath: pomPath, filename: pomFilename, contentType: "application/octet-stream"},
		{label: jarFilename + ".md5", filename: jarFilename + ".md5", content: []byte(jarMD5), contentType: "text/plain"},
		{label: jarFilename + ".sha1", filename: jarFilename + ".sha1", content: []byte(jarSHA1), contentType: "text/plain"},
		{label: pomFilename + ".md5", filename: pomFilename + ".md5", content: []byte(pomMD5), contentType: "text/plain"},
		{label: pomFilename + ".sha1", filename: pomFilename + ".sha1", content: []byte(pomSHA1), contentType: "text/plain"},
	}

	client := newHTTPClient()

	sem := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup
	errs := make([]error, len(jobs))

	for i, job := range jobs {
		wg.Add(1)
		go func(i int, job uploadJob) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			fmt.Fprintf(os.Stderr, "Uploading %s ...\n", job.label)
			var uploadErr error
			if job.fromDisk {
				uploadErr = mavenPutFile(ctx, client, registry, groupPath, coords, job.filename, job.localPath, job.contentType)
			} else {
				uploadErr = mavenPutBytes(ctx, client, registry, groupPath, coords, job.filename, job.content, job.contentType)
			}
			if uploadErr != nil {
				errs[i] = fmt.Errorf("failed to upload %s: %w", job.label, uploadErr)
			}
		}(i, job)
	}
	wg.Wait()

	for _, e := range errs {
		if e != nil {
			return e
		}
	}

	// Fetch or create maven-metadata.xml and upload updated version
	fmt.Fprintf(os.Stderr, "Updating maven-metadata.xml ...\n")
	if err := updateMavenMetadata(ctx, client, registry, groupPath, coords); err != nil {
		return fmt.Errorf("failed to update maven-metadata.xml: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Successfully pushed Maven artifact %s:%s:%s to %s\n",
		coords.GroupID, coords.ArtifactID, coords.Version, registry)
	return nil
}

// parseMavenArtifactCoords extracts Maven coordinates from inside a .jar or .war zip archive.
// It tries pom.properties first (faster), then falls back to pom.xml.
func parseMavenArtifactCoords(path string) (*mavenCoords, error) {
	r, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("opening archive %q: %w", path, err)
	}
	defer r.Close()

	// Try pom.properties first
	for _, f := range r.File {
		if !strings.HasSuffix(f.Name, "pom.properties") || !strings.Contains(f.Name, "META-INF/maven/") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("opening pom.properties: %w", err)
		}
		data, readErr := io.ReadAll(rc)
		rc.Close()
		if readErr != nil {
			return nil, fmt.Errorf("reading pom.properties: %w", readErr)
		}
		props := make(map[string]string)
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if kv := strings.SplitN(line, "=", 2); len(kv) == 2 {
				props[kv[0]] = kv[1]
			}
		}
		g, a, v := props["groupId"], props["artifactId"], props["version"]
		if g == "" || a == "" || v == "" {
			return nil, fmt.Errorf("incomplete pom.properties in %q", path)
		}
		return &mavenCoords{GroupID: g, ArtifactID: a, Version: v}, nil
	}

	// Fall back to pom.xml inside the archive
	for _, f := range r.File {
		if !strings.HasSuffix(f.Name, "pom.xml") || !strings.Contains(f.Name, "META-INF/maven/") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("opening embedded pom.xml: %w", err)
		}
		data, readErr := io.ReadAll(rc)
		rc.Close()
		if readErr != nil {
			return nil, fmt.Errorf("reading embedded pom.xml: %w", readErr)
		}
		var pom pomXML
		if err := xml.Unmarshal(data, &pom); err != nil {
			return nil, fmt.Errorf("parsing embedded pom.xml: %w", err)
		}
		g := strings.TrimSpace(pom.GroupID)
		if g == "" && pom.Parent != nil {
			g = strings.TrimSpace(pom.Parent.GroupID)
		}
		v := strings.TrimSpace(pom.Version)
		if v == "" && pom.Parent != nil {
			v = strings.TrimSpace(pom.Parent.Version)
		}
		a := strings.TrimSpace(pom.ArtifactID)
		if g == "" || a == "" || v == "" {
			return nil, fmt.Errorf("incomplete coordinates in embedded pom.xml")
		}
		return &mavenCoords{GroupID: g, ArtifactID: a, Version: v}, nil
	}

	return nil, fmt.Errorf("maven metadata not found inside %q", path)
}

// compareMavenCoords returns an error if the two coordinate sets disagree on any field.
func compareMavenCoords(fromPom, fromArtifact *mavenCoords) error {
	if fromPom.GroupID != fromArtifact.GroupID {
		return fmt.Errorf("groupId mismatch: pom=%q, artifact=%q", fromPom.GroupID, fromArtifact.GroupID)
	}
	if fromPom.ArtifactID != fromArtifact.ArtifactID {
		return fmt.Errorf("artifactId mismatch: pom=%q, artifact=%q", fromPom.ArtifactID, fromArtifact.ArtifactID)
	}
	if fromPom.Version != fromArtifact.Version {
		return fmt.Errorf("version mismatch: pom=%q, artifact=%q", fromPom.Version, fromArtifact.Version)
	}
	return nil
}

// parsePomFile reads and parses a POM XML file, extracting the Maven coordinates.
// It handles the case where groupId or version are inherited from a <parent> block.
func parsePomFile(pomPath string) (*mavenCoords, error) {
	data, err := os.ReadFile(pomPath)
	if err != nil {
		return nil, fmt.Errorf("reading pom file: %w", err)
	}

	var pom pomXML
	if err := xml.Unmarshal(data, &pom); err != nil {
		return nil, fmt.Errorf("parsing pom XML: %w", err)
	}

	groupID := strings.TrimSpace(pom.GroupID)
	if groupID == "" && pom.Parent != nil {
		groupID = strings.TrimSpace(pom.Parent.GroupID)
	}

	version := strings.TrimSpace(pom.Version)
	if version == "" && pom.Parent != nil {
		version = strings.TrimSpace(pom.Parent.Version)
	}

	artifactID := strings.TrimSpace(pom.ArtifactID)

	if groupID == "" {
		return nil, fmt.Errorf("groupId not found in pom")
	}
	if artifactID == "" {
		return nil, fmt.Errorf("artifactId not found in pom")
	}
	if version == "" {
		return nil, fmt.Errorf("version not found in pom")
	}

	return &mavenCoords{
		GroupID:    groupID,
		ArtifactID: artifactID,
		Version:    version,
	}, nil
}

// computeChecksums reads the file at path and returns its hex-encoded MD5 and SHA1 digests.
func computeChecksums(path string) (md5sum, sha1sum string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", fmt.Errorf("opening %q: %w", path, err)
	}
	defer f.Close()

	h5 := md5.New()
	h1 := sha1.New()

	if _, err := io.Copy(io.MultiWriter(h5, h1), f); err != nil {
		return "", "", fmt.Errorf("hashing %q: %w", path, err)
	}

	return hex.EncodeToString(h5.Sum(nil)), hex.EncodeToString(h1.Sum(nil)), nil
}

// mavenPutFile uploads the file at localPath to the Maven repository path for filename.
func mavenPutFile(ctx *cmdctx.Ctx, client *http.Client, registry, groupPath string, coords *mavenCoords, filename, localPath, contentType string) error {
	f, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("opening %q: %w", localPath, err)
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat %q: %w", localPath, err)
	}

	uploadURL, err := mavenFileURL(ctx, registry, groupPath, coords, filename)
	if err != nil {
		return err
	}

	req, err := http.NewRequest("PUT", uploadURL, f)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	setAuthHeader(req, ctx.Auth.PATToken)
	req.Header.Set("Content-Type", contentType)
	req.ContentLength = fi.Size()

	if _, err := doRequest(client, req); err != nil {
		return err
	}
	return nil
}

// mavenPutBytes uploads an in-memory byte slice to the Maven repository path for filename.
func mavenPutBytes(ctx *cmdctx.Ctx, client *http.Client, registry, groupPath string, coords *mavenCoords, filename string, content []byte, contentType string) error {
	fmt.Fprintf(os.Stderr, "Uploading %s ...\n", filename)

	uploadURL, err := mavenFileURL(ctx, registry, groupPath, coords, filename)
	if err != nil {
		return err
	}

	req, err := http.NewRequest("PUT", uploadURL, bytes.NewReader(content))
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	setAuthHeader(req, ctx.Auth.PATToken)
	req.Header.Set("Content-Type", contentType)
	req.ContentLength = int64(len(content))

	if _, err := doRequest(client, req); err != nil {
		return err
	}
	return nil
}

// mavenFileURL builds the upload URL for a single Maven artifact file.
//
// Path pattern: {registryURL}/pkg/{accountID}/{registry}/maven/{groupPath}/{artifactId}/{version}/{filename}
func mavenFileURL(ctx *cmdctx.Ctx, registry, groupPath string, coords *mavenCoords, filename string) (string, error) {
	subpath := fmt.Sprintf("%s/maven/%s/%s/%s/%s",
		registry, groupPath, coords.ArtifactID, coords.Version, filename)
	return buildPkgURL(ctx.Auth.RegistryURL, ctx.Auth.AccountID, subpath)
}

// mavenMetadataURL builds the URL for maven-metadata.xml (no version component).
//
// Path pattern: {registryURL}/pkg/{accountID}/{registry}/maven/{groupPath}/{artifactId}/maven-metadata.xml
func mavenMetadataURL(ctx *cmdctx.Ctx, registry, groupPath string, coords *mavenCoords) (string, error) {
	subpath := fmt.Sprintf("%s/maven/%s/%s/maven-metadata.xml",
		registry, groupPath, coords.ArtifactID)
	return buildPkgURL(ctx.Auth.RegistryURL, ctx.Auth.AccountID, subpath)
}

// updateMavenMetadata fetches the existing maven-metadata.xml (if any), adds the new version,
// and uploads the updated file along with its MD5 and SHA1 checksums.
// maven-metadata.xml lives at the artifact level (no version path segment).
func updateMavenMetadata(ctx *cmdctx.Ctx, client *http.Client, registry, groupPath string, coords *mavenCoords) error {
	metaURL, err := mavenMetadataURL(ctx, registry, groupPath, coords)
	if err != nil {
		return err
	}

	// Attempt to fetch existing metadata
	var meta *mavenMetadataXML
	getReq, err := http.NewRequest("GET", metaURL, nil)
	if err != nil {
		return fmt.Errorf("building metadata GET request: %w", err)
	}
	setAuthHeader(getReq, ctx.Auth.PATToken)

	getResp, err := client.Do(getReq)
	if err != nil {
		return fmt.Errorf("fetching maven-metadata.xml: %w", err)
	}
	defer getResp.Body.Close()
	body, _ := io.ReadAll(getResp.Body)

	switch getResp.StatusCode {
	case http.StatusOK:
		var existing mavenMetadataXML
		if xmlErr := xml.Unmarshal(body, &existing); xmlErr == nil {
			meta = &existing
		}
		// If unmarshal fails, fall through to create fresh below
	case http.StatusNotFound:
		// No metadata yet — will create fresh below
	default:
		return fmt.Errorf("unexpected status %d fetching maven-metadata.xml", getResp.StatusCode)
	}

	// Create fresh metadata if none exists or XML parsing failed
	if meta == nil {
		meta = &mavenMetadataXML{
			GroupID:    coords.GroupID,
			ArtifactID: coords.ArtifactID,
		}
	}

	// Add or refresh the new version
	addVersionToMetadata(meta, coords.Version)

	// Serialise to XML
	xmlBytes, err := marshalMavenMetadata(meta)
	if err != nil {
		return err
	}

	// Upload maven-metadata.xml directly to the artifact-level URL (no version segment)
	fmt.Fprintf(os.Stderr, "Uploading maven-metadata.xml ...\n")
	if err := putBytesToURL(ctx, client, metaURL, xmlBytes, "application/xml"); err != nil {
		return fmt.Errorf("uploading maven-metadata.xml: %w", err)
	}

	// Compute checksums for maven-metadata.xml
	h5 := md5.New()
	h1 := sha1.New()
	io.MultiWriter(h5, h1).Write(xmlBytes) //nolint:errcheck
	md5sum := hex.EncodeToString(h5.Sum(nil))
	sha1sum := hex.EncodeToString(h1.Sum(nil))

	subpathMD5 := fmt.Sprintf("%s/maven/%s/%s/maven-metadata.xml.md5", registry, groupPath, coords.ArtifactID)
	md5URL, _ := buildPkgURL(ctx.Auth.RegistryURL, ctx.Auth.AccountID, subpathMD5)
	subpathSHA1 := fmt.Sprintf("%s/maven/%s/%s/maven-metadata.xml.sha1", registry, groupPath, coords.ArtifactID)
	sha1URL, _ := buildPkgURL(ctx.Auth.RegistryURL, ctx.Auth.AccountID, subpathSHA1)

	if err := putBytesToURL(ctx, client, md5URL, []byte(md5sum), "text/plain"); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to upload maven-metadata.xml.md5: %v\n", err)
	}
	if err := putBytesToURL(ctx, client, sha1URL, []byte(sha1sum), "text/plain"); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to upload maven-metadata.xml.sha1: %v\n", err)
	}

	return nil
}

// putBytesToURL performs a PUT of content to the given URL.
func putBytesToURL(ctx *cmdctx.Ctx, client *http.Client, targetURL string, content []byte, contentType string) error {
	req, err := http.NewRequest("PUT", targetURL, bytes.NewReader(content))
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	setAuthHeader(req, ctx.Auth.PATToken)
	req.Header.Set("Content-Type", contentType)
	req.ContentLength = int64(len(content))

	if _, err := doRequest(client, req); err != nil {
		return err
	}
	return nil
}

// addVersionToMetadata adds version to the metadata's version list (if not already present)
// and updates the release and lastUpdated fields.
func addVersionToMetadata(m *mavenMetadataXML, version string) {
	for _, v := range m.Versioning.Versions {
		if v == version {
			m.Versioning.Release = version
			m.Versioning.LastUpdated = time.Now().UTC().Format("20060102150405")
			return
		}
	}
	m.Versioning.Versions = append(m.Versioning.Versions, version)
	m.Versioning.Release = version
	m.Versioning.LastUpdated = time.Now().UTC().Format("20060102150405")
}

// marshalMavenMetadata serialises a mavenMetadataXML struct to XML bytes with the standard XML header.
func marshalMavenMetadata(m *mavenMetadataXML) ([]byte, error) {
	body, err := xml.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshalling maven-metadata.xml: %w", err)
	}
	header := []byte(xml.Header)
	return append(header, body...), nil
}
