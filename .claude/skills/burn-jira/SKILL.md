---
name: burn-jira
description: Work labelled Jira tickets unattended with idle Claude Code subscription quota — set up, preview, schedule, and report on a `burn process jira` run. Use when the user says "work my Jira tickets while I'm out", "auto pick labelled tickets", "burn my backlog", "run this over the weekend / overnight / while I'm OOO", or asks what happened during an unattended burn run.
---

`burn process jira` runs one headless session per labelled Jira ticket, stops at a quota
threshold, and leaves a digest behind. This skill is the setup and operation path for it.

The order below matters. Never skip straight to a real run: an unattended loop that picks the
wrong tickets spends quota on work nobody wanted, and a `--mode implement` loop opens pull
requests. Preview first, every time.

## 0. Prerequisites

```sh
command -v burn || { test -x ./burn && echo "./burn"; } || echo "MISSING"
burn setup
```

If `burn` is `MISSING`, follow the `burn` skill's install step (ask first, do not install
silently). In `burn setup` output, the lines that matter here are the `jira source:` ones:

- `acli` not found → the user installs the [Atlassian CLI](https://developer.atlassian.com/cloud/acli/).
- `acli is not authenticated` → the user runs `acli jira auth login`. **They must run it**; it is
  interactive and needs their credentials.

Any `[FAIL]` line: relay the remediation and stop.

## 1. Agree the selector, then prove it

Ask which tickets qualify. Most teams answer with a label they already apply during triage
(`claude-ready`, `ai-ok`, `burn`); if they have none, propose one — a dedicated label is the
kill switch that scales, since removing it removes the ticket from every future run.

Preview the exact set before anything runs:

```sh
burn connect jira --project SUDS --label claude-ready --status "To Refine,Selected for Development"
```

Show the user the ticket list and get explicit confirmation. `--project` / `--label` /
`--status` compile to JQL (printed as the `query:` line); `--query "<JQL>"` is the escape hatch
for anything more complex. Note that `burn process` orders by `created ASC`, so the oldest
qualifying ticket goes first.

## 2. Pick the mode

| Mode | What each session does | Needs |
|---|---|---|
| `enrich` (default) | reads the ticket, posts **one** refinement comment | nothing but acli |
| `implement` | branch, change, tests, **draft** PR, comment linking it | `--repo` |
| `auto` | `enrich` for early-status tickets, `implement` for ready ones | `--repo` (without it, always enriches) |

Start a user at `enrich`. It cannot touch a repository, so it is the only mode allowed to run
with `--jobs > 1` — which is also what actually burns idle quota, several tickets at a time.

Move to `implement` only once they have seen enrich output they liked. Both built-in prompts
carry their guardrails (draft PRs only, never push to the default branch, never merge, never
force-push, comment instead of guessing on an underspecified ticket). `--prompt-template FILE`
overrides them with a `text/template` rendering `.Key .Summary .Status .Labels .Source .Mode
.Repo` — if the user wants their own prompt, keep the guardrail lines.

## 3. Dry-run it

```sh
burn process jira --label claude-ready --mode auto --repo ~/code/myrepo --dry-run
```

This prints the compiled query, current usage against the targets, the picked tickets with each
one's resolved mode, and the full rendered prompt for the first one. Read the prompt back to the
user. `--dry-run` is the only mode that does not require `--dangerously-skip-permissions`.

## 4. Bound the run before it is unattended

An unattended loop with no bounds can spend a whole 7-day window on failing sessions. Set these
deliberately and tell the user what each one means:

- `--target 80 --weekly-target 50` — re-checked **before every ticket**, not just at the start.
  For an overnight or weekend run, `--weekly-target` is the one that matters.
- `--max-items 6` — hard cap on tickets started (default 5).
- `--max-errors 2` — stop after N consecutive failures, so a broken repo does not burn the window.
- `--max-runtime 8h` — stop starting tickets past this point.
- `--stop-file ~/.claude/burn/STOP` — the kill switch. `touch` that path and a run in flight
  stops before its next ticket; a new run refuses to start at all, before spending the preflight
  probe. Tell the user this path, and remember to `rm` it before the next intended run.
- `--dangerously-skip-permissions` — **required** for a real run. A headless session that hits a
  permission prompt hangs forever, so `burn process` refuses to start without it.

For `implement`, prefer `--sandbox --repo ~/code/myrepo`: writes happen in a Docker sandbox
instead of the user's checkout. It needs `docker` + `osb` + the image (`just build-sandbox-image`)
and supports one job at a time. Without `--sandbox`, unattended sessions write directly in the
real repository — say that out loud before running it.

Already-processed tickets are skipped automatically (the JSONL store remembers item keys), so a
resumed or repeated run never redoes finished work. `--redo` overrides that.

## 5. Wire the report

The user is away; the run has to reach them.

```sh
# Slack incoming webhook — the full markdown digest
export BURN_DIGEST_CMD='curl -s -XPOST "$SLACK_WEBHOOK" --json "{\"text\": $(printf %s "$BURN_MSG" | python3 -c "import json,sys;print(json.dumps(sys.stdin.read()))")}"'
# and/or keep a file to read on return
--digest ~/.claude/burn/digests/$(date +%F).md
```

- `BURN_DIGEST_CMD` gets the whole digest in `$BURN_MSG`; it falls back to `BURN_NOTIFY_CMD`.
- `BURN_NOTIFY_CMD` gets the one-line headline, plus per-ticket lines if `--notify-each` is set.
  With `--notify-each` the hook also sees `BURN_ITEM_KEY`, `BURN_ITEM_SUMMARY`,
  `BURN_ITEM_STATUS`, `BURN_ITEM_MODE`.
- `BURN_CLAIM_CMD` runs before each ticket with the same `BURN_ITEM_*` variables; a non-zero exit
  **skips** that ticket. Use it to label the ticket in Jira as taken, so a second runner (another
  machine, a colleague) leaves it alone. Local dedup does not cross machines; this hook is what does.

## 6. Schedule it

`burn process` is one bounded run, not a daemon. Something else decides when it fires.

```sh
# every weekday at 19:00, host cron
0 19 * * 1-5 /usr/local/bin/burn process jira --label claude-ready --mode enrich --jobs 2 \
  --target 80 --weekly-target 50 --max-items 6 --max-runtime 4h \
  --dangerously-skip-permissions --digest "$HOME/.claude/burn/digests/$(date +\%F).md" \
  >> "$HOME/.claude/burn/process.log" 2>&1
```

Cron needs an absolute `burn` path and `CLAUDE_CODE_OAUTH_TOKEN` in its environment — a cron
shell has neither the user's PATH nor their keychain. Have them run `claude setup-token` and put
the token in the crontab or a sourced env file. On macOS prefer a launchd agent (cron there
cannot reach the keychain); on Linux a systemd user timer with `Environment=` is cleaner than
crontab.

From inside Claude Code, the `/loop` skill can re-run this on an interval within a session, and a
Routine can fire a fresh session on a schedule. Both keep the quota accounting identical — the
sessions `burn` spawns are what spend it either way.

## 7. Report back when they return

```sh
burn overview --group item      # per ticket: sessions, cost, turns, errors, time
burn overview                   # per run shape: "process jira enrich" in aggregate
cat ~/.claude/burn/digests/*.md
```

Lead with what changed in Jira and GitHub, not with the numbers: which tickets got comments,
which got draft PRs, which failed and why, and the stop reason from the digest (the answer to
"why only 3 of 6"). Then the quota state. Check `burn overview --group item` for repeated errors
on one ticket — that ticket usually needs a human, not another run.

## Do not

- Do not run a real `burn process` before the user has seen and confirmed the ticket list.
- Do not sit in a foreground wait on a long run. Start it in the background and use **Monitor**
  with an until-loop, or hand it to a sub-agent (see the `burn` skill's step 3).
- Do not raise `--jobs` above 1 for `implement`/`auto`; concurrent sessions in one checkout race
  on the working tree, and `burn` refuses it.
- Do not write back to Jira yourself. The sessions do that with acli; `burn` only orchestrates.
