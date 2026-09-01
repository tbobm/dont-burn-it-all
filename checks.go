package main

import (
	"encoding/json"
	"fmt"
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
// front — it always resolves from the branch.
func prForBranch(dir string) (number int, url string, err error) {
	cmd := exec.Command("gh", "pr", "view", "--json", "number,url")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return 0, "", fmt.Errorf("no PR found for the current branch in %s: %w", dir, err)
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

// fetchChecks runs one `gh pr checks` poll for the given PR number in dir.
// gh's own exit status is not an error here: exit 8 means "checks pending"
// and exit 1 means "a check failed" — both expected mid-poll, so only stdout
// is trusted.
func fetchChecks(dir string, prNumber int) ([]ghCheck, error) {
	cmd := exec.Command("gh", "pr", "checks", fmt.Sprintf("%d", prNumber), "--json", "name,state,bucket,link")
	cmd.Dir = dir
	out, _ := cmd.Output()
	return parseGHChecks(out)
}

// waitForCheck resolves the PR for cfg's repo/workdir, then polls its checks
// until every check whose name contains cfg.WaitForCheck reaches a terminal
// state or cfg.WaitTimeout elapses. Prints a summary and writes one "check"
// record to the store either way.
func waitForCheck(cfg Config, store *Store) error {
	dir := cfg.Repo
	if dir == "" {
		dir = cfg.Workdir
	}

	prNumber, prURL, err := prForBranch(dir)
	if err != nil {
		return fmt.Errorf("--wait-for-check: %w", err)
	}
	fmt.Printf("wait-for-check: watching %s for checks matching %q (timeout %s)\n", prURL, cfg.WaitForCheck, cfg.WaitTimeout)

	deadline := time.Now().Add(cfg.WaitTimeout)
	var matched []ghCheck
	for {
		checks, err := fetchChecks(dir, prNumber)
		if err != nil {
			return fmt.Errorf("--wait-for-check: %w", err)
		}
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
