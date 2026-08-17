# Subcommand CLI, `burn overview`, `burn connect` — design

Implements #17, #18, #19.

## Problem

`burn` is a single flat command: one global `flag.Parse()` in `main.go`, with
`setup` special-cased via a manual `os.Args[1] == "setup"` check. Two planned
features need their own flag sets that don't belong in the global one:

- `burn overview` (#18) — summarize the JSONL activity store (`store.go`)
  instead of only exposing raw lines.
- `burn connect` (#19) — verify/query an external data source (Jira first),
  needing its own `--jql` flag.

Both were filed depending on #17 (subcommand dispatch), which doesn't exist
yet. This spec covers building a minimal #17 as a prerequisite, then #18 and
#19 on top of it. #20 (`burn process`, looping sessions over a data source) is
explicitly out of scope.

## 1. Subcommand dispatch (#17)

`main.go`'s `run()` becomes a dispatcher over `os.Args[1]`:

- `burn run [flags]` — today's exact flag set and behavior (`--target`,
  `--jobs`, `--model`, `--goal`, `--watch`, `--workdir`, `--store`,
  `--max-turns`, `--dry-run`, `--max-usd-guard`, `--i-know-this-bills-api`,
  `--dangerously-skip-permissions`), parsed with its own
  `flag.NewFlagSet("run", flag.ExitOnError)`. No behavior change — this is a
  pure extraction of the existing flag-parsing block into a named subcommand.
- Bare invocation — no arguments at all, or the first argument starts with
  `-` — is aliased to `run`, so `burn --dry-run --goal x` and bare `burn`
  keep working exactly as today, including today's `--goal is required`
  error on a bare no-flag invocation. A first argument that does *not*
  start with `-` and isn't one of the known command names falls through to
  the unrecognized-command case below (usage + exit 1), rather than being
  silently treated as a `run` flag.
- `burn setup` — unchanged behavior, just one branch of the dispatch table
  instead of a special-cased `os.Args[1]` check.
- `burn overview [flags]` — see below.
- `burn connect <source> [flags]` — see below.
- `burn -h` / `burn --help` / an unrecognized command name — print the list
  of subcommands (`run`, `overview`, `connect`, `setup`) and a one-line
  description of each, exit 0 for `-h`/`--help`, exit 1 for an unknown
  command.

No new dependency: `go.mod` has zero requires today; stdlib
`flag.NewFlagSet` fully covers four flat (non-nested) subcommands.

## 2. `burn overview` (#18)

New file `overview.go`.

**Data source:** the same `--store` JSONL file `run`/`watch` already write
(default `~/.claude/burn/worker.jsonl`), read via `flag.NewFlagSet("overview", ...)`
with `--store` (same default as `run`) and `--json`.

**Aggregation:** read every `"session"`-kind `Record` (`store.go`), group by
`Goal` (exact string match — `Goal` is the only project/task dimension that
exists today). For each group compute:

- session count
- total cost (`sum(CostUSD)`)
- total turns (`sum(NumTurns)`)
- error count (`count(IsError)`)
- total time spent (see below)
- first-run / last-run timestamps (`min(TS)` / `max(TS)`)

Plus one totals row summing across all goals.

**Time spent — `StartedAt` addition:** `Record` (store.go) gains a new field:

```go
StartedAt string `json:"started_at,omitempty"`
```

`launch()` in `runner.go` stamps `start := time.Now().UTC()` immediately
before calling `runClaude`, and sets `StartedAt` on the `Record` it writes.
Per-session duration = `TS - StartedAt`. This is additive and backward
compatible: existing JSONL lines have no `started_at` key, unmarshal it as
`""`, and are counted as 0 duration — the overview output flags a goal as
"duration: partial" if any of its sessions predate the field rather than
silently under-reporting.

**Output:**

- Default: a plain-text table (goal, sessions, cost, turns, errors, time
  spent, first run, last run), plus a totals row. Long goals are truncated
  for table width, matching terminal-tool conventions.
- `--json`: the same aggregated struct, marshaled directly — no separate
  schema to maintain.
- Missing/empty store file: print `"no burn activity recorded yet"` and
  exit 0 (not an error — a fresh install has never run `burn run`).

## 3. `burn connect` (#19)

New files: `connect.go` (CLI wiring + the `Source` abstraction) and
`jira.go` (the Jira implementation).

**Abstraction** (`connect.go`):

```go
type WorkItem struct {
    Key     string
    Summary string
}

// Source lists work items matching a query string (source-specific syntax,
// e.g. JQL for Jira).
type Source interface {
    ListItems(query string) ([]WorkItem, error)
}
```

One interface, one implementation (`jiraSource`) — sized so a second source
(#20-adjacent work) is "implement the interface", not a rewrite. No
registry/factory/plugin-loading machinery for a single implementation.

**CLI:** `burn connect <source> [flags]`, parsed with
`flag.NewFlagSet("connect", ...)`. `<source>` is a positional argument;
`jira` is the only recognized value for now (anything else errors:
`unknown source %q`). Flags: `--jql string` (required for `jira`).

**Jira implementation** (`jira.go`):

- Requires `acli` on `PATH` (same pattern as `setup.go`'s check for
  `claude`) — matches the user's existing Jira tooling convention instead
  of reinventing credential handling.
- `ListItems(jql string)` shells out to `acli jira workitem search --jql
  <jql>` (exact output flag TBD at implementation time — verify whatever
  machine-readable output `acli` supports and parse that) and returns
  `[]WorkItem`.
- Parsing is a pure function (`parseAcliSearchOutput([]byte) ([]WorkItem,
  error)`) separate from the `exec.Command` call, so it's unit-testable
  against a captured sample without shelling out in tests.

**Behavior:** `burn connect jira --jql "..."` checks `acli` is present,
runs the search, prints matched issues (key + summary) to stdout. No store
writes — connect doesn't spawn sessions or log activity; that's #20's job.

## Error handling

- Dispatch: unknown subcommand → usage + exit 1 (no panic/stack trace).
- `overview`: unreadable store file (permissions, corrupt JSON line) →
  report the error, don't silently skip; a corrupt single line is skipped
  with a warning to stderr rather than aborting the whole aggregation.
- `connect jira`: missing `acli` on `PATH` → clear error naming the missing
  binary, mirroring `setup.go`'s style. `acli` command failure (bad JQL,
  auth expired) → surface `acli`'s stderr, don't swallow it.

## Testing

Following the existing pure-function test style (`burn_test.go`'s
`TestChildEnvScrubsBillingRiskAndPinsToken`, `TestCredsBlobParse`):

- `overview_test.go`: aggregation function against hand-built `[]Record`
  fixtures (covering: multiple goals, missing `StartedAt`, an error
  session, empty input) — no real file I/O.
- `jira_test.go`: `parseAcliSearchOutput` against a captured sample output
  string — no real `acli` invocation.
- A small dispatcher test confirming each known command name resolves to
  its handler and unknown names error.

## Out of scope

- `burn process` (#20) — looping sessions over a connected source's
  workload, `--team`/`--status` → JQL translation, `--enrich`/`--implement`/
  `--auto` modes.
- Any second `Source` implementation.
- `overview` grouping by anything other than exact `Goal` string match.
- Historical backfill of `StartedAt` for existing JSONL entries.
