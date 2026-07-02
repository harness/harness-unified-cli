# Auth & Config

## Two Modes

The two modes target different primary use cases:

- **Profile mode** is for interactive use — a developer on their workstation, managing multiple accounts or environments.
- **Env var mode** is for scripting and CI/CD — credentials injected by the runner, no config file on disk.

### Resolution Order

Auth is resolved in this order (first match wins):

1. `--profile <name>` flag — explicit profile, env vars ignored entirely
2. `HARNESS_API_KEY` set — env var mode, no config file read
3. `HARNESS_PROFILE` env var — use named profile from config file
4. `default` profile in config file

If `--profile` is given and the named profile does not exist, it is an error.  
If `--profile` is given, all auth-related env vars (`HARNESS_API_KEY`, `HARNESS_ACCOUNT`, `HARNESS_API_URL`, `HARNESS_ORG`, `HARNESS_PROJECT`, `HARNESS_REGISTRY_URL`) are ignored entirely — no blending between modes.  
If no auth is resolved by any method, error with: `"not logged in — run 'harness auth login' to get started"`.

### Scope Overrides

`--org` and `--project` are global flags available on every command. They override the org/project from the active profile or env vars for that invocation only. Account and token are always fixed by the profile or env vars — they cannot be overridden per-command.

```
harness pipeline list --org myorg --project myproject
harness pipeline list --profile staging --project other-project
```

### Profile Mode

Credentials are stored across two files in `~/.harness/`. Primary use case is interactive local development.

### Env Var Mode

When `HARNESS_API_KEY` is set (and `--profile` is not), env var mode is active. No config file is read.

Required:

- `HARNESS_API_KEY` — PAT token
- `HARNESS_ACCOUNT` — Harness account identifier (inferred from token if not set)

Optional:

- `HARNESS_API_URL` — defaults to `https://app.harness.io` (override for self-hosted)
- `HARNESS_ORG` — org context
- `HARNESS_PROJECT` — project context
- `HARNESS_REGISTRY_URL` — defaults to `https://pkg.harness.io` (override for self-hosted)

---

## Config Files

Credentials are split across two files for security — profile metadata is separate from tokens.

**`~/.harness/config.yaml`** (permissions `0600`) — profile metadata, no tokens:

```yaml
profiles:
  default:
    api_url: https://app.harness.io
    account_id: AccountID
    org_id: myorg
    project_id: myproject
    registry_url: https://pkg.harness.io

  staging:
    api_url: https://staging.harness.io
    account_id: AccountID
    org_id: myorg
    project_id: myproject
    registry_url: https://pkg.harness.io
```

`org_id`, `project_id`, and `registry_url` are omitted from the file when not set (`omitempty`).

**`~/.harness/credentials`** (permissions `0600`) — tokens only, in a minimal TOML-like format:

```toml
[default]
token = "pat.AccountID.xxx.yyy"

[staging]
token = "pat.AccountID.aaa.bbb"
```

Both files live in `~/.harness/` which is created with `0700` permissions. No "active profile" field exists in either file — the active profile is always resolved at runtime per the rules above.

---

## Token Format

Two token types are supported:

- **PAT** (Personal Access Token) — `pat.{AccountID}.{tokenID}.{secret}`
- **SAT** (Service Account Token) — `sat.{AccountID}.{tokenID}.{secret}`

Both follow the same 4-segment dot-separated format. The account ID is extracted from the token at login and stored explicitly in `config.yaml` — it is not re-parsed at runtime.

### SAT tokens and scope

SATs are often scoped to specific resources and may not have permission to list organizations or projects. If `auth login` is run with a SAT, the org/project picker in the interactive wizard may fail. In that case, set scope manually after login:

```
harness auth login --api-token sat.xxx.yyy.zzz --overwrite
harness auth setscope --org myorg --project myproject
```

`auth status` handles SATs differently: instead of calling `GET /ng/api/user/currentUser`, it calls `POST /ng/api/token/validate` and shows the service account identity. 403 responses on the Account/Org/Project checks are shown as warnings (not errors) since the SA may have resource-level access without enumeration permissions.

