# Context Inference Design

**Date:** 2026-06-18
**Status:** Draft

## Problem

Two related friction points in the CLI today:

1. Commands like `harness list pr`, `harness list branch`, `harness list commit` require a `<repo_id>` positional argument even when the user is already inside a cloned Harness Code repository. There is no way to default it from the environment.

2. When a resource exists in a different org/project than the user's profile default, the API returns a 404 with no helpful guidance. Users must discover and pass `--org`/`--project` manually.

## Solution Overview

Introduce **local context inference**: before erroring on a missing required positional arg, the CLI resolves a `LocalContext` from the user's environment. It also uses that context to automatically set `org`/`project` in the auth resolution chain, eliminating cross-project 404s for users working in cloned repos.

---

## LocalContext

A new package `pkg/gitcontext` owns context resolution. It exposes one function:

```go
func Resolve() (*LocalContext, error)
```

The result type:

```go
type LocalContext struct {
    AccountID string            // feeds into ResolvedAuth
    Org       string            // feeds into ResolvedAuth
    Project   string            // feeds into ResolvedAuth
    Extra     map[string]string // noun-specific values: "repo", "pipeline", etc.
}
```

`AccountID`, `Org`, `Project` are named because they are first-class auth concepts used universally. Noun-specific positional args (`repo`, `pipeline`, `environment`, etc.) live in `Extra` and are looked up by the spec-declared `context_key`.

### Resolution Chain

Resolve() attempts each source in order and returns on the first match:

1. **Git remote URL** — runs `git remote get-url origin` (falls back to `upstream`). Parses a Harness Code URL of the form:
   ```
   https://<host>/code/account/<org>/<project>/<repo>.git
   ```
   Populates `Org`, `Project`, `AccountID` (from the account segment), and `Extra["repo"]`.

2. **Context file** — walks up from the current working directory looking for `.harness/context.yaml`. First file found wins. Format:
   ```yaml
   org: my-org
   project: my-project
   repo: my-repo
   pipeline: my-pipeline   # any extra key supported
   ```
   Named keys `org`, `project`, `account_id` map to struct fields. All other keys go into `Extra`.

3. **No context** — returns `&LocalContext{}` (all zero values). Never returns an error for "not found"; inference simply doesn't happen.

---

## Spec Change: `context_key` on NounDef

One new optional field on `NounDef` in `pkg/spec/spec.go`:

```go
type NounDef struct {
    // existing fields ...
    ContextKey string `yaml:"context_key,omitempty"`
}
```

Declared in spec YAML on nouns whose commands take a required positional arg that can be inferred:

```yaml
nouns:
  - noun: pr
    context_key: repo

  - noun: branch
    context_key: repo

  - noun: commit
    context_key: repo

  - noun: tag
    context_key: repo
```

`context_key` is a key into `LocalContext.Extra`. The registry looks up `Extra[noun.ContextKey]` when the required positional arg is absent. This is spec-driven and noun-scoped — no per-command YAML, no special-casing per noun in Go code.

---

## Auth Resolution Change

`buildCtx` in `pkg/registry/buildctx.go` calls `gitcontext.Resolve()` once per invocation. The result feeds into two places:

### 1. Org/Project in ResolvedAuth

Extended resolution order (highest to lowest precedence):

1. Explicit `--org` / `--project` flags
2. **LocalContext** `Org` / `Project` (new)
3. `HARNESS_ORG` / `HARNESS_PROJECT` env vars
4. Profile defaults

This means a user in a cloned Harness repo automatically gets the correct org/project without passing flags or configuring env vars. Applies to every command — no spec change needed.

### 2. Missing Positional Arg Inference

In the section of `buildCtx` that currently errors on a missing required `parentid` or `id`, add:

```go
if len(args) == 0 && cs.RequiresParentId {
    nd := r.GetNoun(cs.Noun)
    if nd != nil && nd.ContextKey != "" {
        if lc != nil {
            if val := lc.Extra[nd.ContextKey]; val != "" {
                ctx.ParentId = val
                // continue, no error
            }
        }
    }
    if ctx.ParentId == "" {
        return nil, fmt.Errorf(...)  // existing error
    }
}
```

Same pattern applies for `id_parts` commands where the first part is the repo (e.g. `get pr <repo>/<number>` — if only one part is provided and `context_key` fills the missing first part).

Inference is silent in non-TTY mode. In TTY mode, a dim line is printed to stderr:
```
# using repo: my-repo (from git remote)
```

---

## 404 Friendliness

When the HTTP client receives a 404 response and `LocalContext` was empty (inference did not run or produced nothing), append to the error message:

```
404 not found
hint: if this resource is in a different org or project, try --org / --project
      or add a .harness/context.yaml in your project directory
```

This only fires on 404, and only when inference was not the source of org/project — avoids false hints when a resource genuinely doesn't exist.

---

## User-Facing Behavior

| Scenario | Before | After |
|---|---|---|
| `harness list pr` inside a cloned Harness repo | Error: missing `<repo_id>` | Works; infers repo from git remote |
| `harness list pr` with `.harness/context.yaml` | Error: missing `<repo_id>` | Works; infers repo from context file |
| `harness list branch` / `list commit` / `list tag` | Same error | Same inference — all nouns with `context_key: repo` benefit |
| Cross-project repo, profile has wrong org/project | 404 not found | Git remote sets correct org+project automatically |
| Profile org=A, git remote org=B | Profile wins | Git remote wins (explicit flags still highest) |
| Non-Harness remote (GitHub), no context file | N/A | No inference; existing behavior unchanged |
| 404 with no context available | `404 not found` | `404 not found` + hint about `--org`/`--project` |

---

## Files Changed

| File | Change |
|---|---|
| `pkg/gitcontext/gitcontext.go` | New package: `Resolve()`, `LocalContext` |
| `pkg/gitcontext/gitcontext_test.go` | Unit tests for URL parsing and file walking |
| `pkg/spec/spec.go` | Add `ContextKey string` to `NounDef` |
| `pkg/spec/code.spec.yaml` | Add `context_key: repo` to `pr`, `branch`, `commit`, `tag`, `pr_comment`, `pr_activity` nouns |
| `pkg/registry/buildctx.go` | Call `gitcontext.Resolve()`; apply `Org`/`Project` to auth; infer missing positional arg |
| `pkg/client/client.go` | Append 404 hint when inference was not active |

---

## Out of Scope

- `context_key` for non-code nouns (pipeline, environment, service) — format is forward-compatible; wiring is a follow-on
- `harness context set` / `harness context status` commands — useful for visibility but not required for the feature to work
- Support for multiple remotes beyond `origin` / `upstream`
