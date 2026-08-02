package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// setup is a self-check, in the spirit of `dx setup`: it verifies everything
// `burn` needs to run sessions that bill to the subscription, and prints a
// checklist with remediation. Exits non-zero if a hard requirement is missing.
func setup() error {
	var hardFail bool
	ok := func(msg string) { fmt.Println("[ ok ] " + msg) }
	warn := func(msg string) { fmt.Println("[warn] " + msg) }
	fail := func(msg string) { fmt.Println("[FAIL] " + msg); hardFail = true }

	fmt.Println("burn setup — checking configuration")
	fmt.Println()

	// 1. claude CLI is required to launch sessions.
	if _, err := exec.LookPath("claude"); err != nil {
		fail("`claude` not on PATH — install Claude Code first")
	} else {
		ok("`claude` found on PATH")
	}

	// 2. Subscription OAuth token resolvable.
	token, source, err := resolveToken()
	if err != nil {
		fail(err.Error())
	} else {
		ok("OAuth token resolved from " + source)
		// Pinning CLAUDE_CODE_OAUTH_TOKEN is the strongest guarantee of
		// subscription (not API) billing for headless runs.
		if os.Getenv("CLAUDE_CODE_OAUTH_TOKEN") == "" {
			warn("CLAUDE_CODE_OAUTH_TOKEN is not set in your shell; burn pins it per-session, " +
				"but for certainty run: export CLAUDE_CODE_OAUTH_TOKEN=$(claude setup-token)")
		} else {
			ok("CLAUDE_CODE_OAUTH_TOKEN is set")
		}
	}

	// 3. Billing-risk env vars must not silently reroute to API billing.
	if bad := checkHostileEnv(); len(bad) > 0 {
		warn(fmt.Sprintf("billing-risk env vars set: %v — burn scrubs these from sessions, "+
			"but unset them to be safe (or pass --i-know-this-bills-api)", bad))
	} else {
		ok("no billing-risk env vars set")
	}

	// 4. Real usage endpoint reachable (only if we have a token).
	if err == nil {
		if uc, e := newUsageClient(); e == nil {
			if u, e2 := uc.Get(); e2 != nil {
				fail("usage endpoint unreachable: " + e2.Error())
			} else {
				ok(fmt.Sprintf("usage endpoint OK — 5-hour at %.1f%%, 7-day at %.1f%%",
					u.FiveHour.Utilization, u.SevenDay.Utilization))
			}
		}
	}
	_ = token

	// 5. Store + scratch dirs writable.
	home, _ := os.UserHomeDir()
	storeDir := filepath.Join(home, ".claude", "burn")
	if e := os.MkdirAll(storeDir, 0o755); e != nil {
		fail("cannot create store dir " + storeDir + ": " + e.Error())
	} else {
		ok("store dir writable: " + storeDir)
	}

	fmt.Println()
	if hardFail {
		return fmt.Errorf("setup incomplete — resolve the [FAIL] items above")
	}
	fmt.Println("setup OK — try: burn --dry-run --goal x")
	return nil
}
