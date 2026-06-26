// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package code

import (
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/harness/harness-cli/pkg/client"
	"github.com/harness/harness-cli/pkg/cmdctx"
)

// harnessUIDRe matches a Harness UID: exactly 22 base64url characters ([A-Za-z0-9_-]).
var harnessUIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{22}$`)

// numericRe matches a bare positive integer (numeric principal ID).
var numericRe = regexp.MustCompile(`^[0-9]+$`)

// resolvePrincipalID resolves an --author flag value to a Code numeric principal
// ID string. Accepts:
//   - email (contains "@")         → resolved via email lookup
//   - Harness UID (22 base64url chars) → resolved via UID lookup
//   - numeric string               → passed through as-is
//   - anything else                → error
func resolvePrincipalID(ctx *cmdctx.Ctx, raw string) (string, error) {
	var id int
	var err error
	switch {
	case strings.Contains(raw, "@"):
		id, err = PrincipalIDFromEmail(ctx, raw)
	case harnessUIDRe.MatchString(raw):
		id, err = PrincipalIDFromUID(ctx, raw)
	case numericRe.MatchString(raw):
		return raw, nil
	default:
		return "", fmt.Errorf("%q is not a valid author: expected an email, a 22-char Harness UID, or a numeric principal ID", raw)
	}
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%d", id), nil
}

var (
	cachedPrincipalID  int
	cachePrincipalOnce sync.Once
	cachePrincipalErr  error
)

// CurrentUserPrincipalID resolves the authenticated user's Code numeric
// principal ID. The result is memoized within a process lifetime so that
// repeated calls (e.g. across pages) only hit the API once.
func CurrentUserPrincipalID(ctx *cmdctx.Ctx) (int, error) {
	cachePrincipalOnce.Do(func() {
		raw, _, err := client.New(ctx).Get("/ng/api/user/currentUser", map[string]string{
			"accountIdentifier": ctx.Auth.AccountID,
		})
		if err != nil {
			cachePrincipalErr = fmt.Errorf("fetching current user: %w", err)
			return
		}
		email := currentUserEmail(raw)
		if email == "" {
			cachePrincipalErr = fmt.Errorf("could not determine current user email")
			return
		}
		id, err := PrincipalIDFromEmail(ctx, email)
		if err != nil {
			cachePrincipalErr = fmt.Errorf("resolving principal for %q: %w", email, err)
			return
		}
		cachedPrincipalID = id
	})
	return cachedPrincipalID, cachePrincipalErr
}

// currentUserEmail extracts the email from a /ng/api/user/currentUser response.
func currentUserEmail(raw any) string {
	m, ok := raw.(map[string]any)
	if !ok {
		return ""
	}
	data, ok := m["data"].(map[string]any)
	if !ok {
		return ""
	}
	email, _ := data["email"].(string)
	return email
}

// principal is a Code-module principal (user, service, or service account).
// Code uses its own numeric id for filtering (e.g. list pr --author), which is
// distinct from the platform UUID (uid).
type principal struct {
	ID          int    `json:"id"`
	UID         string `json:"uid"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
	Type        string `json:"type"`
}

// listPrincipals fetches Code principals matching the given query string.
func listPrincipals(cc *cmdctx.Ctx, query string) ([]principal, error) {
	c := client.New(cc)
	raw, _, err := c.Get("/code/api/v1/principals", map[string]string{
		"query": query,
		"type":  "user",
		"limit": "100",
	})
	if err != nil {
		return nil, fmt.Errorf("listing principals: %w", err)
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("unexpected response type from principals endpoint")
	}
	out := make([]principal, 0, len(items))
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		p := principal{}
		if v, ok := m["id"].(float64); ok {
			p.ID = int(v)
		}
		if v, ok := m["uid"].(string); ok {
			p.UID = v
		}
		if v, ok := m["display_name"].(string); ok {
			p.DisplayName = v
		}
		if v, ok := m["email"].(string); ok {
			p.Email = v
		}
		if v, ok := m["type"].(string); ok {
			p.Type = v
		}
		out = append(out, p)
	}
	return out, nil
}

// PrincipalIDFromEmail resolves an email address to a Code numeric principal ID.
// Returns an error if no match is found or the email matches multiple principals.
func PrincipalIDFromEmail(cc *cmdctx.Ctx, email string) (int, error) {
	principals, err := listPrincipals(cc, email)
	if err != nil {
		return 0, err
	}
	var matches []principal
	for _, p := range principals {
		if p.Email == email {
			matches = append(matches, p)
		}
	}
	switch len(matches) {
	case 0:
		return 0, fmt.Errorf("no Code principal found with email %q", email)
	case 1:
		return matches[0].ID, nil
	default:
		return 0, fmt.Errorf("multiple Code principals found with email %q", email)
	}
}

// PrincipalIDFromUID resolves a platform UUID to a Code numeric principal ID.
// Returns an error if no match is found or the UID matches multiple principals.
func PrincipalIDFromUID(cc *cmdctx.Ctx, uid string) (int, error) {
	// The principals endpoint doesn't support filtering by UID, so we search
	// with an empty query and scan all results. For targeted lookups this is
	// acceptable; avoid using this in completion hot-paths.
	principals, err := listPrincipals(cc, "")
	if err != nil {
		return 0, err
	}
	var matches []principal
	for _, p := range principals {
		if p.UID == uid {
			matches = append(matches, p)
		}
	}
	switch len(matches) {
	case 0:
		return 0, fmt.Errorf("no Code principal found with UID %q", uid)
	case 1:
		return matches[0].ID, nil
	default:
		return 0, fmt.Errorf("multiple Code principals found with UID %q", uid)
	}
}
