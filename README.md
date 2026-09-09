# dont-burn-it-all

[![CI](https://github.com/tbobm/dont-burn-it-all/actions/workflows/ci.yml/badge.svg)](https://github.com/tbobm/dont-burn-it-all/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/tbobm/dont-burn-it-all?sort=semver)](https://github.com/tbobm/dont-burn-it-all/releases)
[![Go Report Card](https://goreportcard.com/badge/github.com/tbobm/dont-burn-it-all)](https://goreportcard.com/report/github.com/tbobm/dont-burn-it-all)
[![License: MIT](https://img.shields.io/github/license/tbobm/dont-burn-it-all)](LICENSE)

Spend your Claude Code **subscription** 5-hour quota on real work — many parallel headless
`claude -p` sessions — and stop at a threshold so you keep a reserve. Metered against the real
Anthropic usage endpoint, not a local estimate.

## Install

```sh
# prebuilt binary via mise (no Go needed)
mise use -g "github:tbobm/dont-burn-it-all[exe=burn]@latest"

# or from source
go build -o burn .        # or: just build
```

## Quick start

```sh
burn setup                                    # verify config (claude, token, endpoint, dirs)
burn run --dry-run --goal test                # print current 5h usage + planned jobs; run nothing
burn run --goal "write tests for pkg/foo" --jobs 4 --target 80   # spend quota up to 80%
burn run --watch --target 80                   # monitor only: notify when 5h usage hits 80%
burn run --target 60 --weekly-target 40 --goal "..."   # stop at 60% of 5h AND 40% of 7d, whichever hits first
burn overview                                  # summarize past sessions: cost, turns, errors, time spent
burn connect jira --label claude-ready              # list matching Jira issues
burn process jira --label claude-ready --dry-run   # preview one session per labelled issue
```

`burn <command>` dispatches to a subcommand (`run`, `process`, `overview`, `connect`, `setup`); bare
`burn --goal ...` / `burn --dry-run ...` (no subcommand keyword) remain aliases for `burn run ...`.

Each `--goal` launch runs `--jobs` sessions to completion and stops — you start each launch.
Pick a `--target` above your current usage, or the launch refuses by design.

## `burn run` flags

| Flag | Default | Meaning |
|---|---|---|
| `--target` | `25` | Refuse/notify once 5-hour utilization reaches this % |
| `--weekly-target` | `0` | Refuse/notify once 7-day utilization reaches this % (0 disables) |
| `--jobs` | `1` | Parallel sessions per launch |
| `--goal` | — | Task each session works on (required to launch) |
| `--model` | `opus` | Model for sessions |
| `--watch` | `false` | Monitor mode: poll + notify, spawn nothing |
| `--workdir` | temp scratch dir | Where sessions run — a scratch dir, **not a real repo** |
| `--store` | `~/.claude/burn/worker.jsonl` | JSONL log of sessions and readings |
| `--max-turns` | `30` | Max agent turns per session |
| `--max-usd-guard` | `0` (off) | Abort if reported cost exceeds this $ |
| `--dangerously-skip-permissions` | `false` | Run sessions unattended (opt-in) |
| `--i-know-this-bills-api` | `false` | Override the billing-risk env refusal |
| `--dry-run` | `false` | Print state, spawn nothing |
| `--sandbox` | `false` | Opt-in extra: run sessions in a local [OpenSandbox](https://github.com/opensandbox-group/OpenSandbox) (Docker) instead of on the host |
| `--sandbox-image` | `burn-sandbox:latest` | Image for `--sandbox` sessions |
| `--repo` | — | Repo to work in: mounted read-write into the sandbox with `--sandbox`, otherwise the session working dir |
| `--gh-token-env` | `GH_TOKEN` | Env var with a GitHub token to forward for PR creation (falls back to `gh auth token`) |
| `--aws-profile` | — | `--sandbox` only: mount `~/.aws` read-only and export `AWS_PROFILE` inside the sandbox |
| `--wait-for-check` | — | After launch, wait for PR checks whose name contains this substring (e.g. `spacelift`) and report pass/fail (requires `--jobs 1`) |
| `--wait-timeout` | `30m` | Give up waiting for `--wait-for-check` after this long |

`--wait-for-check` resolves the PR from the current branch of the directory the session actually
ran in: `--repo` under `--sandbox`, or `--workdir` otherwise (host mode's default `--workdir` is
a scratch dir with no `.git` — point `--workdir` at a real repo for host-mode use).

Anything after a `--` separator is forwarded verbatim to the underlying `claude` invocation —
see [Passing flags through to `claude`](#passing-flags-through-to-claude) below.

## Passing flags through to `claude`

`burn run ... -- <claude flags>` forwards everything after `--` straight to `claude -p`,
appended after burn's own flags (so a passthrough `--model`/`--max-turns` wins over burn's
default). Two common uses:

```sh
# Resume a session that got cut off — session ids are in worker.jsonl (`session_id` field)
burn run --goal "..." -- --resume <session-id>

# Load MCP servers for the session
burn run --goal "..." -- --mcp-config ./m.json --strict-mcp-config
```

Caveats:

- **`--resume` is scoped to `--workdir`.** Claude Code indexes session history per working
  directory, so a resume only finds the session if `--workdir` matches what was used to create
  it — otherwise `claude` hard-errors ("No conversation found..."), it does not silently start
  fresh. `--resume` also refuses `--jobs > 1` (N jobs resuming one session id is meaningless).
- **`--mcp-config` under `--sandbox`** needs a path that exists **inside** the container — a
  host path won't resolve there.

## `burn process` — unattended work over a backlog

`burn run` runs one goal N times. `burn process` runs one session **per work item** a connected
source returns, which is what makes an evening, weekend, or out-of-office run useful:

```sh
burn process jira --project SUDS --label claude-ready --mode enrich --jobs 2 \
  --target 80 --weekly-target 50 --max-items 6 --max-runtime 4h \
  --dangerously-skip-permissions --digest ~/.claude/burn/digest.md
```

Selection uses `--project` / `--label` / `--status` (compiled to JQL and printed as the `query:`
line) or a raw `--query "<JQL>"`. Items already completed in the store are skipped, so a repeated
or resumed run never redoes work; `--redo` overrides that.

| Mode | Each session | Needs |
|---|---|---|
| `enrich` (default) | reads the item, posts **one** refinement comment | acli only |
| `implement` | branch, change, tests, **draft** PR, comment linking it | `--repo` |
| `auto` | `enrich` for early-status items, `implement` for ready ones | `--repo` |

`enrich` touches no repository, so it is the only mode allowed `--jobs > 1`. Both built-in
prompts carry their guardrails — draft PRs only, never push to the default branch, never merge,
never force-push, comment instead of guessing on an underspecified item.
`--prompt-template FILE` replaces them with a `text/template` over `.Key .Summary .Status
.Labels .Source .Mode .Repo`.

### `burn process` flags

| Flag | Default | Meaning |
|---|---|---|
| `--project` / `--label` / `--status` | — | Compiled into the source's query (at least one of project/label) |
| `--query` | — | Raw source query (JQL); mutually exclusive with the shorthand |
| `--mode` | `enrich` | `enrich` \| `implement` \| `auto` |
| `--max-items` | `5` | Hard cap on items started this run |
| `--max-errors` | `2` | Stop after N consecutive failures (0 disables) |
| `--max-runtime` | `0` | Stop starting items past this duration (e.g. `8h`) |
| `--stop-file` | `~/.claude/burn/STOP` | Kill switch: `touch` it and a run in flight stops before its next item, and a new run refuses to start |
| `--redo` | `false` | Reprocess items the store records as done |
| `--prompt-template` | — | `text/template` file overriding the built-in prompt |
| `--digest` | — | Write the run digest as markdown to this path |
| `--notify-each` | `false` | Notify per item, not only at the end |

Plus the whole `burn run` flag set. Every threshold is re-checked **before each item**, not only
at launch, and the stop reason lands in the digest — so "why only 3 of 6" always has an answer.

A real run requires `--dangerously-skip-permissions`: a headless session that hits a permission
prompt hangs forever, so `burn process` refuses rather than stalling at 2am. Pair it with
`--sandbox --repo <path>` to keep `implement` writes out of your checkout.

## `burn overview`

Summarizes the JSONL activity store (`--store`, same default as `run`) grouped by goal: session
count, total cost, turns, errors, time spent, and first/last run timestamps. `--group item`
switches to the per-work-item view of `burn process` runs. Add `--json` for scripting.
`--wait-for-check` results are appended to the same store as `"kind":"check"` lines (PR URL,
matched checks, pass/fail) but do not appear in the `overview` table today — read them directly
from the JSONL if you need them.

## `burn connect`

Verifies and queries an external data source. Today: `burn connect jira`, which shells out to
[`acli`](https://developer.atlassian.com/cloud/acli/) (install it and run `acli jira auth login`
first) and prints matching issues as `KEY<tab>status<tab>summary` lines. Takes the same
`--project` / `--label` / `--status` / `--query` selection as `burn process` (`--jql` remains an
alias of `--query`). `burn setup` reports acli's presence and auth as informational `jira
source:` lines.

## Notifications and hooks

By default a target hit rings the terminal bell, prints `NOTICE:`, and shows a
macOS notification. Three env vars forward events anywhere — each runs via `sh -c` with the
message in `$BURN_MSG`:

| Var | Fires on | Extra env |
|---|---|---|
| `BURN_NOTIFY_CMD` | target hit, run headline, each item with `--notify-each` | `BURN_ITEM_*` when per-item |
| `BURN_DIGEST_CMD` | end of a `burn process` run, full markdown digest (falls back to `BURN_NOTIFY_CMD`) | — |
| `BURN_CLAIM_CMD` | before each item; **non-zero exit skips it** | `BURN_ITEM_KEY`, `BURN_ITEM_SUMMARY`, `BURN_ITEM_STATUS`, `BURN_ITEM_MODE`, `BURN_ITEM_SOURCE` |

```sh
# Slack incoming webhook
BURN_NOTIFY_CMD='curl -s -XPOST "$SLACK_WEBHOOK" -d "{\"text\":\"$BURN_MSG\"}"' burn --watch --target 80
# append to a file another process tails
BURN_NOTIFY_CMD='echo "$BURN_MSG" >> ~/.claude/burn/alerts.log' burn --watch --target 80
```

`BURN_CLAIM_CMD` is how a `burn process` run avoids colliding with another machine: local dedup
uses the JSONL store, which is per-host, so label the ticket as taken from the hook and exit
non-zero when someone else already has it.

## Safety

`burn` only spends **subscription** quota, never pay-per-token API. It refuses to run when a
billing-risk env var (`ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`, Bedrock/Vertex) is set, scrubs
them from every session, and runs a one-time preflight that proves a probe actually moves your
5-hour usage before launching real work. Sessions are interactive unless you pass
`--dangerously-skip-permissions` — only do that with a throwaway `--workdir`.

## Sandboxed write sessions (`--sandbox`)

An opt-in extra — nothing in the base tool depends on it, and `burn setup` reports it as
informational only (never a hard failure). Requires Docker and the
[`osb` CLI](https://github.com/opensandbox-group/OpenSandbox); build the image with
`just build-sandbox-image`. Use it when a goal needs `--dangerously-skip-permissions` *and*
real write access (e.g. opening a PR), so the unattended session is isolated from your host:

```sh
burn run --sandbox --repo ~/code/myrepo --dangerously-skip-permissions --goal '
Fix the failing test in pkg/foo, commit on a new branch, push, and open a draft PR with `gh pr create`.'
```

`--sandbox` currently supports `--jobs 1` only — mounting one repo read-write into multiple
sandboxes would race on the working tree and git index.

Changes to `--sandbox` need more than unit tests to trust — see [TESTING.md](TESTING.md) for
the required smoke test against a real local OpenSandbox server.

### AWS read-only access (`--aws-profile`)

`--aws-profile <name>` mounts `~/.aws` read-only into the sandbox and exports `AWS_PROFILE`
(plus `AWS_REGION`, if set on the host) so a goal defaults to running `aws` under a chosen
read-only profile. Host mode needs no flag — it already inherits your full environment.

**Not profile-scoped**: the whole `~/.aws` directory is mounted, not just `<name>`'s section —
read-only protects the files from being *changed*, not which profile the sandboxed agent is
allowed to *use*. It can `export AWS_PROFILE=<other>` or pass `--profile <other>` to select any
profile present in that directory. Only put a profile-less `--aws-profile` you'd be fine with
the agent using *any* of your local profiles for.

```sh
burn run --sandbox --repo ~/code/myrepo --aws-profile readonly --goal '
Check the ECS service status with `aws ecs describe-services ...` and summarize drift.'
```

Caveats:

- **SSO tokens**: the mount carries `~/.aws/sso/cache/`, so reads work while the *host's* SSO
  token is fresh. The container can't run `aws sso login` (no browser, and the mount is
  read-only by design) — refresh on the host if it expires.
- **Assumed-role profiles**: a `role_arn` + `source_profile` profile makes the AWS CLI write to
  `~/.aws/cli/cache`, which fails against the read-only mount. Use a profile that doesn't need
  to write a cache (e.g. a plain `sso_session` profile).

## Example: prepare pending PR reviews

Spend idle quota drafting review comments in **pending** state (nothing submitted):

```sh
burn run --jobs 1 --target 90 --dangerously-skip-permissions --goal '
Review PR https://github.com/OWNER/REPO/pull/123 (`gh pr diff 123`). Draft line-anchored
comments and create them as a PENDING review via `gh api .../pulls/123/reviews` with no
event — do NOT submit.'
```

## Example: open a PR and wait for its Spacelift preview

```sh
burn run --sandbox --repo ~/code/terraform --dangerously-skip-permissions \
  --wait-for-check spacelift --wait-timeout 20m --goal '
Add the new S3 bucket resource, commit, push, and open a draft PR with `gh pr create`.'
```

`--wait-for-check` resolves the PR from the current branch (the session creates it — burn
can't know the number up front), polls `gh pr checks` until every check whose name contains the
substring is terminal or the timeout elapses, and prints a pass/fail summary. `burn` still exits
non-zero on a failed check, the same as any other error — distinguish it
from a refusal by the printed summary or the `"kind":"check"` store record (see `burn
overview` above).

## Example: work labelled tickets while you are away

Triage once, label the tickets you are happy for an agent to touch, then let idle quota work
them overnight or over a weekend:

```sh
# every weekday at 19:00
0 19 * * 1-5 /usr/local/bin/burn process jira --project SUDS --label claude-ready \
  --mode enrich --jobs 2 --target 80 --weekly-target 50 \
  --max-items 6 --max-errors 2 --max-runtime 4h --dangerously-skip-permissions \
  --digest "$HOME/.claude/burn/digests/$(date +\%F).md" \
  >> "$HOME/.claude/burn/process.log" 2>&1
```

A cron shell has neither your PATH nor your keychain, so use an absolute `burn` path and put
`CLAUDE_CODE_OAUTH_TOKEN` (from `claude setup-token`) in the crontab or a sourced env file. On
macOS prefer a launchd agent; on Linux a systemd user timer with `Environment=` is cleaner.

Set `BURN_DIGEST_CMD` to post the digest to Slack, `touch ~/.claude/burn/STOP` to stop the loop
mid-run, and read `burn overview --group item` when you get back.

## Claude Code plugin

Claude Code is the intended entry point. The repo ships `/burn` and `/burn-jira` slash commands
plus two project skills (`.claude/skills/burn/`, `.claude/skills/burn-jira/`). Install it from any
Claude Code session — the repo is its own marketplace:

```sh
/plugin marketplace add tbobm/dont-burn-it-all
/plugin install dont-burn-it-all@dont-burn-it-all
```

Then say "burn quota" / "watch my usage" and the `burn` skill installs `burn` if missing, runs
`burn setup`, and drives it (optionally as a background sub-agent tracked with Monitor). Say
"work my labelled Jira tickets while I'm out" and the `burn-jira` skill walks the whole
unattended path: acli auth, agreeing the label, previewing the ticket list, dry-run, bounds,
Slack digest, scheduling, and the report when you get back.

Slash commands are declared in `.claude-plugin/plugin.json`; the skills live under
`.claude/skills/`, so a `/plugin install` elsewhere currently ships the commands but not the
skills — clone the repo (or copy the skill directories) to get those.

## Development

```sh
mise install      # dev toolchain (Go + just)
just vet test     # go vet + go test
just dist         # cross-build release archives into dist/
```

Releases are automated with [release-please](https://github.com/googleapis/release-please)
(Conventional Commits): a merged release PR tags a version, attaches per-platform binaries, and
publishes a [ko](https://ko.build) image to `ghcr.io/tbobm/dont-burn-it-all`.

## License

MIT — see [LICENSE](LICENSE).
