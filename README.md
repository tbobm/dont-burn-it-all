# dont-burn-it-all

A small Go CLI (and Claude Code plugin) that **deliberately uses your Claude Code subscription's
5-hour quota** by running real work in parallel headless sessions — and **stops at a threshold**
so you keep a reserve instead of getting locked out.

It meters against the **real Anthropic usage endpoint** (`/api/oauth/usage`, the same number
`/usage` shows), not a local token estimate.

## Why the preflight matters (read this)

Under a Max/Pro login, `claude -p` only consumes your **subscription** window if no
`ANTHROPIC_API_KEY` / `ANTHROPIC_AUTH_TOKEN` / Bedrock / Vertex is active. Otherwise it can
**silently bill pay-per-token API** — which wouldn't move your 5-hour usage at all and *would*
cost real money (documented cases of $1,800+ surprise bills from parallel `claude -p`).

This tool defends against that three ways:

1. **Refuses** to run if a billing-risk env var is set (override with `--i-know-this-bills-api`).
2. **Scrubs** those vars from every session's environment and pins `CLAUDE_CODE_OAUTH_TOKEN`.
3. **Preflight metering proof**: before any real session, it runs one probe and confirms your
   real 5-hour utilization actually rose. If it didn't, it hard-aborts. (~3 minutes; skipped for
   ~4h after a successful proof.)

## Install

```sh
go build -o burn .
# optional: export CLAUDE_CODE_OAUTH_TOKEN=$(claude setup-token)   # forces subscription auth
```

The OAuth token is read (in order) from `CLAUDE_CODE_OAUTH_TOKEN`, the macOS Keychain entry
`Claude Code-credentials`, or `~/.claude/.credentials.json`.

## Usage

```sh
./burn --dry-run                                   # show usage %, auth source, planned jobs
./burn --goal "write tests for pkg/foo" --jobs 4   # launch 4 parallel sessions toward one goal
./burn --watch --target 25                         # governor: notify when 5h usage hits 25%
```

Each `--goal` launch runs `--jobs` sessions to completion and stops. It does not loop or refill —
you start each launch yourself.

### Example: "prepare my reviews"

A good use of otherwise-idle quota: draft PR review comments in **pending** state so you can read
and submit them yourself. Sessions run headless, so you pick the PR up front — the tool doesn't
prompt mid-run.

```sh
# 1. List PRs waiting on you (do this yourself, or let your main Claude loop do it):
gh pr list --search "review-requested:@me" --json number,title,url

# 2. Launch a session to prepare comments for the one you chose. It creates a PENDING review
#    (no `event` field) — nothing is submitted until you say so:
./burn --jobs 1 --target 90 --dangerously-skip-permissions --goal '
Review PR https://github.com/OWNER/REPO/pull/123.
Read the diff with `gh pr diff 123`. Draft specific, line-anchored review comments.
Create them as a PENDING review via:
  gh api repos/OWNER/REPO/pulls/123/reviews -f body="..." -F "comments[][path]=..." \
    -F "comments[][line]=..." -F "comments[][body]=..."
Do NOT set an event / do NOT submit. Leave the review pending for me to read and send.'
```

The session does the reading and comment-drafting (using your quota); you get a pending review to
approve or discard. To prep several PRs at once, run one launch per PR (each with its own goal) —
`--jobs` copies of a *single* goal all target the same PR, so use separate launches for separate PRs.

### Flags

| Flag | Default | Meaning |
|---|---|---|
| `--target` | `25` | Refuse/notify once 5-hour utilization reaches this % |
| `--jobs` | `1` | Parallel sessions per launch |
| `--model` | `opus` | Model for sessions |
| `--goal` | — | Task each session works on (required to launch) |
| `--watch` | `false` | Governor mode: poll + notify, spawn nothing |
| `--workdir` | temp scratch dir | Where sessions run — a scratch dir, **not a real repo** |
| `--store` | `~/.claude/burn/worker.jsonl` | JSONL log of sessions and readings |
| `--max-turns` | `30` | Max agent turns per session |
| `--max-usd-guard` | `0` (off) | Abort if reported cost exceeds this $ (subscription should stay ~$0) |
| `--i-know-this-bills-api` | `false` | Override the billing-risk env refusal |
| `--dangerously-skip-permissions` | `false` | Opt in to unattended sessions (no per-tool approval) |
| `--dry-run` | `false` | Print state, spawn nothing |

## Cautions

- Sessions ask for tool permission by default. Pass `--dangerously-skip-permissions` to run them
  fully unattended — and only then keep `--workdir` a throwaway dir, since sessions act without
  approval.
- Burning also consumes your **weekly** limit (`seven_day`), surfaced in the output.
- The goal is to *use* quota on real work up to a reserve — not to waste it.

## License

MIT — see [LICENSE](LICENSE).
