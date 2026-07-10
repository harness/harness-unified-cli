---
name: harness-cli
description: Use when the user wants to inspect, query, or act on Harness resources (pipelines, executions, connectors, orgs/projects, users, secrets, artifacts, etc.) via the `harness` CLI. Triggers on mentions of Harness, pipelines, deployments, CD/CI runs, or when the user references a Harness resource by name without saying "CLI" explicitly.
---

# Harness CLI

`harness` is self-documenting. This skill is not a substitute for reading that
documentation — it is a guide to *how* to read it correctly, plus the mistakes
that are easy to make even when you do.

## Grammar and verb meanings

`harness <verb> <noun> [id] [flags]`. The core verb set below covers almost
everything and has a consistent meaning across every noun. It's closed by
default — but individual modules can be granted extra, module-specific verbs
where a client-side workflow doesn't map to one of these. Don't assume the
core list below is exhaustive for every module — check `get module <name>` or
`get noun <noun>` if a noun's commands don't look like standard CRUD.

- `list` — read-only, returns multiple items, paginated. Safe to run freely.
- `get` — read-only, returns a single item by id. Safe to run freely.
- `create` — write, makes a new resource. Needs a body (`-f file.yaml` or `--set key=value`).
- `update` — write, modifies an existing resource by id (`--set`/`--del`, or `-f`).
- `delete` — write, irreversible removal by id.
- `execute` — write/side-effecting, "do the thing": e.g. trigger a pipeline
  run, approve/reject a pending approval. Not always reversible — treat like
  `create`/`update`/`delete` for confirmation purposes.

`<noun>:<qualifier>` is a variant on the same noun (e.g. `get
pipeline:summary`) — same safety rules as the base verb.

## Golden rule: discover before you construct

Never guess a noun's flags, fields, or columns. Before running an unfamiliar
command:

```sh
harness get module <module>     # domain model + narrative context for a module
harness get noun <noun>         # fields and available verbs for one noun
harness <verb> <noun> --help    # flags for one specific command
```

`get module` is not boilerplate — it contains the domain model (how nouns
relate: e.g. "a pipeline is a definition, an execution is a run instance of
it") and module-specific gotchas. Skipping it and jumping straight to
`--help` gets you the flags but not the mental model, and you will misuse a
flag whose purpose you guessed at.

## Read the full output, not the head of it

Running bare `harness` (or `harness --help`) prints one screen of quick
reference — but do not pipe it through `head` or otherwise truncate it. The
sections at the bottom (paging flags, output flags for get/create/update,
examples) are exactly the parts most likely to get cut off, and they contain
the pagination default below. If you truncate this once and internalize a
partial mental model, you will carry that gap into every command after it.
Same applies to `get noun <noun>` output — read the whole thing, including the
Fields list at the bottom, don't stop after the Commands section.

## Pagination defaults to 20 — a full page doesn't mean "that's all"

`list <noun>` defaults to `--limit 20`, `--offset 0`. If a result has fewer
than 20 items (0-19), that page is complete by definition — no need for
`--all`. But if you get back exactly 20, that's a signal there may be more:
before reasoning about "all X" or doing anything count-sensitive (totals,
"does X exist", diffing), either:

- pass `--all` to fetch everything, or
- pass `--count` to get the true total first, or
- check the `Showing X-Y of Z` footer under the table and compare Z to what
  you fetched.

Don't treat an exactly-20-item result as necessarily the full answer.

## Fields vs. columns — currently two flags, one underlying concept

- `--columns` (list only) controls what's shown in a *table*. Discover valid
  IDs with `--list-columns`.
- `--fields` (get only) extracts tab-separated *field* values for shell
  capture (`id=$(harness get pipeline foo --fields identifier)`). Discover
  valid IDs with `--list-fields`.
- `--list-fields` also appears on `create`/`update`, but there's no paired
  `--fields` extraction flag there — it's discovery for the keys you can pass
  to `--set`/`--del`, not for extracting output.
- All of these draw from the same per-noun field list — the split is only at
  the flag-name layer, scoped by verb. Don't assume a `--columns` ID works
  with `--fields` on a different verb for the same noun without checking;
  check the flag that matches the verb you're running.

## Output formats — pick based on what you actually need

Two different families:

- **`table` / `csv` / `tsv`**: column-based, driven by the noun's field/column
  definitions — a shaped projection of the resource, not the full object.
  Prefer **`tsv`** over `csv` for scripting/parsing — it doesn't have
  CSV's quoting/escaping ambiguity and is easier to split reliably.
- **`json` / `yaml`**: both return the *full* underlying object, not just the
  columns — use these when you need fields that aren't in the default
  column set, or nested/complex data the table view flattens away.
  - `json` includes the full **envelope**: metadata that doesn't round-trip
    through create/update (e.g. create time, update time). Use this when
    you need that metadata, not just the resource body.
  - `yaml` (on `get`) is the object only, no envelope — this is the form to
    edit and pass back to `update -f`.
- `--fields a,b,c`: tab-separated single line of specific field values, built
  for `$(...)` shell capture, not for display or bulk parsing.

## Safety: read vs. mutate

`list` and `get` are always safe to run freely for exploration. `create`,
`update`, `delete`, and `execute` are mutating — confirm with the user before
running these unless they've clearly already authorized the specific action.

## Check auth status for scope, not just "is it logged in"

An unauthenticated command fails loudly on its own — you don't need to
preemptively check for that. But a command run against the *wrong* profile,
account, org, or project can succeed silently and just return the wrong data.
Run `harness auth status` early in a session to confirm you're pointed at the
account/org/project the user actually means, especially before assuming a
"not found" result means a resource doesn't exist anywhere. If auth is
genuinely missing, tell the user and stop — `harness auth login` is
interactive and cannot be driven non-interactively; don't work around it by
guessing env vars or profile names.

## Working with pipelines

Pipelines are the highest-traffic use case for this CLI — executing them,
checking status, and debugging failures. Before doing any of that, run
`harness get module pipeline` — it covers the pipeline/execution/step domain
model and how to drill into a failure (executions, logs, steps, approvals).
Don't try to piece the pipeline workflow together from `--help` output alone.

## Don't hardcode the module/noun catalog

This skill file intentionally doesn't list specific modules or nouns — that
catalog varies by installation and version, and would go stale the moment a
module is added or renamed. Use `list module`, `list noun`, `get module
<name>`, and `get noun <noun>` as the source of truth for what's actually
available, not this file.
