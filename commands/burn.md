---
name: burn
description: Use your Claude Code 5-hour subscription quota with parallel sessions, stopping at a threshold. Runs the `burn` CLI; does not babysit.
---

Run the `burn` CLI once for the user and report its output. Do NOT loop or re-invoke it — each
`/burn` is a single, self-contained launch. The human decides when to run it again.

Steps:
1. Build the binary if it is missing:
   `test -x "${CLAUDE_PLUGIN_ROOT}/burn" || go build -o "${CLAUDE_PLUGIN_ROOT}/burn" "${CLAUDE_PLUGIN_ROOT}"`
2. Run it, passing the user's arguments verbatim:
   `"${CLAUDE_PLUGIN_ROOT}/burn" $ARGUMENTS`
3. Surface the summary: current 5-hour usage %, headroom to target, sessions run, any errors.

Notes:
- Launch mode needs `--goal "<task>"` (e.g. `/burn --goal "write unit tests for pkg/foo" --jobs 4`).
- Sessions prompt for permissions by default; add `--dangerously-skip-permissions` to run unattended.
- `--watch` polls usage and notifies at the target, spawning nothing.
- The first launch runs a ~3-minute preflight that proves sessions bill to the subscription (not
  pay-per-token API); this is intentional and cannot be skipped. If it reports a token or
  billing-env problem, relay the fix and stop — do not try to work around it.
- If a permission prompt blocks reading the OAuth credential, tell the user to run the command
  themselves with `! "${CLAUDE_PLUGIN_ROOT}/burn" $ARGUMENTS`.
