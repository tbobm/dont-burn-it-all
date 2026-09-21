# `burn run`/`burn process --foreground` — an attended session for building trust

## Problem

Every existing launch path is headless (`claude -p`): no TTY, so a permission
prompt would hang forever, which is why unattended runs require
`--dangerously-skip-permissions`. There is no way to watch a real burn-driven
session end to end — including its permission prompts — before deciding to
trust it unattended. New users, or a new goal/prompt template, have to take
that leap on faith.

## Non-goals

- **Sandbox support.** `--sandbox` execs `osb command run ... -o raw` and
  captures output via `cmd.Output()` — there is no attached-interactive/TTY
  path through `osb` today, and proving one exists would need its own live-osb
  smoke test (same category `TESTING.md` already requires for any
  `sandbox.go` change). `--foreground` and `--sandbox` are mutually exclusive
  for now.
- **Real cost tracking.** An interactive session has no `--output-format
  json` result to parse. Reconstructing cost from the transcript's raw token
  counts would mean `burn` maintaining its own per-model pricing table that
  drifts out of sync with Anthropic's actual prices — an explicit trade-off:
  turns are recovered for real, cost is reported as `n/a`.
- **A confirm-to-continue prompt between `burn process` items.** A human is
  already watching (and approved) every permission prompt inside each
  session; an extra keypress between items adds a step without adding safety.

## Design

`--foreground` is a `Config` field, so both `burn run` and `burn process
<source>` get it for free via `registerRunFlags`. Validation
(`validateForegroundConfig` in `main.go`) is shared by `validateRunFlags` and
`validateProcessConfig` so the two paths can't drift:

- forces `--jobs 1` (one terminal can't attach to more than one interactive
  session)
- refuses `--sandbox`, `--dangerously-skip-permissions` (contradicts the
  point — it would suppress the prompts you're there to watch), and
  `--max-usd-guard` (can't be enforced against an unknown cost)
- for `burn process`, replaces the existing "needs
  `--dangerously-skip-permissions` or `--dry-run`" gate with a third option —
  each item's session is attended, so there's no headless prompt to hang on

Preflight (the subscription-metering probe) is unchanged: it still runs
headless even under `--foreground` — it's an internal check, not something
the human needs to watch, and forcing it through `runClaudeForeground` would
break its own result parsing. `preflight()` explicitly zeroes
`probe.Foreground` before calling `runClaude`.

### Execution (`runClaudeForeground` in `runner.go`)

`runClaude` branches to `runClaudeForeground` when `cfg.Foreground` is set:

1. Generate a random UUIDv4 (`newSessionID`, `crypto/rand` — stdlib, no new
   dependency) and pass it as `--session-id`.
2. Exec `claude --session-id <id> --model M --max-turns N <passthrough> --
   <prompt>` (no `-p`, no `--output-format json`) with `cmd.Stdin/Stdout/Stderr`
   set to this process's own, so the session is genuinely attached to the
   terminal burn is running in.
3. After `cmd.Run()` returns, recover the turn count by globbing
   `~/.claude/projects/*/<id>.jsonl` (`findTranscript` — sidesteps needing to
   reproduce Claude Code's cwd-to-directory-name encoding) and counting
   `"type":"assistant"` lines (`countAssistantTurns`). A missing/unreadable
   transcript is non-fatal — turns stay 0 and a warning prints to stderr.
4. `IsError` comes from the process exit code; `CostUSD` stays 0.

Verified empirically before writing this: `claude --session-id <uuid> -p
--output-format json --max-turns 1 -- "..."` accepts `--session-id` and a
`--`-terminated positional prompt (not print-only, per `claude --help`), and
the resulting transcript's assistant-line count matches the real
`num_turns` from the headless JSON result exactly.

The nonce `burn` normally appends to a goal (to defeat identical-prompt
caching across `--jobs`) is skipped for `--foreground` — a human reads that
prompt literally, and `--foreground` already forces `--jobs 1` so there is no
cache collision to defeat.

### Accounting

`Record` gains a `Foreground bool` field. `burn overview` (`GoalSummary`)
gains a parallel `PartialCost bool`, set whenever any session in a group ran
foreground — mirroring the existing `PartialDuration` convention. Rendering:
`n/a` when the group's cost is entirely unknown (all-foreground), `$X.XXXX
(partial)` when a group mixes foreground and headless sessions (so a real,
non-zero total isn't mistaken for the whole truth). `burn process`'s digest
table (`formatDigest`) applies the same `n/a` rule per item.

### Bundled fix

While touching `cmdProcess`'s preflight call, fixed a pre-existing gap: it
never checked `pc.Run.SkipPreflight`, so `burn process --skip-preflight` was
silently ignored (unlike `burn run`, which already respected the flag file
`doLaunch`). Same one-line pattern as `doLaunch`.
