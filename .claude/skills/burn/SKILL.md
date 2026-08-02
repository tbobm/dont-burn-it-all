---
name: burn
description: Use idle Claude Code subscription quota on real work up to a threshold, or monitor the 5-hour window. Use when the user says "burn quota", "use my remaining session", "prepare my reviews with burn", "watch my usage", or asks to set up/run the dont-burn-it-all tool in this repo.
---

This repo builds `burn`, a CLI that spends your subscription 5-hour quota on real headless work and
stops at a threshold. Drive it in three steps; never re-implement its logic in the loop.

## 1. Ensure it's built and configured

```sh
test -x ./burn || go build -o ./burn .
./burn setup
```

Relay the `setup` checklist. On any `[FAIL]` (no `claude`, no OAuth token, endpoint unreachable),
report the remediation and STOP — do not try to burn. `[warn]` lines are fine to proceed past.

## 2. Pick a mode

- **Launch** (spend quota on a task): needs `--goal`. Each launch runs `--jobs` sessions to
  completion and stops — it does not loop. Start each launch deliberately.
  ```sh
  ./burn --goal "<real task>" --jobs 4 --target 80
  ```
  Sessions prompt for permissions unless you pass `--dangerously-skip-permissions` (opt-in;
  only with a throwaway `--workdir`).
- **Watch** (governor only): polls usage every ~180s and notifies at the target, spawning nothing.
  ```sh
  ./burn --watch --target 80
  ```

The first launch runs a ~3-minute preflight proving sessions bill to the subscription (not API).
This is intentional. If it hard-aborts, relay the fix (`export CLAUDE_CODE_OAUTH_TOKEN=$(claude
setup-token)`, unset `ANTHROPIC_API_KEY`) and stop.

## 3. Run it without blocking the loop (sub-agent + Monitor)

`burn --watch` and multi-job launches are long-running. Do NOT sit in a foreground wait.

- **As a sub-agent**: the main loop can dispatch this skill to a sub-agent (Agent tool) so `burn`
  runs isolated while the main session continues. The sub-agent runs the steps above and returns a
  summary (final 5-hour %, sessions run, errors).
- **With Monitor**: run `burn` as a background job (`run_in_background: true`), then use the
  **Monitor** tool with an until-loop to wait cheaply for a condition instead of polling in
  context — e.g. until the background job exits, or until the last line of
  `~/.claude/burn/worker.jsonl` shows `five_hour_before >= target`. Report when the condition trips.

Pick a `--target` above the current 5-hour % (see `./burn --dry-run --goal x`); a target below it
makes launch refuse immediately by design.
