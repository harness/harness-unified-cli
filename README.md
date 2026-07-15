<div align="center">

# Harness CLI 3.0

**A unified, spec-driven CLI for the entire Harness platform. Built for Humans and Agents, supercharging the Developer Experience across Harness ecosystem.**

Manage pipelines, artifacts, code, infrastructure, feature flags, governance, and platform resources
with a single consistent grammar.

[![License: Apache 2.0](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)
[![Made with Go](https://img.shields.io/badge/Made_with-Go-00ADD8.svg?logo=go)](https://go.dev)
[![Platform: macOS · Linux](https://img.shields.io/badge/Platform-macOS_·_Linux-lightgrey.svg)](#install)
[![Releases](https://img.shields.io/badge/Downloads-GitHub_Releases-brightgreen.svg)](https://github.com/harness/cli/releases)

[Install](#-install) ·
[Quickstart](#-quickstart) ·
[Grammar](#-grammar) ·
[Discovery](#-discovery) ·
[Commands](#-commands) ·
[Output](#-output-formats) ·
[Configuration](#-configuration) ·
[Contributing](#-contributing)

</div>

---

## Table of Contents

- [Why Harness CLI?](#-why-harness-cli)
- [Install](#-install)
- [Upgrade](#-upgrade)
- [Shell Completions](#-shell-completions)
- [Quickstart](#-quickstart)
- [Grammar](#-grammar)
- [Authentication](#-authentication)
- [Discovery](#-discovery)
- [Modules & Commands](#-commands)
- [Output Formats](#-output-formats)
- [Profiles & Scope](#-profiles--scope)
- [Configuration](#-configuration)
- [Global Flags](#-global-flags)
- [Exit Codes](#-exit-codes)
- [Build from Source](#-build-from-source)
- [Contributing](#-contributing)
- [License](#-license)

---

## ✨ Why Harness CLI?

- **One grammar, every resource** — `harness <verb> <noun>` works identically across pipelines, code, artifacts, IaC, feature flags, governance, and platform.
- **Spec-driven** — commands are declared in YAML specs and wired at startup, so new resources arrive without waiting on custom code paths.
- **Self-describing** — every module, noun, field, and verb is queryable at runtime with `list module`, `get module`, `list noun --matrix`, and `get noun`.
- **Human and machine friendly** — the same command outputs a colored table for you, or JSON / JSONL / YAML / CSV / TSV / Markdown for scripts and agents.
- **Interactive when you want, headless when you don't** — TUI wizards for onboarding and picking, non-interactive flags for CI, and `HARNESS_API_KEY` for zero-config env-var auth.
- **Live log streaming** — follow pipeline executions with real-time SSE-based log tailing.
- **Tab completion that talks to the API** — completions for IDs return live `id<tab>Name` suggestions.
- **Multi-account, multi-profile** — named profiles let you jump between accounts, orgs, and projects on the same shell.
- **Agent-friendly** — detects and reports the coding agent (Claude Code, Cursor, Gemini CLI, Codex, Cline, and more) so operators know how the CLI is being driven.

---

## 📦 Install

### Recommended: one-line installer

```sh
curl -fsSL https://raw.githubusercontent.com/harness/cli/main/install.sh | sh
```

The installer will:

- Download the latest `harness-bundle` for your platform (macOS and Linux, `amd64` / `arm64`).
- Install the `harness` and `harness-har` binaries to `~/.local/bin` (override with `--install-dir`).
- Optionally add `~/.local/bin` to your `PATH` and enable shell completions.

### Installer flags

| Flag                   | Description                                                    |
| ---------------------- | -------------------------------------------------------------- |
| `--install-dir <path>` | Override the install directory (default: `~/.local/bin`)       |
| `--core`               | Install only the `harness` binary (skip `harness-har`)         |
| `--non-interactive`    | Skip all prompts (useful for CI, Docker, provisioning scripts) |
| `--no-verify`          | Skip checksum verification                                     |

> [!TIP]
> When passing flags through a pipe, use `sh -s --` — `-s` tells `sh` to read from stdin, and `--` separates `sh`'s own options from the installer flags.

```sh
# install core + har bundle (default)
curl -fsSL https://raw.githubusercontent.com/harness/cli/main/install.sh | sh

# install harness core only (skip harness-har)
curl -fsSL https://raw.githubusercontent.com/harness/cli/main/install.sh | sh -s -- --core

# non-interactive install to a custom directory
curl -fsSL https://raw.githubusercontent.com/harness/cli/main/install.sh | sh -s -- --non-interactive --install-dir /usr/local/bin
```

### Manual install

Prefer to install by hand? Download an archive from [GitHub Releases](https://github.com/harness/cli/releases) and place the binaries on your `PATH`. Both `tar.gz` bundles (core + `har`) and per-binary archives are published for `linux_amd64`, `linux_arm64`, `darwin_amd64`, and `darwin_arm64`.

---

## 🔄 Upgrade

The CLI can upgrade itself in place:

```sh
harness install cli                  # upgrade to latest
harness install cli --version v1.2.3 # install a specific version
harness install cli --check          # print the resolved version without installing (exits 1 if not found)
harness install cli --force          # reinstall even if already up to date
harness install cli --core-only      # skip module updates
```

| Flag                   | Description                                                          |
| ---------------------- | -------------------------------------------------------------------- |
| `--version <v>`        | Version to install (default: `latest`)                               |
| `--install-dir <path>` | Directory to install into (default: `~/.local/bin`)                  |
| `--force`              | Install even if the current version is already up to date            |
| `--check`              | Print the resolved version without installing; exits 1 if not found  |
| `--core-only`          | Only install the core binary, skip module updates                    |

External modules are managed the same way:

```sh
harness install module har           # install the Artifact Registry plugin
```

---

## ⌨️ Shell Completions

Tab-completion is fully wired and hits the live API where useful — completions for IDs return `id<tab>Name` pairs.

**Zsh**

```sh
source <(harness completion zsh)
```

**Bash**

```sh
source <(harness completion bash)
```

Add the appropriate line to your `.zshrc` or `.bashrc` to make it permanent. The installer can do this for you.

---

## 🚀 Quickstart

```sh
# 1. Log in (interactive TUI)
harness auth login

# 2. See the shape of the world
harness list module
harness list noun --matrix

# 3. Do something
harness list pipeline
harness execute pipeline my-pipeline --follow
harness get execution my-pipeline/<execution-id>
```

---

## 🧭 Grammar

Every command is:

```sh
harness <verb> <noun> [identifier] [flags]
```

### Verbs

| Verb           | Meaning                                                              |
| -------------- | -------------------------------------------------------------------- |
| `list`         | List resources of a noun (paginated).                                |
| `get`          | Fetch a single resource by ID.                                       |
| `create`       | Create a resource — supports `--file/-f` (YAML) or `--set key=value`.|
| `update`       | Update a resource — `--set key=value`, `--del key`.                  |
| `delete`       | Delete a resource by ID.                                             |
| `execute`      | Run, trigger, or invoke a resource (pipelines, scans, HQL, etc.).    |
| `push` / `pull`| Move package content in the Artifact Registry.                       |
| `configure`    | Configure a local client to use a Harness resource (e.g. a registry).|
| `install`      | Install or upgrade the CLI and its modules.                          |
| `auth`         | Manage authentication profiles.                                      |
| `version`      | Print the CLI version.                                               |

### Qualified nouns (`noun:variant`)

Some resources expose multiple variants of the same verb. The CLI uses a colon to qualify them:

```sh
harness get pipeline:summary <id>
harness execute pipeline:input_set <id>
harness execute pipeline:dynamic <id>
harness list pr:mine
harness execute pr:merge <repo>/<pr>
harness execute pr:close <repo>/<pr>
harness push artifact:docker my-image:1.0
harness push artifact:npm ./my-package.tgz
harness execute artifact_version:firewall_scan <ver>
harness execute registry:migrate <registry>
harness execute execution:abort <id>
harness execute execution:retry <id>
harness execute approval_instance:approve <id>
harness execute evaluation:run <id>
harness execute feature_flag:kill <id>
harness execute hql:run
```

Run `harness list noun --matrix` at any time to see every qualified verb the CLI supports.

---

## 🔐 Authentication

Credentials resolve in this order:

1. `--profile <name>` flag
2. `HARNESS_API_KEY` env var
3. `HARNESS_PROFILE` env var
4. CI runner env vars (auto-detected)
5. Default profile from `~/.harness/config.yaml`

### Interactive login

```sh
harness auth login
```

Launches a TUI wizard that walks through the API URL, PAT/SAT token, and default org/project. Requires a TTY.

### Non-interactive login (CI, scripting)

Prefer `HARNESS_API_KEY` for CI. If you need a saved profile without a TTY, pass credentials as flags:

```sh
harness auth login \
  --api-url  app.harness.io \
  --api-token <token> \
  --account   <id> \
  --org       <id> \
  --project   <id>
```

### Named profiles

```sh
harness auth login --profile staging
harness list pipeline --profile staging
```

Set the active profile for a whole shell session:

```sh
export HARNESS_PROFILE=staging
```

### Manage profiles

```sh
harness auth profiles              # list all saved profiles
harness auth status                # show resolved profile and validate credentials
harness auth setscope --org my-org --project my-project
harness auth env                   # print env vars for the current auth context
harness auth env --export          # prefixed with "export "
harness auth token                 # print the active API token
harness auth logout                # remove a profile
```

Profile config is saved to `~/.harness/config.yaml`; the token is stored in `~/.harness/credentials`.

---

## 🔎 Discovery

The CLI is self-describing — you rarely need to leave the terminal to find a command.

```sh
harness list module                # every loaded module
harness get module <name>          # domain model, nouns, and guides for a module
harness list noun                  # every registered noun
harness list noun --matrix         # all nouns × verbs at a glance
harness get noun <noun>            # fields and commands for a specific noun
harness <verb> <noun> --help       # flags specific to a command
```

`get module <name> --matrix` prints the verb matrix scoped to a single module.

---

## 🧩 Commands

Legend used in the tables below:

| Symbol | Meaning                                                     |
| ------ | ----------------------------------------------------------- |
| `✓`    | Supported                                                   |
| `L`    | Supports `--level` (project / org / account scope)          |
| `S`    | Set-fields — create/update with `--set key=value`           |
| `GTP`  | Get-then-put — `--set` / `--del` semantics for updates      |
| `Y`    | YAML file — outputs or accepts a YAML body with `-f`        |

> [!NOTE]
> All `list` commands support paging (`--limit`, `--offset`, `--all`, `--count`).

<details open>
<summary><b>Core & Discovery</b></summary>

| Command              | Purpose                                                     |
| -------------------- | ----------------------------------------------------------- |
| `auth login`         | Interactive or non-interactive login to a profile           |
| `auth logout`        | Remove a profile                                            |
| `auth status`        | Show resolved profile and validate credentials              |
| `auth setscope`      | Set default org / project on a profile                      |
| `auth profiles`      | List all authentication profiles                            |
| `auth env`           | Print env vars for the current auth context                 |
| `auth token`         | Print the active API token                                  |
| `version`            | Print the CLI version                                       |
| `install cli`        | Install or upgrade the Harness CLI and installed modules    |
| `install module`     | Install a Harness CLI module (e.g. `har`)                   |
| `list module`        | Show all available modules                                  |
| `get module <name>`  | Domain model, nouns, and guides for a module                |
| `list noun`          | Show all registered nouns (supports `--matrix`)             |
| `get noun <noun>`    | Fields and commands for a specific noun                     |

</details>

<details open>
<summary><b>Platform / Access Control</b></summary>

| Noun              | list | get | create | update | delete | execute |
| ----------------- | :--: | :-: | :----: | :----: | :----: | :-----: |
| `account`         |      |  ✓  |        |        |        |         |
| `organization`    |  ✓   |  ✓  |   S    |  GTP   |   ✓    |         |
| `project`         |  L   |  ✓  |   S    |  GTP   |   ✓    |         |
| `user`            |  L   |  ✓  |        |        |        |         |
| `user_group`      |  L   |  ✓  |        |        |        |         |
| `service_account` |  L   |  ✓  |        |        |        |         |
| `role`            |  L   |  ✓  |        |        |        |         |
| `role_assignment` |  L   |  ✓  |        |        |        |         |
| `resource_group`  |  L   |  ✓  |        |        |        |         |
| `permission`      |  ✓   |  ✓  |        |        |        |         |
| `setting`         |  L   |  ✓  |        |        |        |         |
| `connector`       |  L   |  ✓  |   S    |  GTP   |   ✓    |    ✓    |
| `delegate`        |  L   |  ✓  |        |        |        |         |
| `delegate_token`  |  ✓   |     |   ✓    |        |   ✓    |         |
| `agent`           |  ✓   |  ✓  |   S    |  GTP   |        |         |
| `secret`          |  L   |  ✓  |   S    |  GTP   |   ✓    |         |
| `entity_usage`    |  ✓   |     |        |        |        |         |

`execute connector:test` runs a connectivity test against a configured connector.

</details>

<details open>
<summary><b>Pipelines / CI · CD</b></summary>

| Noun                      | list | get | create | update | delete | execute |
| ------------------------- | :--: | :-: | :----: | :----: | :----: | :-----: |
| `pipeline`                |  ✓   |  Y  |   Y    |   Y    |   ✓    |    ✓    |
| `pipeline:dynamic`        |      |     |        |        |        |    ✓    |
| `pipeline:input_set`      |      |     |        |        |        |    ✓    |
| `pipeline:summary`        |      |  ✓  |        |        |        |         |
| `pipeline_v1`             |  ✓   |  ✓  |        |        |        |         |
| `execution`               |  ✓   |  ✓  |        |        |        |    ✓    |
| `execution:abort`         |      |     |        |        |        |    ✓    |
| `execution:retry`         |      |     |        |        |        |    ✓    |
| `execution:retry_history` |      |  ✓  |        |        |        |         |
| `execution_step`          |  ✓   |     |        |        |        |         |
| `execution_log`           |  ✓   |  ✓  |        |        |        |         |
| `trigger`                 |  ✓   |  ✓  |   S    |  GTP   |   ✓    |         |
| `input_set`               |  ✓   |  ✓  |   S    |  GTP   |   ✓    |         |
| `runtime_input_template`  |      |  ✓  |        |        |        |         |
| `template`                |  ✓   |  ✓  |   S    |        |        |         |
| `template_version`        |  ✓   |  ✓  |        |  GTP   |   ✓    |         |
| `approval_instance`       |  ✓   |  ✓  |        |        |        |    ✓    |
| `freeze_window`           |  L   |  ✓  |        |        |        |         |
| `global_freeze`           |      |  ✓  |        |        |        |         |

`execute pipeline` supports `--branch`, `--input-set`, `--input key=value` (repeatable), `--input-file`, and `--follow` (live log streaming that exits when the execution reaches a terminal state and inherits the execution's exit status).

`execute approval_instance:approve` / `:reject` action a manual approval; `execute execution:abort` / `:retry` control a running execution; `update template_version:set-stable` promotes a template version.

</details>

<details open>
<summary><b>CD (Deployment)</b></summary>

| Noun               | list | get | create | update | delete |
| ------------------ | :--: | :-: | :----: | :----: | :----: |
| `service`          |  ✓   |  ✓  |   S    |  GTP   |   ✓    |
| `environment`      |  ✓   |  ✓  |   S    |  GTP   |   ✓    |
| `infrastructure`   |  ✓   |  ✓  |   S    |  GTP   |   ✓    |
| `service_override` |  ✓   |  ✓  |   S    |  GTP   |   ✓    |

</details>

<details open>
<summary><b>Code (Repositories & Pull Requests)</b></summary>

| Noun          | list | get | create | update | delete | execute |
| ------------- | :--: | :-: | :----: | :----: | :----: | :-----: |
| `repository`  |  ✓   |  ✓  |   S    |  GTP   |   ✓    |         |
| `pr`          |  ✓   |  ✓  |   S    |  GTP   |        |         |
| `pr:mine`     |  ✓   |     |        |        |        |         |
| `pr:merge`    |      |     |        |        |        |    ✓    |
| `pr:close`    |      |     |        |        |        |    ✓    |
| `branch`      |  ✓   |  ✓  |   S    |        |   ✓    |         |
| `commit`      |  ✓   |  ✓  |        |        |        |         |
| `tag`         |  ✓   |     |   S    |        |   ✓    |         |
| `pr_activity` |  ✓   |     |        |        |        |         |
| `pr_commit`   |  ✓   |     |        |        |        |         |
| `pr_check`    |  ✓   |     |        |        |        |         |
| `pr_comment`  |  ✓   |     |   S    |  GTP   |   ✓    |         |
| `commit_check`|  ✓   |     |        |        |        |         |

</details>

<details open>
<summary><b>Artifact Registry (<code>har</code>) — external module</b></summary>

The `har` binary ships alongside `harness` in the default bundle. It manages registries, artifacts, and versions across every major package format.

| Noun                             | list | get | create | update | delete | execute | push | pull |
| -------------------------------- | :--: | :-: | :----: | :----: | :----: | :-----: | :--: | :--: |
| `registry`                       |  ✓   |  ✓  |   S    |        |   ✓    |         |      |      |
| `registry:firewall_scan`         |      |     |        |        |        |    ✓    |      |      |
| `registry:migrate`               |      |     |        |        |        |    ✓    |      |      |
| `registry_metadata`              |      |  ✓  |        |  GTP   |        |         |      |      |
| `artifact`                       |  ✓   |  ✓  |        |        |   ✓    |         |  ✓†  |  ✓   |
| `artifact_metadata`              |      |  ✓  |        |  GTP   |        |         |      |      |
| `artifact_version`               |  ✓   |  ✓  |        |        |   ✓    |    ✓    |      |      |
| `artifact_version:copy`          |      |     |        |        |        |    ✓    |      |      |
| `artifact_version:firewall_scan` |      |     |        |        |        |    ✓    |      |      |
| `artifact_version_metadata`      |      |  ✓  |        |  GTP   |        |         |      |      |
| `artifact_file`                  |  ✓   |     |        |        |        |         |      |      |

`configure registry <id> --client npm` wires a local package manager client to a Harness registry.

**Push variants (†)** — pick the variant that matches your package type; each validates the file format and sets the correct registry type:

```
push artifact:maven       push artifact:cargo       push artifact:swift
push artifact:npm         push artifact:go          push artifact:puppet
push artifact:python      push artifact:conda       push artifact:helm
push artifact:nuget       push artifact:dart        push artifact:docker
push artifact:rpm         push artifact:composer
```

</details>

<details open>
<summary><b>Infrastructure as Code Management (<code>iacm</code>)</b></summary>

| Noun              | list | get | execute |
| ----------------- | :--: | :-: | :-----: |
| `workspace`       |  ✓   |  ✓  |    ✓    |
| `host`            |  ✓   |  ✓  |         |
| `inventory`       |  ✓   |  ✓  |         |
| `playbook`        |  ✓   |  ✓  |         |
| `registry_module` |  ✓   |  ✓  |         |
| `provider`        |  ✓   |  ✓  |         |

`execute workspace` runs plans/applies/destroys against a Terraform/OpenTofu workspace.

</details>

<details open>
<summary><b>Governance (OPA policies)</b></summary>

| Noun                | list | get | create | update | delete |
| ------------------- | :--: | :-: | :----: | :----: | :----: |
| `policy`            |  ✓   |  ✓  |   S    |  GTP   |   ✓    |
| `policy_set`        |  ✓   |  ✓  |   S    |  GTP   |   ✓    |
| `policy_evaluation` |  ✓   |     |        |        |        |

</details>

<details open>
<summary><b>Audit Trail</b></summary>

| Noun          | list | get |
| ------------- | :--: | :-: |
| `audit_event` |  ✓   |  ✓  |

Filter with `--from` and `--to` for a specific time window.

</details>

<details>
<summary><b>Feature Management & Experimentation (<code>fme</code>)</b></summary>

| Noun                        | list | get | create | update | delete | execute |
| --------------------------- | :--: | :-: | :----: | :----: | :----: | :-----: |
| `feature_flag`              |  ✓   |  ✓  |   S    |  GTP   |   ✓    |         |
| `feature_flag:archive`      |      |     |        |        |        |    ✓    |
| `feature_flag:unarchive`    |      |     |        |        |        |    ✓    |
| `feature_flag:definition`   |  ✓   |  ✓  |   S    |  GTP   |   ✓    |         |
| `feature_flag:kill`         |      |     |        |        |        |    ✓    |
| `feature_flag:restore`      |      |     |        |        |        |    ✓    |
| `feature_flag:reallocate`   |      |     |        |        |        |    ✓    |

</details>

<details>
<summary><b>AI Evals</b></summary>

| Noun              | list | get | create | delete | execute |
| ----------------- | :--: | :-: | :----: | :----: | :-----: |
| `eval_dataset`    |  ✓   |  ✓  |   S    |   ✓    |         |
| `evaluation`      |  ✓   |  ✓  |   S    |   ✓    |         |
| `evaluation:run`  |      |     |        |        |    ✓    |
| `eval_run`        |  ✓   |  ✓  |        |        |         |
| `eval_metric`     |  ✓   |  ✓  |   S    |   ✓    |         |
| `eval_metric_set` |  ✓   |  ✓  |   S    |   ✓    |         |
| `eval_target`     |  ✓   |  ✓  |   S    |   ✓    |         |
| `eval_model`      |  ✓   |  ✓  |   S    |   ✓    |         |
| `eval_suite`      |  ✓   |  ✓  |        |   ✓    |         |
| `eval_suite:run`  |      |     |        |        |    ✓    |

</details>

<details>
<summary><b>Knowledge Graph & HQL</b></summary>

| Noun                | list | get | execute |
| ------------------- | :--: | :-: | :-----: |
| `kg:type`           |  ✓   |  ✓  |         |
| `kg:queryable_type` |  ✓   |     |         |
| `kg:related_type`   |  ✓   |     |         |
| `kg:connection`     |  ✓   |     |         |
| `hql:run`           |      |     |    ✓    |
| `hql:validate`      |      |     |    ✓    |
| `hql:explain`       |      |     |    ✓    |
| `hql:grammar`       |      |     |    ✓    |

HQL is Harness's graph query language over the unified schema. Run `execute hql:grammar` to fetch the full grammar; `execute hql:validate` and `execute hql:explain` help iterate on queries before you run them.

```sh
harness execute hql:run --query 'find entity "platform:project" | select { * } | limit 10'
```

</details>

---

## 📤 Output Formats

Every command supports `--format`. `list` commands default to `table`; single-resource commands default to `text`.

```sh
harness list pipeline --format table      # default for lists
harness list pipeline --format json
harness list pipeline --format jsonl      # one JSON object per line — stream friendly
harness list pipeline --format yaml
harness list pipeline --format csv
harness list pipeline --format tsv
harness list pipeline --format markdown

harness get  pipeline my-pipeline --format text   # default for get
harness get  pipeline my-pipeline --format json
harness get  pipeline my-pipeline --format yaml   # object only — ready to edit and pass back with `update -f`
```

Shorthands: `--json` == `--format json`, `--yaml` == `--format yaml`.

### Custom columns and fields

```sh
harness list pipeline --list-columns                       # show available columns
harness list pipeline --columns name,tags
harness list pipeline --columns "+lastRun"                 # add to defaults
harness list pipeline --columns "Owner:it.metadata.owner"  # rename with an expression

harness get  pipeline my-pipeline --list-fields
harness get  pipeline my-pipeline --fields name,git_url    # tab-separated for `read` / `$( ... )`
```

Other output flags:

| Flag              | Description                                                  |
| ----------------- | ------------------------------------------------------------ |
| `--no-headers`    | Suppress table/CSV headers and paging footer                 |
| `-o`, `--out`     | Write output to a file instead of stdout                     |
| `--raw`           | Emit the full raw API response (only with `--format json`)   |

---

## 🗂 Profiles & Scope

### Multiple profiles

```sh
harness auth login  --profile prod --api-token <token> --account <id>
harness list pipeline --profile prod
export HARNESS_PROFILE=prod   # session-wide switch
```

### Scope flags

Every command accepts scope overrides:

```sh
harness list pipeline --org my-org --project my-project
harness list secret   --level account          # target account scope
harness list secret   --level org --org my-org # target an org
```

### Paging

```sh
harness list pipeline               # default page (limit 20)
harness list pipeline --limit 100
harness list pipeline --offset 40 --limit 20
harness list pipeline --all         # fetch every page
harness list pipeline --count       # just the total count
```

---

## ⚙️ Configuration

### Environment variables

| Variable                  | Description                                                                     |
| ------------------------- | ------------------------------------------------------------------------------- |
| `HARNESS_API_KEY`         | API token. Takes precedence over saved profile credentials.                     |
| `HARNESS_ACCOUNT_ID`      | Account ID. Used together with `HARNESS_API_KEY` for env-var auth.              |
| `HARNESS_PROFILE`         | Name of the saved profile to use. Same effect as `--profile <name>`.            |
| `HARNESS_DEFAULT_ORG`     | Default org for commands that need one. Overridden by `--org`.                  |
| `HARNESS_DEFAULT_PROJECT` | Default project for commands that need one. Overridden by `--project`.         |
| `HARNESS_API_URL`         | Override the API URL (advanced; typically only needed for self-hosted Harness). |
| `HARNESS_DEBUG`           | Set to `1` to enable debug logging without passing `--debug`.                   |
| `HARNESS_NO_COLOR`        | Set to `1` to disable ANSI colors. `NO_COLOR` is also respected.                |
| `HARNESS_CONFIG_HOME`     | Override the location of `~/.harness/`.                                         |

Common CI runner env vars are auto-detected. `HARNESS_API_KEY` always wins.

### Config files

- `~/.harness/config.yaml` — named profiles (API URL, account ID, default org/project)
- `~/.harness/credentials` — tokens (kept out of the config so it can be safely shared/committed)

---

## 🚩 Global Flags

Flags below work on every command.

**Scope**

| Flag                          | Description                                    |
| ----------------------------- | ---------------------------------------------- |
| `--profile <name>`            | Use a named auth profile                       |
| `--org <id>`                  | Override the resolved org                      |
| `--project <id>`              | Override the resolved project                  |
| `--level account\|org\|project` | Set scope for multi-level nouns              |

**Behavior**

| Flag                    | Description                                                                                    |
| ----------------------- | ---------------------------------------------------------------------------------------------- |
| `--debug`               | Enable debug logging                                                                            |
| `--timeout <seconds>`   | Abort after N seconds; accepts decimals (`1.5`); `0` = no timeout; exits `124` on timeout      |
| `--ui`                  | Launch an interactive TUI (requires a TTY; supported on selected commands)                     |
| `-h`, `--help`          | Help for the current command                                                                    |

---

## 🚦 Exit Codes

| Code  | Meaning                                                     |
| ----- | ----------------------------------------------------------- |
| `0`   | Success                                                     |
| `1`   | Generic failure (validation error, API error, not found …)  |
| `2`   | Usage error — invalid flags or unknown command              |
| `124` | Command timed out (`--timeout` exceeded)                    |
| `130` | Interrupted by the user (`Ctrl+C`)                          |

`execute pipeline --follow` exits with the pipeline's terminal status — a successful execution is `0`, a failed one is `1`.

Use exit codes in scripts:

```sh
if ! harness list pipeline --search deploy-prod --json | jq -e 'length > 0'; then
  echo "no matching pipelines"
  exit 1
fi
```

---

## 🛠 Build from Source

Requires [Task](https://taskfile.dev) and Go 1.26+.

```sh
brew install go-task
task build            # builds bin/harness and bin/harness-har
task build:main       # builds only bin/harness (faster; skips har)
```

Add the built binaries to your `PATH` for the duration of the session:

```sh
source local-setup.zsh
```

For details on the release process and Homebrew publishing, see [`BUILD.md`](BUILD.md) and [`docs/publishing-to-homebrew.md`](docs/publishing-to-homebrew.md).

---

## 🤝 Contributing

Contributions are welcome. Most command additions are pure YAML edits under `pkg/spec/` — no Go required. See [`AGENTS.md`](AGENTS.md) for a deep dive on the spec-driven design, and open an issue or pull request on GitHub to get started.

- **Report a bug or request a feature** → [open an issue](https://github.com/harness/cli/issues)
- **Send a change** → open a pull request against `main`

---

## 📜 License

Licensed under the [Apache License 2.0](LICENSE). Copyright © 2026 Harness Inc.
