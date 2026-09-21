package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// hostileEnvVars, if set, can silently route `claude -p` to pay-per-token API
// billing instead of the subscription window. We refuse to run when any is set
// (unless --i-know-this-bills-api) and strip them from every child's env.
var hostileEnvVars = []string{
	"ANTHROPIC_API_KEY",
	"ANTHROPIC_AUTH_TOKEN",
	"CLAUDE_CODE_USE_BEDROCK",
	"CLAUDE_CODE_USE_VERTEX",
}

// claudeResult is the subset of `claude -p --output-format json` we care about.
type claudeResult struct {
	Subtype      string  `json:"subtype"`
	TotalCostUSD float64 `json:"total_cost_usd"`
	IsError      bool    `json:"is_error"`
	NumTurns     int     `json:"num_turns"`
	SessionID    string  `json:"session_id"`
}

// checkHostileEnv returns the names of any billing-risk env vars currently set.
func checkHostileEnv() []string {
	var found []string
	for _, k := range hostileEnvVars {
		if os.Getenv(k) != "" {
			found = append(found, k)
		}
	}
	return found
}

// awsCredEnvVars are the AWS SDK/CLI credential env vars, which take
// precedence over AWS_PROFILE. Only scrubbed when a --sandbox session sets
// --aws-profile — that combination means the operator wants the sandboxed
// agent limited to the mounted profile, and any ambient AWS_ACCESS_KEY_ID/etc.
// on the host (e.g. from an unrelated admin-role shell) would otherwise
// silently override it if `osb` forwards this process's env into the
// container. Host mode never scrubs these — it's documented to inherit the
// full env.
var awsCredEnvVars = []string{
	"AWS_ACCESS_KEY_ID",
	"AWS_SECRET_ACCESS_KEY",
	"AWS_SESSION_TOKEN",
}

// childEnv builds a scrubbed environment: billing-risk vars removed, the resolved
// subscription token pinned as CLAUDE_CODE_OAUTH_TOKEN. This deterministically
// forces subscription auth regardless of the parent shell's state. scrubAWS
// additionally drops the AWS credential env vars — see awsCredEnvVars.
func childEnv(token string, scrubAWS bool) []string {
	drop := map[string]bool{"CLAUDE_CODE_OAUTH_TOKEN": true}
	for _, k := range hostileEnvVars {
		drop[k] = true
	}
	if scrubAWS {
		for _, k := range awsCredEnvVars {
			drop[k] = true
		}
	}
	var env []string
	for _, kv := range os.Environ() {
		if i := strings.IndexByte(kv, '='); i >= 0 && drop[kv[:i]] {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "CLAUDE_CODE_OAUTH_TOKEN="+token)
}

// claudeArgs builds the full `claude` arg list: burn's own flags, then
// cfg.ClaudeArgs (from a `--` separator on `burn run`) appended last, so a
// user-supplied --model/--max-turns overrides burn's default. Pure — no
// exec — so it's table-testable like resolveCommand in main.go.
func claudeArgs(cfg Config, prompt string) []string {
	args := []string{
		"-p", prompt,
		"--output-format", "json",
		"--model", cfg.Model,
		"--max-turns", fmt.Sprintf("%d", cfg.MaxTurns),
	}
	if cfg.SkipPermissions {
		args = append(args, "--dangerously-skip-permissions")
	}
	return append(args, cfg.ClaudeArgs...)
}

// resumeInArgs reports whether a passthrough arg list resumes an existing
// session (-r/--resume[=id]). Used to refuse --jobs > 1 (N jobs resuming one
// session id is garbage) and to skip the launch nonce (it would inject a
// literal "(nonce=N)" into a resumed conversation's next turn).
func resumeInArgs(args []string) bool {
	for _, a := range args {
		if a == "-r" || a == "--resume" || strings.HasPrefix(a, "--resume=") {
			return true
		}
	}
	return false
}

// runClaude spawns one headless `claude -p` and parses its JSON result. When
// cfg.Sandbox is set (an opt-in extra, see sandbox.go), it runs inside a fresh
// local OpenSandbox instead of directly on the host — same args, same JSON
// parse, different exec target. preflight() calls this same function for its
// probe session with cfg carried through unchanged, so a sandboxed run's
// preflight proof runs sandboxed too — as long as preflight's own freshness
// stamp is keyed by mode (it is, see stampPath in main.go), so a host-mode
// proof can never be mistaken for a sandboxed one.
func runClaude(cfg Config, token, prompt string) (claudeResult, error) {
	if cfg.Foreground {
		return runClaudeForeground(cfg, token, prompt)
	}
	args := claudeArgs(cfg, prompt)

	var cmd *exec.Cmd
	var cancel context.CancelFunc
	if cfg.Sandbox {
		id, cleanup, err := ensureSandbox(cfg, token, resolveGHToken(cfg))
		if err != nil {
			return claudeResult{}, err
		}
		defer cleanup()
		// Exec the entrypoint wrapper ensureSandbox wrote, not `claude`
		// directly — it exports the OAuth/GH tokens from files inside the
		// sandbox (never from argv) before handing off to claude with args
		// untouched. -t is a second, server-side enforcement of
		// sandboxCommandTimeout, on top of the client-side context below —
		// belt and suspenders against either the osb CLI or claude hanging.
		osbArgs := []string{
			"command", "run", id,
			"-o", "raw",
			"-w", sandboxMountPath,
			"-t", fmt.Sprintf("%dm", int(sandboxCommandTimeout.Minutes())),
			"--", "sh", sandboxEntrypointFile,
		}
		osbArgs = append(osbArgs, args...)
		var ctx context.Context
		ctx, cancel = context.WithTimeout(context.Background(), sandboxCommandTimeout)
		cmd = exec.CommandContext(ctx, "osb", osbArgs...)
		// Same scrub as the host branch below: the sandboxed claude only ever
		// gets its token from the file ensureSandbox wrote (never from env),
		// but `osb` itself still inherits this process's env unless told
		// otherwise — strip billing-risk vars from it too, in case osb
		// forwards its own env into the container. Also scrub AWS credential
		// vars when --aws-profile is set, so an ambient host credential can't
		// override the mounted profile (see awsCredEnvVars).
		cmd.Env = childEnv(token, cfg.AWSProfile != "")
	} else {
		cmd = exec.Command("claude", args...)
		cmd.Env = childEnv(token, false)
		cmd.Dir = cfg.Workdir
	}
	if cancel != nil {
		defer cancel()
	}

	out, err := cmd.Output()
	var res claudeResult
	if jErr := json.Unmarshal(out, &res); jErr != nil {
		if err != nil {
			return res, fmt.Errorf("claude -p failed: %w", err)
		}
		return res, fmt.Errorf("parsing claude -p output: %w", jErr)
	}
	return res, nil
}

// newSessionID generates a random UUIDv4, in the same format `claude` prints
// as session_id in a headless result. Pinning one up front (via --session-id)
// is what lets runClaudeForeground find the session's transcript afterward.
func newSessionID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// countAssistantTurns counts `"type":"assistant"` lines in a session
// transcript file — pure and file-based so it's unit-testable against a
// fixture, separate from findTranscript's globbing.
func countAssistantTurns(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	turns := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		var line struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(sc.Bytes(), &line) == nil && line.Type == "assistant" {
			turns++
		}
	}
	return turns, sc.Err()
}

