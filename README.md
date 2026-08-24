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
burn overview                                  # summarize past sessions: cost, turns, errors, time spent
burn connect jira --jql 'project = SUDS AND status = "To Refine"'   # list matching Jira issues
```

`burn <command>` dispatches to a subcommand (`run`, `overview`, `connect`, `setup`); bare
`burn --goal ...` / `burn --dry-run ...` (no subcommand keyword) remain aliases for `burn run ...`.

Each `--goal` launch runs `--jobs` sessions to completion and stops — you start each launch.
Pick a `--target` above your current usage, or the launch refuses by design.

## `burn run` flags

| Flag | Default | Meaning |
|---|---|---|
| `--target` | `25` | Refuse/notify once 5-hour utilization reaches this % |
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

## `burn overview`

Summarizes the JSONL activity store (`--store`, same default as `run`) grouped by goal: session
count, total cost, turns, errors, time spent, and first/last run timestamps. Add `--json` for
scripting.

## `burn connect`

Verifies and queries an external data source. Today: `burn connect jira --jql "<JQL>"`, which
shells out to [`acli`](https://developer.atlassian.com/cloud/acli/) (install it and run `acli
jira auth login` first) and prints matching issues as `KEY<tab>summary` lines.

## Safety

`burn` only spends **subscription** quota, never pay-per-token API. It refuses to run when a
billing-risk env var (`ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`, Bedrock/Vertex) is set, scrubs
them from every session, and runs a one-time preflight that proves a probe actually moves your
5-hour usage before launching real work. Sessions are interactive unless you pass
`--dangerously-skip-permissions` — only do that with a throwaway `--workdir`.

## Example: prepare pending PR reviews

Spend idle quota drafting review comments in **pending** state (nothing submitted):

```sh
burn run --jobs 1 --target 90 --dangerously-skip-permissions --goal '
Review PR https://github.com/OWNER/REPO/pull/123 (`gh pr diff 123`). Draft line-anchored
comments and create them as a PENDING review via `gh api .../pulls/123/reviews` with no
event — do NOT submit.'
```

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
