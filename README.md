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
burn connect jira --jql 'project = DEMO AND status = "To Refine"'   # list matching Jira issues
```

`burn <command>` dispatches to a subcommand (`run`, `overview`, `connect`, `setup`); bare
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
| `--repo` | — | Repo mounted read-write into the sandbox (`--sandbox` only; defaults to `--workdir`) |
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

## `burn overview`

Summarizes the JSONL activity store (`--store`, same default as `run`) grouped by goal: session
count, total cost, turns, errors, time spent, and first/last run timestamps. Add `--json` for
scripting. `--wait-for-check` results are appended to the same store as `"kind":"check"` lines
(PR URL, matched checks, pass/fail) but do not appear in the `overview` table today — read them
directly from the JSONL if you need them.

## `burn connect`

Verifies and queries an external data source. Today: `burn connect jira --jql "<JQL>"`, which
shells out to [`acli`](https://developer.atlassian.com/cloud/acli/) (install it and run `acli
jira auth login` first) and prints matching issues as `KEY<tab>summary` lines.

## Notifications

By default a target hit rings the terminal bell, prints `NOTICE:`, and shows a
macOS notification. Set `BURN_NOTIFY_CMD` to forward it anywhere — the command
runs via `sh -c` with the message in `$BURN_MSG`:

```sh
# Slack incoming webhook
BURN_NOTIFY_CMD='curl -s -XPOST "$SLACK_WEBHOOK" -d "{\"text\":\"$BURN_MSG\"}"' burn --watch --target 80
# append to a file another process tails
BURN_NOTIFY_CMD='echo "$BURN_MSG" >> ~/.claude/burn/alerts.log' burn --watch --target 80
```

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
(plus `AWS_REGION`, if set on the host) so a goal can run `aws` under a scoped, read-only
profile. Host mode needs no flag — it already inherits your full environment.

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

## Claude Code plugin

Ships a `/burn` slash command and a project skill (`.claude/skills/burn/`). Install it from any
Claude Code session — the repo is its own marketplace:

```sh
/plugin marketplace add tbobm/dont-burn-it-all
/plugin install dont-burn-it-all@dont-burn-it-all
```

Then say "burn quota" / "watch my usage" — the skill installs `burn` if missing, runs
`burn setup`, and drives it (optionally as a background sub-agent tracked with Monitor).

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
