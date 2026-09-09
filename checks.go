package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// checkPollInterval paces `gh pr checks` polling. A Spacelift preview check
// can take seconds to minutes to appear on a freshly opened PR, so this
// doesn't need to be tight.
// ponytail: fixed value; make it a flag if a goal legitimately needs a
// different cadence.
const checkPollInterval = 15 * time.Second

// ghExecTimeout bounds a single `gh` invocation (prForBranch, fetchChecks) so
// a stalled gh process (network blip, a stuck credential prompt) can't hang
// the poll loop forever — without this, cfg.WaitTimeout is never enforced
// because the loop never gets back around to check the deadline.
const ghExecTimeout = 30 * time.Second

// ghCheck is the subset of `gh pr checks --json ...` we need.
type ghCheck struct {
	Name   string `json:"name"`
	State  string `json:"state"`
	Bucket string `json:"bucket"` // pass|fail|pending|skipping|cancel
	Link   string `json:"link"`
}

// parseGHChecks parses `gh pr checks --json name,state,bucket,link` output.
// Pure — no exec — so it's table-testable without a real gh/PR.
func parseGHChecks(out []byte) ([]ghCheck, error) {
	var checks []ghCheck
	if err := json.Unmarshal(out, &checks); err != nil {
		return nil, fmt.Errorf("parsing gh pr checks output: %w", err)
	}
	return checks, nil
}

// classifyChecks filters checks whose name contains pattern, then reports
// whether any of those matches are still pending (no terminal bucket yet) or
// failed (bucket fail/cancel). Pure — table-testable.
func classifyChecks(checks []ghCheck, pattern string) (matched []ghCheck, pending, failed bool) {
	for _, c := range checks {
		if !strings.Contains(c.Name, pattern) {
			continue
		}
		matched = append(matched, c)
		switch c.Bucket {
		case "pass", "skipping":
			// terminal, ok
		case "fail", "cancel":
			failed = true
		default: // "pending" or anything else not yet terminal
			pending = true
		}
	}
	return matched, pending, failed
}

// prForBranch resolves the PR (number and URL) for dir's current branch via
// `gh pr view`. The session creates the PR, so burn can't know the number up
// front — it always resolves from the branch. ctx bounds the exec (see
// ghExecTimeout) so a stalled gh process can't hang the caller forever.
func prForBranch(ctx context.Context, dir string) (number int, url string, err error) {
	cmd := exec.CommandContext(ctx, "gh", "pr", "view", "--json", "number,url")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return 0, "", fmt.Errorf("no PR found for the current branch in %s: %w", dir, ghExecError(err))
	}
	var res struct {
		Number int    `json:"number"`
		URL    string `json:"url"`
	}
	if jErr := json.Unmarshal(out, &res); jErr != nil {
		return 0, "", fmt.Errorf("parsing gh pr view output: %w", jErr)
	}
	return res.Number, res.URL, nil
}

// ghExecError enriches a gh exec error with its stderr, when available —
// cmd.Output() populates *exec.ExitError.Stderr since these callers never set
// cmd.Stderr themselves. Without this, a real failure (expired auth, rate
// limit) surfaces only as a bare exit-status error with no indication why.
func ghExecError(err error) error {
	var exitErr *exec.ExitError
	if ok := errors.As(err, &exitErr); ok && len(exitErr.Stderr) > 0 {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(exitErr.Stderr)))
	}
	return err
}

// classifyFetchResult turns one `gh pr checks` invocation's raw stdout/error
// into either the parsed checks or a real error. gh's own exit status is not
// an error by itself: exit 8 means "checks pending" and exit 1 means "a check
// failed" — both expected mid-poll and both still print valid JSON to
// stdout. Only when stdout *also* fails to parse (expired auth, a deleted PR,
// a rate limit — cases with no usable JSON) does this report a failure, and
// it reports the real gh error/stderr rather than the parse error, since the
// parse error ("unexpected end of JSON input") is never the actual cause.
// Pure — table-testable without a real gh/PR.
func classifyFetchResult(out []byte, execErr error) ([]ghCheck, error) {
	if checks, pErr := parseGHChecks(out); pErr == nil {
		return checks, nil
	}
	if execErr != nil {
		return nil, fmt.Errorf("gh pr checks: %w", ghExecError(execErr))
	}
	return nil, fmt.Errorf("gh pr checks: parsing output")
}

