# Paging

Harness APIs use different paging mechanisms internally. The CLI abstracts these behind a uniform set of flags so callers — including AI agents — don't need to know which model a given endpoint uses.

## Flags

| Flag | Description |
|------|-------------|
| `--offset N` | Skip the first N items (item-level, not page-level) |
| `--limit N` | Return at most N items |
| `--all` | Fetch all pages |
| `--count` | Print total item count instead of items |

`--offset` and `--limit` are always item-level. If a page boundary falls in the middle of the requested window, the CLI fetches the necessary pages and slices the result transparently.

`--all` overrides `--offset` and `--limit`.

`--count` is mutually exclusive with `--offset`, `--limit`, and `--all`. It returns the total count for the active query, respecting any other filters (e.g. `--search`). The count reflects the full matching set, not a page window.

`--all` and `--count` are only shown for endpoints that support them. For endpoints where counting or full enumeration is not available, those flags are hidden.

## Agent heuristic

Normal output is a flat list of items with no paging envelope. Agents can infer position without any metadata:

- Got back fewer items than requested → you've reached the end
- Got back exactly as many as requested → there may be more; try `--offset N+limit`
