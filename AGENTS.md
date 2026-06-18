# harness-unified-cli — Agent Guide

## What this repo is

A spec-driven Go CLI for Harness. Commands are declared in YAML spec files; the framework wires them into Cobra subcommands at startup. Agents rarely need to touch Go code — most work is adding or editing spec files.

## Build & install

```sh
# Build (requires Task: brew install go-task)
task build                    # outputs bin/harness and bin/harness-har

# Install to GOPATH
go install ./cmd/harness/...

# IMPORTANT: active binary lives at ~/.local/bin/harness, not ~/go/bin/harness
cp $(go env GOPATH)/bin/harness ~/.local/bin/harness
```

## Repository layout

```
cmd/harness/          # main entry point
pkg/
  spec/               # ← primary work area: *.spec.yaml + Go struct types
  registry/           # spec → Cobra command wiring (endpoint.go, verb.go, etc.)
  endpoint/           # HTTP execution: BuildRequest, HTTPFetchFn, paging strategies
  exprenv/            # expr-lang evaluation (flags.*, ctx.*, auth.*, it.*)
  client/             # HTTP client (DoRequest, auth injection)
  cmdctx/             # Ctx struct threaded through all handlers
  format/             # Table/field rendering
  tui/                # Interactive UI picker
modules/har/          # External HAR module (separate go.mod)
```

## How specs work

Each `*.spec.yaml` declares:
1. **`nouns`** — entity types with their `fields` (id, expr, label, width_max, align).
2. **`commands`** — `verb noun[:variant]` pairs wired to `handler_type: endpoint`.

### Noun variant → Cobra subcommand name

```yaml
noun: kg
noun_variant: type      # → cobra command "kg:type"
```

`FullNoun()` returns `"noun:variant"` when variant is set, `"noun"` otherwise.

### Field rendering rules

| Field | When it applies |
|-------|----------------|
| `columns: [...]` | Which fields show in `list` output |
| `fields_subset: [...]` | Which fields show in `get` output (on endpoint, not noun) |
| All noun fields | Available to `get` unless `fields_subset` limits them |

### Path templating

Paths use Go template syntax evaluated by `exprenv`:

```yaml
path: /code/api/v1/repos/{{ctx.parentId}}/branches
```

Available variables: `ctx.id`, `ctx.idParts[N]`, `ctx.parentId`, `auth.account`, `auth.org`, `auth.project`, `flags.*`.

### id_parts vs requires_parentid

- `id_parts: 2` → user passes `<a>/<b>`; available as `ctx.idParts[0]` and `ctx.idParts[1]`. Works for `get`/`execute`/`delete`.
- `requires_parentid: true` → user passes the parent as a positional arg; available as `ctx.parentId`. Used for `list`/`create` where the sub-resource doesn't have its own id yet. **`id_parts` is NOT supported by `list`.**

### expr-lang tips

- `nil` from an expression causes `buildBody` to skip the key entirely (use for optional params).
- `??` is null-coalescing: `it.a ?? it.b ?? {}` resolves the first non-null.
- Array concat: `concat(it.entity_types ?? [], it.event_types ?? [])`.
- Epoch formatting: `epochMs(it.created)` → human-readable timestamp.

### Body params (dotted paths)

```yaml
body_params:
  options.timeout_ms: 'flags.timeout != "" ? flags.timeout : nil'
```

Dots are expanded into nested objects by `setDotPath`.

### Paging strategies

| Strategy | Use case |
|----------|---------|
| `page_header` | Harness v1 REST APIs (total in `X-Total` header) |
| `flat_list` | gRPC-gateway endpoints that return everything at once |
| `page_index` | Older Harness APIs with `pageIndex`/`pageSize` |

### gRPC-gateway endpoints

POST to `/schema-service/grpc/...` or `/query-service/grpc/...`. Always need:

```yaml
method: POST
request_headers:
  x-tenant-id: auth.account
```

An empty `body_params` still sends `{}` (required by gRPC-gateway for POST/PATCH/PUT).

## Adding a new spec file

1. Create `pkg/spec/<module>.spec.yaml`.
2. It is automatically embedded via `//go:embed *.spec.yaml` in `spec.go`.
3. Set `module_type: builtin` and `module_desc: ...`.
4. Add `noun_aliases` (at minimum the plural form) to every noun in the spec.
5. Create `modules/<module>/<module>.help.txt` — see `modules/platform/platform.help.txt` for the format. Include a domain-model section and a `{{nouns}}` placeholder.
6. Create `modules/<module>/<module>.go` — embed the help.txt and call `reg.SetHelpText(helpText)` in `ModuleInit`. See `modules/kg/kg.go` for the minimal pattern.
7. Wire it into `cmd/harness/main-harness.go`: add the import and call `<module>.ModuleInit(reg.Module("<module>"))`.
8. Run `task build && cp $(go env GOPATH)/bin/harness ~/.local/bin/harness` to test.

## Testing commands

```sh
# Always copy after build
task build && cp $(go env GOPATH)/bin/harness ~/.local/bin/harness

# Verify a command
harness list kg:type
harness get kg:type <id>
harness list branch <repo_id>
harness list pr_activity <repo_id> --pr <number>
```

The CLI reads auth from the active profile (typically `~/.harness/profiles.yaml`).

## Current spec files

| File | Commands |
|------|---------|
| `aievals.spec.yaml` | list/get/create/delete for eval_dataset, evaluation, eval_run, eval_metric, eval_metric_set, eval_target, eval_model, eval_suite; execute evaluation:run, execute eval_suite:run |
| `code.spec.yaml` | list/get/create/update/delete repository; list/get/create/update/execute pr (pr:merge, pr:close); list/get/create/delete branch; list/get commit; list/create/delete tag; list pr_activity |
| `kg.spec.yaml` | list/get kg:type; list kg:queryable_type, kg:related_type, kg:connection; execute hql:grammar, hql:validate, hql:run, hql:explain |
| `platform.spec.yaml` | Platform resources (projects, orgs, etc.) |
| `pipeline.spec.yaml` | CI/CD pipelines |
| `core.spec.yaml` | Core resources |

## Security — never put real credentials in code or comments

Do not hardcode into source files, comments, or documentation:
- Account IDs, org IDs, project IDs
- API tokens, OAuth tokens, client secrets
- User emails, UUIDs, or any other PII
- Real hostnames or URLs from live environments (unless they are published public endpoints like `id.harness.io`)

Use placeholder text like `<accountId>`, `<token>`, `<email>` in examples.

## Common pitfalls

- **Binary not updated**: `task build` alone isn't enough — must `cp` to `~/.local/bin/harness`.
- **`list` with `id_parts`**: Not supported. Use `requires_parentid: true` instead.
- **Code API paths**: Use bare repo identifier in path (e.g. `/code/api/v1/repos/{{ctx.parentId}}/branches`). org/project go as query params automatically — do NOT prefix paths with `{{auth.account}}/{{auth.org}}/{{auth.project}}`.
- **`columns` on `get`**: Ignored. Use `fields_subset` on the endpoint to filter `get` output.
- **gRPC oneof fields**: Include all variants in `??` chain (entity_type, event_type, metric_type, view_type, relationship_type, config_type).
