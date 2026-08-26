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
func setup(sandboxImage string) error {
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

	// 6. --sandbox is an opt-in extra: never hardFail here. It's only a hard
	// requirement at launch time, and only if --sandbox was actually passed.
	checkSandboxExtra(ok, warn, sandboxImage)

	fmt.Println()
	if hardFail {
		return fmt.Errorf("setup incomplete — resolve the [FAIL] items above")
	}
	fmt.Println("setup OK — try: burn --dry-run --goal x")
	return nil
}

// checkSandboxExtra reports --sandbox prerequisites as informational only
// (never hardFail) — burn works fully without any of this. image is the
// configured --sandbox-image (parsed the same as any other flag before
// dispatching to setup — see run() in main.go), not a hardcoded literal, so
// this checks whatever image a real launch would actually use.
func checkSandboxExtra(ok, warn func(string), image string) {
	if _, err := exec.LookPath("docker"); err != nil {
		warn("sandbox extra: docker not found — optional, only needed for --sandbox")
		return
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		warn("sandbox extra: docker daemon not reachable — optional, only needed for --sandbox")
		return
	}
	ok("sandbox extra: docker found and reachable")

	if _, err := exec.LookPath("osb"); err != nil {
		warn("sandbox extra: `osb` (OpenSandbox CLI) not found — install it before using --sandbox")
	} else {
		ok("sandbox extra: `osb` found on PATH")
	}

	if err := exec.Command("docker", "image", "inspect", image).Run(); err != nil {
		warn(fmt.Sprintf("sandbox extra: image %s not built — see Dockerfile.sandbox", image))
	} else {
		ok(fmt.Sprintf("sandbox extra: %s image present", image))
	}

	if _, err := exec.LookPath("gh"); err != nil {
		warn("sandbox extra: `gh` not found — only needed if a --sandbox goal opens a PR")
	} else {
		ok("sandbox extra: `gh` found on PATH")
	}
}