---

## Commands

All auth commands are under `harness auth`. They all set `no_auth: true` — they do not require prior authentication.

### `harness auth login`

Saves credentials to a profile. Writes to both `~/.harness/config.yaml` and `~/.harness/credentials`.

```
harness auth login
harness auth login --profile staging
harness auth login --api-url https://staging.harness.io --api-token pat.xxx.yyy.zzz --overwrite
```

**Flags:**

| Flag | Description |
|---|---|
| `--profile` | Global flag — selects which profile to write (default: `"default"`). Must match `^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`. |
| `--api-url` | Harness API base URL (default: `https://app.harness.io`) |
| `--api-token` | PAT token. Required in non-interactive mode. |
| `--account` | Account ID. Inferred from token if not provided; must match token if provided. |
| `--org` | Default org ID to store in profile |
| `--project` | Default project ID to store in profile |
| `--overwrite` | Overwrite existing profile without prompting |
| `--no-overwrite` | Error if profile already exists |
| `--no-validate` | Skip token validation against the API |

**Interactive flow** (stdin and stdout are TTYs and `--api-url` or `--api-token` is not provided):

Runs a bubbletea TUI wizard. If the profile already exists and neither `--overwrite` nor `--no-overwrite` is passed, prompts to confirm overwrite before launching the wizard. The wizard handles URL entry, PAT entry, validation, and org/project pickers.

**Non-interactive flow** (all required values provided via flags, or not a TTY):

If the profile already exists, `--overwrite` is required (errors otherwise, unless `--no-overwrite` is set). Validates the PAT format, then calls `GET /ng/api/accounts/{accountID}` to verify the token (skipped with `--no-validate`). Attempts to fetch `registry_url` from `GET /gateway/har/api/v3/system/info` — stored if successful, silently skipped if not.

After writing, runs `auth status` to display the result.

### `harness auth logout`

Removes a profile from both config files.

```
harness auth logout
harness auth logout --profile staging
```

Defaults to the `"default"` profile.

### `harness auth status`

Shows current auth context and validates credentials against the API. Returns non-zero on any failure.

```
harness auth status
harness auth status --profile staging
harness auth status --format json
```

Performs these checks in sequence (stops on first failure):

1. **Profile** — loads credentials from config/credentials files
2. **API** — TCP dial to port 443 on the API host
3. **User** — PAT format check, then `GET /ng/api/user/currentUser` (shows email and UUID on success)
4. **Account** — `GET /ng/api/accounts/{accountID}` (shows account name on success)
5. **Org** — `GET /ng/api/organizations/{orgID}` (skipped if org not set; shows org name on success)
6. **Project** — `GET /ng/api/projects/{projectID}` (skipped if project not set; shows project name on success)

Token is never printed. Supports `--format json` for structured output.

### `harness auth profiles`

Lists all profiles in the config file.

```
harness auth profiles
harness auth profiles --search staging
```

`--search` filters by substring match on profile name.

### `harness auth setscope`

Sets the default org and/or project on a profile.

```
harness auth setscope --org myorg --project myproject
harness auth setscope --profile staging --org otherorg
harness auth setscope   # interactive picker if both --org and --project are omitted
```

When neither `--org` nor `--project` is provided and stdin/stdout are TTYs, launches an interactive org/project picker. Errors if nothing to set in non-interactive mode. After updating, runs `auth status` to display the result.

### `harness auth env`

Prints env vars for the current auth context (includes auth tokens). Useful for scripting.

```
harness auth env
harness auth env --profile staging
harness auth env --export   # prefix each line with "export "
```

Always outputs `HARNESS_API_KEY`, `HARNESS_ACCOUNT`, `HARNESS_API_URL`. Outputs `HARNESS_ORG`, `HARNESS_PROJECT`, and `HARNESS_REGISTRY_URL` only when they are set in the resolved profile.

### `harness auth token`

Prints the active API token to stdout. Useful for piping to other tools (e.g. w/ `$()`).

```
harness auth token
harness auth token --profile staging
```

Does not require org/project to be configured.
