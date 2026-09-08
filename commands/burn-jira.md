---
name: burn-jira
description: Run one unattended pass of `burn process jira` over labelled Jira tickets — one session per ticket, stopping at a quota threshold. Previews first; does not babysit.
---

Run `burn process jira` **once** for the user and report its digest. Do NOT loop or re-invoke it —
each `/burn-jira` is a single bounded pass. A schedule (cron, launchd, a systemd timer, a
Routine) decides when it runs again.

Steps:
1. Build the binary if it is missing:
   `test -x "${CLAUDE_PLUGIN_ROOT}/burn" || go build -o "${CLAUDE_PLUGIN_ROOT}/burn" "${CLAUDE_PLUGIN_ROOT}"`
2. **Preview before spending anything.** Unless the user's arguments already contain `--dry-run`,
   run it with `--dry-run` added first, show the compiled query, the picked tickets with their
   resolved modes, and the rendered prompt, and get confirmation:
   `"${CLAUDE_PLUGIN_ROOT}/burn" process jira $ARGUMENTS --dry-run`
3. Then run it for real, passing the user's arguments verbatim:
   `"${CLAUDE_PLUGIN_ROOT}/burn" process jira $ARGUMENTS`
4. Surface the digest: tickets processed, per-ticket outcome, the stop reason, and usage after.

Notes:
- Selection: `--project` / `--label` / `--status` compile to JQL; `--query "<JQL>"` is the raw
  escape hatch. At least one of `--project` / `--label` is required.
- `--mode enrich` (default) posts one refinement comment per ticket and touches no repository.
  `--mode implement` needs `--repo` and opens draft PRs. `--mode auto` picks per ticket.
- A real run requires `--dangerously-skip-permissions`; `burn process` refuses without it,
  because a headless session that hits a permission prompt hangs forever. Pair it with
  `--sandbox --repo <path>` to keep writes out of the user's checkout.
- Bounds worth setting every time: `--target`, `--weekly-target`, `--max-items`, `--max-errors`,
  `--max-runtime`. The kill switch is `--stop-file` (default `~/.claude/burn/STOP`) — `touch` it
  to stop the loop before the next ticket.
- Tickets already completed in the JSONL store are skipped; `--redo` overrides that.
- Reporting: `--digest <path>` writes markdown, `BURN_DIGEST_CMD` forwards the digest (Slack),
  `BURN_NOTIFY_CMD` gets the headline. Afterwards: `burn overview --group item`.
- The `burn-jira` skill covers the full setup path (acli auth, label convention, scheduling).
- If a permission prompt blocks reading the OAuth credential, tell the user to run the command
  themselves with `! "${CLAUDE_PLUGIN_ROOT}/burn" process jira $ARGUMENTS`.