// findTranscript locates a session's transcript by globbing for its id across
// every project directory under home — sidestepping any need to reproduce
// Claude Code's own cwd-to-directory-name encoding.
func findTranscript(home, sessionID string) (string, error) {
	matches, err := filepath.Glob(filepath.Join(home, ".claude", "projects", "*", sessionID+".jsonl"))
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("no transcript found for session %s", sessionID)
	}
	return matches[0], nil
}

// runClaudeForeground runs `claude` interactively, attached to this process's
// own stdio, instead of headless `-p` — the point of --foreground is a human
// watching, and approving or denying, the session live. There is no
// --output-format json result to parse afterward: NumTurns is recovered by
// counting assistant turns in the session transcript, found via the
// --session-id pinned up front; CostUSD is left at 0 (see Record.Foreground
// in store.go for why cost is deliberately not reconstructed from the
// transcript's raw token counts).
func runClaudeForeground(cfg Config, token, prompt string) (claudeResult, error) {
	sessionID, err := newSessionID()
	if err != nil {
		return claudeResult{}, fmt.Errorf("generating session id: %w", err)
	}

	args := []string{"--session-id", sessionID, "--model", cfg.Model, "--max-turns", fmt.Sprintf("%d", cfg.MaxTurns)}
	args = append(args, cfg.ClaudeArgs...)
	args = append(args, "--", prompt)

	cmd := exec.Command("claude", args...)
	cmd.Env = childEnv(token, false)
	cmd.Dir = cfg.Workdir
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr

	runErr := cmd.Run()

	turns := 0
	if home, herr := os.UserHomeDir(); herr == nil {
		if path, ferr := findTranscript(home, sessionID); ferr == nil {
			if n, cerr := countAssistantTurns(path); cerr == nil {
				turns = n
			} else {
				fmt.Fprintf(os.Stderr, "burn: reading transcript for session %s: %v\n", sessionID, cerr)
			}
		} else {
			fmt.Fprintf(os.Stderr, "burn: could not recover turn count for session %s: %v\n", sessionID, ferr)
		}
	}
	return claudeResult{SessionID: sessionID, NumTurns: turns, IsError: runErr != nil}, runErr
}

