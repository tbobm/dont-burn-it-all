# `burn process` — unattended, label-driven work over a connected source

Implements #20. Motivating use case: a human is out of office (evening,
weekend, holiday) and wants idle subscription quota spent on the tickets they
already triaged, with a report waiting when they come back.

## Problem

`burn run --goal X --jobs 4` runs the *same* goal four times. There is no way
to say "here are 6 labelled Jira tickets, work one session per ticket". Every
piece needed for an unattended run is also missing:

| Need | Today |
|---|---|
| one session per work item | `--jobs` clones one goal |
| pick by label | only raw `--jql` |
| don't redo an item | `Record` has no item key |
| stop mid-loop when quota runs out | threshold checked once, before launch |
| bound an 8-hour unattended run | no item cap, error breaker, runtime cap, kill switch |
| report what happened | store keeps cost/turns/error, no digest, no Slack |
| know if the Jira path even works | `burn setup` never checks `acli` |

## Non-goals

- **Jira write-back from `burn`.** `burn` stays an orchestrator. The session
  itself runs `acli` to comment on the ticket — it has the tools and can adapt
  to the site's field config. Hardcoding `acli` write syntax into `burn` would
  bake in an unverified CLI contract.
- **Cross-machine claiming.** Dedup is local (the JSONL store) plus an optional
  `BURN_CLAIM_CMD` hook. Two people running `burn process` against the same
  label on different machines can collide; the hook is the escape valve.
- **A scheduler.** `burn process` is one bounded run. cron/launchd/systemd or
  Claude Code schedules it; see the `burn-jira` skill.

## `burn process <source> [flags]`

Source selection reuses the `Source` registry from `connect.go`. A source may
also implement `QueryBuilder` so burn's generic `--project/--label/--status`
shorthand compiles to that source's query language (JQL for Jira); `--query`
stays as the raw escape hatch and is mutually exclusive with the shorthand.

| Flag | Default | Meaning |
|---|---|---|
| `--project` / `--label` / `--status` | — | shorthand compiled to a source query |
| `--query` | — | raw source query (JQL); mutually exclusive with the shorthand |
| `--mode` | `enrich` | `enrich` \| `implement` \| `auto` |
| `--max-items` | `5` | hard cap on items started this run |
| `--max-errors` | `2` | stop after N consecutive failures |
| `--max-runtime` | `0` | stop starting items past this duration (0 = unbounded) |
| `--stop-file` | `~/.claude/burn/STOP` | kill switch: exists ⇒ stop before the next item |
| `--redo` | `false` | reprocess items the store already records as done |
| `--prompt-template` | — | `text/template` file overriding the built-in per-mode prompt |
| `--digest` | — | write the run digest as markdown to this path |
| `--notify-each` | `false` | notify per item, not just at the end |

Plus the whole `burn run` flag set (`--target`, `--weekly-target`, `--jobs`,
`--model`, `--max-turns`, `--dry-run`, `--sandbox`, `--repo`, ...), registered
from one shared `registerRunFlags` so the two commands cannot drift.

### Modes

`enrich` refines the ticket and posts one comment; it never touches a repo, so
it is the only mode allowed with `--jobs > 1`. `implement` works in a repo and
opens a **draft** PR. `auto` resolves per item: no repo configured, or a status
in the early set (`new`, `backlog`, `to refine`, `to do`, `open`, `triage`,
`refinement`) ⇒ `enrich`, otherwise `implement`.

Both built-in prompts carry their guardrails in the prompt text: never push to
the default branch, never merge, never force-push, draft PRs only, and "if the
ticket is underspecified, comment instead of guessing".

### Unattended safety

`burn process` **requires** `--dangerously-skip-permissions` (or `--dry-run`).
There is no safe interactive path for a headless loop: a permission prompt in
an unattended session hangs forever. Making it explicit beats hanging at 2am.
`--sandbox --repo X` is the recommended pairing for `implement`.

Before each item the loop re-reads usage (`UsageClient.Get` caches for
`minPollInterval`, so this is free) and stops on: 5-hour target, 7-day target,
`--max-items`, `--max-errors` consecutive failures, `--max-runtime`, or the
stop file. The stop reason lands in the digest, so "why did it only do 3 of 6"
is always answerable.

### Store and reporting

`Record` gains `item_key`, `item_source`, `mode`. Sessions from a process run
share `Goal = "process <source> <mode>"`, so `burn overview` (grouped by goal)
shows the run in aggregate and `burn overview --group item` shows per ticket.

The run emits a markdown digest (query, mode, picked/skipped counts, a table of
per-item outcomes, stop reason, usage before/after). It goes to `--digest` as a
file, to `BURN_DIGEST_CMD` (falling back to `BURN_NOTIFY_CMD`) as `$BURN_MSG`
for Slack, and a one-line headline through the existing `notify`.

### Hooks

`BURN_CLAIM_CMD` runs via `sh -c` before an item, with `BURN_ITEM_KEY`,
`BURN_ITEM_SUMMARY`, `BURN_ITEM_MODE`, `BURN_ITEM_SOURCE` in the environment. A
non-zero exit **skips** that item — that is the "someone else claimed it"
signal. Same shape as `BURN_NOTIFY_CMD`, so there is one hook idiom to learn.

## Testability

Pure, unit-tested: `buildJQL`, `resolveMode`, `renderGoal`, `processedKeys`,
`filterItems`, `stopState.reason`, `formatDigest`, `validateProcessConfig`,
`aggregate` by item, `resolveCommand`. The orchestration (`processItems`) and
the `acli` field contract shell out and are covered by the manual smoke test in
TESTING.md — the same convention `--sandbox` already follows, and for the same
reason: the real `acli` JSON shape is not knowable from its docs.

## Deferred

- Plugin packaging: `plugin.json` declares `commands` only, so `/plugin install`
  elsewhere ships `/burn` and `/burn-jira` but not `.claude/skills/`. Needs a
  verified plugin-manifest `skills` key before changing.
- `--jobs > 1` for `implement` (needs per-session `git clone --local`
  isolation, the same upgrade `--sandbox` is waiting on).
- OpenMetrics export (#22) would make the digest scrapeable rather than pushed.