// fetchChecks runs one `gh pr checks` poll for the given PR number in dir.
// ctx bounds the exec (see ghExecTimeout).
func fetchChecks(ctx context.Context, dir string, prNumber int) ([]ghCheck, error) {
	cmd := exec.CommandContext(ctx, "gh", "pr", "checks", fmt.Sprintf("%d", prNumber), "--json", "name,state,bucket,link")
	cmd.Dir = dir
	out, err := cmd.Output()
	return classifyFetchResult(out, err)
}

// waitForCheckDir picks the directory the actual session ran in, matching
// runClaude's exec target (runner.go): --sandbox sessions operate on
// cfg.Repo (bind-mounted into the container); host sessions always run in
// cfg.Workdir (cmd.Dir there is never cfg.Repo), so watching cfg.Repo in host
// mode would resolve the wrong repo's branch/PR whenever the two differ.
func waitForCheckDir(cfg Config) string {
	if cfg.Sandbox {
		return cfg.Repo
	}
	return cfg.Workdir
}

// waitForCheck resolves the PR for the session's working directory, then
// polls its checks until every check whose name contains cfg.WaitForCheck
// reaches a terminal state or cfg.WaitTimeout elapses. Prints a summary and
// writes one "check" record to the store either way.
func waitForCheck(cfg Config, store *Store) error {
	dir := waitForCheckDir(cfg)

	prCtx, prCancel := context.WithTimeout(context.Background(), ghExecTimeout)
	prNumber, prURL, err := prForBranch(prCtx, dir)
	prCancel()
	if err != nil {
		return fmt.Errorf("--wait-for-check: %w", err)
	}
	fmt.Printf("wait-for-check: watching %s for checks matching %q (timeout %s)\n", prURL, cfg.WaitForCheck, cfg.WaitTimeout)

	deadline := time.Now().Add(cfg.WaitTimeout)
	var matched []ghCheck
	for {
		pollCtx, pollCancel := context.WithTimeout(context.Background(), ghExecTimeout)
		checks, err := fetchChecks(pollCtx, dir, prNumber)
		pollCancel()
		if err != nil {
			// A single flaky `gh` call (rate limit, network blip) must not
			// kill a 30-minute wait — log and keep polling until the deadline.
			fmt.Fprintf(os.Stderr, "wait-for-check: poll failed, retrying: %v\n", err)
		} else {
			var pending, failed bool
			matched, pending, failed = classifyChecks(checks, cfg.WaitForCheck)
			if len(matched) > 0 && !pending {
				printCheckSummary(matched)
				writeCheckRecord(store, prURL, matched, failed)
				if failed {
					return fmt.Errorf("--wait-for-check: %q check(s) failed on %s", cfg.WaitForCheck, prURL)
				}
				return nil
			}
		}
		if time.Now().After(deadline) {
			writeCheckRecord(store, prURL, matched, true)
			return fmt.Errorf("--wait-for-check: timed out after %s waiting for %q checks on %s",
				cfg.WaitTimeout, cfg.WaitForCheck, prURL)
		}
		time.Sleep(checkPollInterval)
	}
}

func writeCheckRecord(store *Store, prURL string, matched []ghCheck, isError bool) {
	store.Write(Record{
		TS:      time.Now().UTC().Format(time.RFC3339),
		Kind:    "check",
		PRURL:   prURL,
		Checks:  matched,
		IsError: isError,
	})
}

func printCheckSummary(matched []ghCheck) {
	fmt.Println("wait-for-check: result")
	for _, c := range matched {
		fmt.Printf("  %-10s %-40s %s\n", c.Bucket, c.Name, c.Link)
	}
}