// preflight is the subscription-metering gate. It refuses to run under a hostile
// env, then empirically proves a single `claude -p` moves the real 5-hour
// utilization. If it does not move, the burst was billed to API, not the
// subscription — we hard-abort before starting any real session.
func preflight(cfg Config, uc *UsageClient, token string) error {
	if bad := checkHostileEnv(); len(bad) > 0 && !cfg.AllowBillsAPI {
		return fmt.Errorf("billing-risk env vars set: %s\n"+
			"These can route `claude -p` to pay-per-token API billing (not your subscription).\n"+
			"Unset them, or pass --i-know-this-bills-api to override", strings.Join(bad, ", "))
	}

	if isPreflightFresh(cfg.Sandbox) {
		fmt.Println("preflight: recent metering proof found, skipping the 3-minute check")
		return nil
	}

	before, err := uc.Get()
	if err != nil {
		return fmt.Errorf("preflight usage read: %w", err)
	}
	fmt.Printf("preflight: proving subscription metering (5h utilization now %.1f%%)...\n", before.FiveHour.Utilization)
	fmt.Println("preflight: running one probe session, then waiting ~3m for the server to reflect it")

	probe := cfg
	probe.MaxTurns = 1
	// Never forward passthrough args to the probe — a --resume there would
	// resume the user's real session for a throwaway "ok" turn.
	probe.ClaudeArgs = nil
	// The probe always runs headless, even under --foreground: it's an
	// internal metering check, not something the human needs to watch, and
	// its result (a claudeResult with TotalCostUSD/IsError) requires the
	// headless -p path to exist at all.
	probe.Foreground = false
	if _, err := runClaude(probe, token, "Reply with the single word: ok"); err != nil {
		return fmt.Errorf("preflight probe session failed: %w", err)
	}

	after, err := uc.GetWaitingFresh()
	if err != nil {
		return fmt.Errorf("preflight usage recheck: %w", err)
	}
	if after.FiveHour.Utilization <= before.FiveHour.Utilization {
		return fmt.Errorf("HARD ABORT: 5h utilization did not rise (%.1f%% -> %.1f%%) after a probe session.\n"+
			"The probe did NOT consume your subscription window — it likely billed the API.\n"+
			"Set CLAUDE_CODE_OAUTH_TOKEN via `claude setup-token` and unset ANTHROPIC_API_KEY, then retry",
			before.FiveHour.Utilization, after.FiveHour.Utilization)
	}
	fmt.Printf("preflight: OK — utilization moved %.1f%% -> %.1f%%, subscription metering confirmed\n",
		before.FiveHour.Utilization, after.FiveHour.Utilization)
	markPreflightFresh(cfg.Sandbox)
	return nil
}

// launchResult aggregates one launch invocation's outcome.
type launchResult struct {
	sessions int
	errors   int
	costUSD  float64
}

// launch starts cfg.Jobs copies of the single goal concurrently, each running to
// completion, logging one record per session. It does NOT loop or refill.
func launch(cfg Config, uc *UsageClient, token string, store *Store) (launchResult, error) {
	before, err := uc.Get()
	if err != nil {
		return launchResult{}, err
	}
	beforePct := before.FiveHour.Utilization

	var (
		wg  sync.WaitGroup
		mu  sync.Mutex
		agg launchResult
		sem = make(chan struct{}, cfg.Jobs)
	)
	for i := 0; i < cfg.Jobs; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()

			start := time.Now().UTC()
			// Nonce only defeats identical-prompt server caching; the work is
			// real. Skip it on --resume — a resumed conversation gets its next
			// real turn, not a literal "(nonce=N)" tacked onto the goal. Skip it
			// on --foreground too — a human reads this prompt literally, and
			// --foreground already forces --jobs 1 so there is no cache
			// collision to defeat.
			prompt := cfg.Goal
			if !resumeInArgs(cfg.ClaudeArgs) && !cfg.Foreground {
				prompt = fmt.Sprintf("%s (nonce=%d)", cfg.Goal, i)
			}
			res, runErr := runClaude(cfg, token, prompt)

			mu.Lock()
			agg.sessions++
			agg.costUSD += res.TotalCostUSD
			if runErr != nil || res.IsError {
				agg.errors++
			}
			mu.Unlock()

			store.Write(Record{
				TS:             time.Now().UTC().Format(time.RFC3339),
				SessionID:      res.SessionID,
				Goal:           cfg.Goal,
				StartedAt:      start.Format(time.RFC3339),
				Model:          cfg.Model,
				CostUSD:        res.TotalCostUSD,
				NumTurns:       res.NumTurns,
				IsError:        runErr != nil || res.IsError,
				FiveHourBefore: beforePct,
				Foreground:     cfg.Foreground,
			})
			if runErr != nil {
				fmt.Fprintf(os.Stderr, "job %d error: %v\n", i, runErr)
			}
		}(i)
	}
	wg.Wait()

	// max-usd-guard: opt-in (0 = disabled). Under a subscription this should be ~0;
	// a climbing total is a signal of silent API routing.
	if cfg.MaxUSDGuard > 0 && agg.costUSD > cfg.MaxUSDGuard {
		return agg, fmt.Errorf("ABORT: reported cost $%.2f exceeded --max-usd-guard $%.2f — possible API billing",
			agg.costUSD, cfg.MaxUSDGuard)
	}
	return agg, nil
}
