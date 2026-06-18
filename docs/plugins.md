# Plugins

## Module Types

Every CLI feature area is a **module**. Modules come in two packaging types:

**Builtin module** — compiled directly into the `harness` binary. No separate binary to install. Examples: `core`, `pipeline`, `platform`, `iacm`, `code`.

**Plugin module** — a separate binary (`harness-<name>`) that the main binary discovers and dispatches to via `exec`. Must be installed separately. Example: `har` (Harness Artifact Registry).

The spec file for every module lives in `pkg/spec/`, regardless of type. This means `harness` always knows the full command surface — including help text and tab completions — even for plugins that aren't installed yet.

---

## When to Use a Plugin

Use a plugin module instead of a builtin when any of these apply:

- **Proprietary code** — the module cannot be open source; a plugin can live in a private repo entirely.
- **CGO or large native dependencies** — linking native libraries would force CGO into the main binary; a plugin isolates that dependency.
- **Independent release cadence** — the module needs to version and ship on its own schedule, not tied to the core binary.
- **Binary size** — large Go dependencies (e.g. container registry clients, Helm libraries) bloat the main binary for all users, even those who never use the module. A plugin keeps those dependencies out of the core binary.

If none of these apply, prefer a builtin — it's simpler to build, install, and distribute.

---

## Spec Fields

Builtin:

```yaml
spec_version: 1
module_type: builtin
module_desc: Pipeline execution, logs, and step management
```

Plugin:

```yaml
spec_version: 1
module_type: plugin
module_desc: Harness Artifact Registry (push and pull artifacts)
external_binary: harness-har
```

`external_binary` is the binary name the main `harness` process looks up on `PATH` (or in the install directory) when dispatching commands for that module.

---

## Repo Layout

```
modules/
  core/              ← builtin (no go.mod)
  pipeline/          ← builtin (no go.mod)
  platform/          ← builtin (no go.mod)
  iacm/              ← builtin (no go.mod)
  code/              ← builtin (no go.mod)
  har/               ← plugin (has go.mod + its own binary)
    go.mod
    cmd/
      harness-har/
        main-harness-har.go
    pkg/
      har/

pkg/
  spec/              ← ALL spec YAML files (builtin + plugin)
    core.spec.yaml
    pipeline.spec.yaml
    har.spec.yaml
    ...
```

A module directory without a `go.mod` is builtin. A module directory with its own `go.mod` and a `cmd/harness-<name>/` entrypoint is a plugin.

---

## How Plugin Dispatch Works

At startup, `harness` loads all spec files. Any spec with `module_type: plugin` registers its `external_binary` name. When a command routes to a plugin module, `harness` execs the named binary, passing the remaining arguments through. The plugin binary runs its own `main`, loads the same spec, and calls `ModuleInit`.

If the plugin binary is not on `PATH`, `harness` prints a clear error with install instructions.

---

## Adding a New Builtin Module

1. Create `modules/<name>/` with a `<name>.go` that exports `ModuleInit(reg registry.ModuleRegistrar)`.
2. Add `pkg/spec/<name>.spec.yaml` with `module_type: builtin`.
3. Call `<name>.ModuleInit(reg.Module("<name>"))` in `cmd/harness/main-harness.go`.
4. Run `task check:specs` to validate.

---

## Adding a New Plugin Module

1. Create `modules/<name>/` with its own `go.mod` (`module github.com/harness/harness-cli/modules/<name>`).
2. Add the binary entrypoint at `modules/<name>/cmd/harness-<name>/main-harness-<name>.go`.
3. The binary's `main` loads the spec and calls `ModuleInit`:
   ```go
   reg := registry.New()
   specloader.LoadSpec(reg, "<name>.spec.yaml")
   <name>.ModuleInit(reg.Module("<name>"))
   rootcmd.SetupAndExecutePluginRootCmd(root, reg, "<name>")
   ```
4. Add `pkg/spec/<name>.spec.yaml` with `module_type: plugin` and `external_binary: harness-<name>`.
5. Add `./modules/<name>` to `go.work` for local development.
6. Add the `replace` directive in the module's `go.mod` for local dev:
   ```
   replace github.com/harness/harness-cli => ../../
   ```
7. Run `task check:specs` to validate.
8. No change to `cmd/harness/main-harness.go` is needed — plugin dispatch is driven by the spec.

---

## Import Rules

| From | May import | Must not import |
|---|---|---|
| `pkg/` | external libs | anything under `modules/` |
| `modules/<name>/` (builtin) | `pkg/`, external libs | other modules |
| `modules/<name>/` (plugin) | root module (`github.com/harness/harness-cli`), external libs | other modules |
| `cmd/harness/` | `pkg/`, builtin modules | plugin modules |

Modules must not import each other.

---

## Release Tags

| Artifact | Tag format | Example |
|---|---|---|
| Core `harness` binary | `core/v<semver>` | `core/v1.4.0` |
| Plugin module (in-repo) | `<name>/v<semver>` | `har/v0.9.1` |

Each binary releases independently on its own tag and cadence.
