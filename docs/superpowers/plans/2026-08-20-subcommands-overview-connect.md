# Subcommand CLI, burn overview, burn connect Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give `burn` a real subcommand dispatcher (`run`/`overview`/`connect`/`setup`), then add `burn overview` (summarize the JSONL activity store) and `burn connect jira` (list matching Jira issues via `acli`) on top of it. Implements #17, #18, #19.

**Architecture:** `main.go` gains a pure `resolveCommand(args) (name, rest)` function plus a `dispatch(args) error` that routes to per-command handlers, each with its own `flag.NewFlagSet`. `run` is today's exact launch/watch/dry-run logic unchanged, just moved off the package-level `flag` set. `overview` and `connect` are new self-contained files reading the existing `Record` JSONL store and shelling out to `acli` respectively.

**Tech Stack:** Go 1.24, stdlib only (`flag`, `bufio`, `encoding/json`, `text/tabwriter`, `os/exec`). No new dependencies — `go.mod` has zero `require` entries today and stays that way.

## Global Constraints

- Zero new dependencies — `go.mod` must not gain a `require` block. Everything is stdlib.
- `bare burn --goal ...` / `burn --dry-run ...` (no subcommand keyword) must keep working exactly as today, including the existing `--goal is required` error on a bare no-flag invocation — this is a hard back-compat requirement, not a nice-to-have.
- Tests follow the existing style in `burn_test.go`: plain `testing` package, no frameworks, table-driven where natural, pure functions tested without shelling out or hitting the network.
- `acli` (Atlassian CLI) is required on `PATH` for `burn connect jira` only — it must not become a requirement for `run`/`overview`/`setup`.
- Commit messages use Conventional Commits (`feat:`, `test:`, `docs:`), matching `git log` history in this repo.
- Run `just vet test` (or `go vet ./... && go test ./...`) before every commit.

---

### Task 1: Subcommand dispatcher (`run`, `setup`, `help`)

**Files:**
- Modify: `main.go` (full rewrite of the dispatch/flag-parsing portion; `dryRun`, `doLaunch`, `watch`, `notify`, `stampPath`, `isPreflightFresh`, `markPreflightFresh` are untouched — copy them through as-is)
- Create: `main_test.go`
- Test: `main_test.go`

**Interfaces:**
- Consumes: `Config` (existing struct, unchanged), `newUsageClient()`, `openStore()`, `dryRun(cfg, uc)`, `doLaunch(cfg, uc, store)`, `watch(cfg, uc, store)`, `setup()` — all existing, unchanged signatures.
- Produces: `resolveCommand(args []string) (name string, rest []string)`, `dispatch(args []string) error`, `cmdRun(args []string) error`, `printUsage()`, `commandHelp map[string]string` — later tasks extend `commandHelp` and add cases to `resolveCommand`/`dispatch`.

- [ ] **Step 1: Write the failing test**

Create `main_test.go`:

```go
package main

import (
	"reflect"
	"testing"
)

func TestResolveCommand(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantName string
		wantRest []string
	}{
		{"bare no args", nil, "run", nil},
		{"bare flags", []string{"--goal", "x"}, "run", []string{"--goal", "x"}},
		{"explicit run", []string{"run", "--goal", "x"}, "run", []string{"--goal", "x"}},
		{"setup", []string{"setup"}, "setup", []string{}},
		{"short help", []string{"-h"}, "help", nil},
		{"long help", []string{"--help"}, "help", nil},
		{"help word", []string{"help"}, "help", nil},
		{"unknown command", []string{"bogus"}, "unknown", []string{"bogus"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotName, gotRest := resolveCommand(c.args)
			if gotName != c.wantName {
				t.Fatalf("name: got %q, want %q", gotName, c.wantName)
			}
			if !reflect.DeepEqual(gotRest, c.wantRest) {
				t.Fatalf("rest: got %#v, want %#v", gotRest, c.wantRest)
			}
		})
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./... -run TestResolveCommand -v`
Expected: build failure — `undefined: resolveCommand`

- [ ] **Step 3: Rewrite `main.go`**

Replace the full contents of `main.go` with:

```go
// Command burn deliberately uses your Claude Code subscription's 5-hour quota by
// launching real work in parallel headless sessions, stopping at a threshold so
// you keep a safety reserve. It meters against the real Anthropic usage endpoint
// and refuses to run unless it can prove the work bills to your subscription
// (not pay-per-token API).
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Config holds all `run` subcommand options.
type Config struct {
	Target          float64
	Jobs            int
	Model           string
	Goal            string
	Watch           bool
	Workdir         string
	Store           string
	MaxTurns        int
	DryRun          bool
	MaxUSDGuard     float64
	AllowBillsAPI   bool
	SkipPermissions bool
}

// commandHelp describes each top-level subcommand for printUsage. Later tasks
// add entries here as they add subcommands.
var commandHelp = map[string]string{
	"run":   "launch or watch sessions against the subscription 5-hour quota",
	"setup": "check burn's configuration (claude, token, endpoint, dirs)",
}

func main() {
	if err := dispatch(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "burn: "+err.Error())
		os.Exit(1)
	}
}

// resolveCommand maps raw CLI args to a command name and the remaining args
// meant for that command's own flag set. Pure and side-effect free so it's
// unit-testable without exec'ing anything.
//
// A first argument that starts with "-" is treated as a `run` flag (back-compat
// for bare `burn --goal ...`), so it must NOT be confused with an unrecognized
// subcommand name.
func resolveCommand(args []string) (name string, rest []string) {
	if len(args) == 0 {
		return "run", args
	}
	switch args[0] {
	case "-h", "--help", "help":
		return "help", nil
	case "run", "setup":
		return args[0], args[1:]
	default:
		if strings.HasPrefix(args[0], "-") {
			return "run", args
		}
		return "unknown", args
	}
}

func dispatch(args []string) error {
	name, rest := resolveCommand(args)
	switch name {
	case "help":
		printUsage()
		return nil
	case "run":
		return cmdRun(rest)
	case "setup":
		return setup()
	default:
		printUsage()
		return fmt.Errorf("unknown command %q", args[0])
	}
}

// printUsage lists top-level subcommands, sorted for stable output.
func printUsage() {
	fmt.Println("burn <command> [flags]")
	fmt.Println()
	fmt.Println("Commands:")
	names := make([]string, 0, len(commandHelp))
	for n := range commandHelp {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Printf("  %-10s %s\n", n, commandHelp[n])
	}
}

// cmdRun implements the original `burn --goal ... [flags]` behavior — launch
// or watch sessions — as the `run` subcommand.
func cmdRun(args []string) error {
	home, _ := os.UserHomeDir()
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	cfg := Config{}
	fs.Float64Var(&cfg.Target, "target", 25, "stop/refuse once 5-hour utilization reaches this percent")
	fs.IntVar(&cfg.Jobs, "jobs", 1, "number of parallel sessions to launch")
	fs.StringVar(&cfg.Model, "model", "opus", "model for launched sessions (opus|sonnet|haiku|id)")
	fs.StringVar(&cfg.Goal, "goal", "", "the task each session works on (required for launch)")
	fs.BoolVar(&cfg.Watch, "watch", false, "governor mode: poll usage and notify at target, spawn nothing")
	fs.StringVar(&cfg.Workdir, "workdir", filepath.Join(os.TempDir(), "dont-burn-it-all-scratch"), "working dir for sessions (a scratch dir, NOT a real repo)")
	fs.StringVar(&cfg.Store, "store", filepath.Join(home, ".claude", "burn", "worker.jsonl"), "JSONL log path")
	fs.IntVar(&cfg.MaxTurns, "max-turns", 30, "max agent turns per session")
	fs.BoolVar(&cfg.DryRun, "dry-run", false, "print usage, auth, and planned jobs; spawn nothing")
	fs.Float64Var(&cfg.MaxUSDGuard, "max-usd-guard", 0, "abort if reported session cost exceeds this ($); 0 disables")
	fs.BoolVar(&cfg.AllowBillsAPI, "i-know-this-bills-api", false, "override the refusal when billing-risk env vars are set")
	fs.BoolVar(&cfg.SkipPermissions, "dangerously-skip-permissions", false, "run sessions unattended with --dangerously-skip-permissions (opt-in)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	uc, err := newUsageClient()
	if err != nil {
		return err
	}
	store, err := openStore(cfg.Store)
	if err != nil {
		return err
	}
	defer store.Close()

	switch {
	case cfg.DryRun:
		return dryRun(cfg, uc)
	case cfg.Watch:
		return watch(cfg, uc, store)
	default:
		return doLaunch(cfg, uc, store)
	}
}

func dryRun(cfg Config, uc *UsageClient) error {
	u, err := uc.Get()
	if err != nil {
		return err
	}
	fmt.Printf("auth token source : %s\n", uc.Source())
	if bad := checkHostileEnv(); len(bad) > 0 {
		fmt.Printf("billing-risk vars : %s  <-- would route to API billing!\n", strings.Join(bad, ", "))
	} else {
		fmt.Printf("billing-risk vars : none\n")
	}
	fmt.Printf("5-hour usage      : %.1f%% (resets %s)\n", u.FiveHour.Utilization, u.FiveHour.ResetsAt)
	fmt.Printf("7-day usage       : %.1f%%\n", u.SevenDay.Utilization)
	fmt.Printf("target            : %.1f%%\n", cfg.Target)
	fmt.Printf("planned           : %d session(s) of model %q in %s\n", cfg.Jobs, cfg.Model, cfg.Workdir)
	fmt.Printf("goal              : %q\n", cfg.Goal)
	return nil
}

func doLaunch(cfg Config, uc *UsageClient, store *Store) error {
	if strings.TrimSpace(cfg.Goal) == "" {
		return fmt.Errorf("--goal is required for a launch (or use --watch)")
	}
	u, err := uc.Get()
	if err != nil {
		return err
	}
	if u.FiveHour.Utilization >= cfg.Target {
		return fmt.Errorf("at/over target: 5-hour usage %.1f%% >= target %.1f%% — stop starting sessions",
			u.FiveHour.Utilization, cfg.Target)
	}
	if err := os.MkdirAll(cfg.Workdir, 0o755); err != nil {
		return err
	}

	if err := preflight(cfg, uc, uc.Token()); err != nil {
		return err
	}

	fmt.Printf("launching %d session(s) toward: %s\n", cfg.Jobs, cfg.Goal)
	res, err := launch(cfg, uc, uc.Token(), store)
	if err != nil {
		return err
	}

	after, aerr := uc.Get()
	fmt.Printf("done: %d session(s), %d error(s), reported cost $%.4f\n", res.sessions, res.errors, res.costUSD)
	if aerr == nil {
		fmt.Printf("5-hour usage now %.1f%% — %.1f%% headroom to target %.1f%%\n",
			after.FiveHour.Utilization, cfg.Target-after.FiveHour.Utilization, cfg.Target)
	}
	return nil
}

func watch(cfg Config, uc *UsageClient, store *Store) error {
	fmt.Printf("watching 5-hour usage; will notify at target %.1f%% (poll every %s)\n", cfg.Target, minPollInterval)
	first := true
	for {
		var u Usage
		var err error
		if first {
			u, err = uc.Get()
			first = false
		} else {
			u, err = uc.GetWaitingFresh()
		}
		if err != nil {
			return err
		}
		store.Write(Record{
			TS:             time.Now().UTC().Format(time.RFC3339),
			Kind:           "watch",
			FiveHourBefore: u.FiveHour.Utilization,
			SevenDay:       u.SevenDay.Utilization,
		})
		fmt.Printf("[%s] 5h %.1f%%  7d %.1f%%\n", time.Now().Format("15:04:05"), u.FiveHour.Utilization, u.SevenDay.Utilization)
		if u.FiveHour.Utilization >= cfg.Target {
			notify(fmt.Sprintf("5-hour usage %.1f%% reached target %.1f%% — stop starting sessions", u.FiveHour.Utilization, cfg.Target))
			return nil
		}
	}
}

// notify emits a terminal bell plus a best-effort macOS notification.
func notify(msg string) {
	fmt.Print("\a")
	fmt.Println("NOTICE: " + msg)
	if _, err := exec.LookPath("osascript"); err == nil {
		exec.Command("osascript", "-e", fmt.Sprintf("display notification %q with title \"dont-burn-it-all\"", msg)).Run()
	}
}

// --- preflight stamp: lets repeat manual launches skip the ~3m metering proof
// within the same usage window. ---

func stampPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "burn", ".preflight-ok")
}

func isPreflightFresh() bool {
	data, err := os.ReadFile(stampPath())
	if err != nil {
		return false
	}
	ts, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return false
	}
	return time.Since(time.Unix(ts, 0)) < 4*time.Hour
}

func markPreflightFresh() {
	p := stampPath()
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte(strconv.FormatInt(time.Now().Unix(), 10)), 0o644)
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./... -run TestResolveCommand -v`
Expected: PASS (all 8 subtests)

- [ ] **Step 5: Manual regression smoke test**

Run:
```sh
go build -o burn .
./burn setup
./burn run --dry-run --goal "smoke test"
./burn --dry-run --goal "smoke test"     # bare alias still works
./burn
```
Expected: `setup` prints its checklist; both dry-run forms print identical output; bare `./burn` (no args) prints the existing `--goal is required for a launch (or use --watch)` error, matching today's behavior exactly.

- [ ] **Step 6: Commit**

```bash
git add main.go main_test.go
git commit -m "feat: add subcommand dispatcher (run/setup)"
```

---

### Task 2: `StartedAt` tracking + `burn overview` (#18)

**Files:**
- Modify: `store.go` (add `StartedAt` to `Record`)
- Modify: `runner.go` (stamp `StartedAt` in `launch()`)
- Modify: `main.go` (wire the `overview` subcommand)
- Modify: `main_test.go` (add `overview` cases to `TestResolveCommand`)
- Create: `overview.go`
- Create: `overview_test.go`

**Interfaces:**
- Consumes: `Record` (store.go, extended), `resolveCommand`/`dispatch`/`commandHelp` (main.go, from Task 1).
- Produces: `GoalSummary{Goal string; Sessions int; CostUSD float64; Turns int; Errors int; DurationSeconds float64; PartialDuration bool; FirstRun string; LastRun string}`, `Overview{Goals []GoalSummary; Total GoalSummary}`, `aggregateByGoal(records []Record) Overview`, `loadRecords(path string) ([]Record, error)`, `cmdOverview(args []string) error`.

- [ ] **Step 1: Write the failing tests**

Create `overview_test.go`:

```go
package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAggregateByGoal(t *testing.T) {
	records := []Record{
		{Kind: "session", Goal: "write tests", TS: "2026-08-18T10:00:30Z", StartedAt: "2026-08-18T10:00:00Z", CostUSD: 0.10, NumTurns: 5},
		{Kind: "session", Goal: "write tests", TS: "2026-08-18T10:05:20Z", StartedAt: "2026-08-18T10:05:00Z", CostUSD: 0.20, NumTurns: 8, IsError: true},
		{Kind: "session", Goal: "refactor pkg", TS: "2026-08-18T11:00:10Z", StartedAt: "2026-08-18T11:00:00Z", CostUSD: 0.05, NumTurns: 2},
		{Kind: "session", Goal: "refactor pkg", TS: "2026-08-18T12:00:00Z", CostUSD: 0.05, NumTurns: 3}, // pre-StartedAt record
		{Kind: "watch", FiveHourBefore: 42}, // must be ignored
	}

	ov := aggregateByGoal(records)

	if len(ov.Goals) != 2 {
		t.Fatalf("expected 2 goal groups, got %d: %+v", len(ov.Goals), ov.Goals)
	}

	// sorted alphabetically: "refactor pkg" before "write tests"
	refactor := ov.Goals[0]
	if refactor.Goal != "refactor pkg" {
		t.Fatalf("expected first group 'refactor pkg', got %q", refactor.Goal)
	}
	if refactor.Sessions != 2 || refactor.Errors != 0 {
		t.Fatalf("refactor pkg: got sessions=%d errors=%d", refactor.Sessions, refactor.Errors)
	}
	if !refactor.PartialDuration {
		t.Fatal("refactor pkg: expected PartialDuration=true (one record has no StartedAt)")
	}
	if refactor.DurationSeconds != 10 {
		t.Fatalf("refactor pkg: expected 10s duration from the one timed record, got %v", refactor.DurationSeconds)
	}

	write := ov.Goals[1]
	if write.Goal != "write tests" {
		t.Fatalf("expected second group 'write tests', got %q", write.Goal)
	}
	if write.Sessions != 2 || write.Errors != 1 {
		t.Fatalf("write tests: got sessions=%d errors=%d", write.Sessions, write.Errors)
	}
	if write.PartialDuration {
		t.Fatal("write tests: expected PartialDuration=false, both records have StartedAt")
	}
	if write.DurationSeconds != 50 {
		t.Fatalf("write tests: expected 30s+20s=50s duration, got %v", write.DurationSeconds)
	}
	if diff := write.CostUSD - 0.30; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("write tests: expected cost 0.30, got %v", write.CostUSD)
	}

	if ov.Total.Sessions != 4 {
		t.Fatalf("total: expected 4 sessions, got %d", ov.Total.Sessions)
	}
	if ov.Total.Errors != 1 {
		t.Fatalf("total: expected 1 error, got %d", ov.Total.Errors)
	}
	if !ov.Total.PartialDuration {
		t.Fatal("total: expected PartialDuration=true (propagated from refactor pkg)")
	}
}

func TestAggregateByGoalEmpty(t *testing.T) {
	ov := aggregateByGoal(nil)
	if len(ov.Goals) != 0 {
		t.Fatalf("expected 0 goal groups, got %d", len(ov.Goals))
	}
	if ov.Total.Sessions != 0 {
		t.Fatalf("expected 0 total sessions, got %d", ov.Total.Sessions)
	}
}

func TestLoadRecordsMissingFile(t *testing.T) {
	records, err := loadRecords("/nonexistent/path/worker.jsonl")
	if err != nil {
		t.Fatalf("expected no error for a missing store file, got %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("expected no records, got %d", len(records))
	}
}

func TestLoadRecordsSkipsMalformedLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker.jsonl")
	content := "{\"kind\":\"session\",\"goal\":\"a\",\"ts\":\"2026-08-18T10:00:00Z\"}\n" +
		"not valid json\n" +
		"{\"kind\":\"session\",\"goal\":\"b\",\"ts\":\"2026-08-18T11:00:00Z\"}\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	records, err := loadRecords(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("expected 2 valid records (malformed line skipped), got %d", len(records))
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./... -run 'TestAggregateByGoal|TestLoadRecords' -v`
Expected: build failure — `undefined: aggregateByGoal` / `undefined: loadRecords` / `unknown field StartedAt in struct literal`

- [ ] **Step 3: Add `StartedAt` to `Record`**

In `store.go`, modify the `Record` struct:

```go
// Record is one line in the JSONL log — either a finished session ("session")
// or a governor reading ("watch").
type Record struct {
	TS             string  `json:"ts"`
	Kind           string  `json:"kind"`
	SessionID      string  `json:"session_id,omitempty"`
	Goal           string  `json:"goal,omitempty"`
	StartedAt      string  `json:"started_at,omitempty"`
	Model          string  `json:"model,omitempty"`
	CostUSD        float64 `json:"cost_usd,omitempty"`
	NumTurns       int     `json:"num_turns,omitempty"`
	IsError        bool    `json:"is_error,omitempty"`
	FiveHourBefore float64 `json:"five_hour_before,omitempty"`
	FiveHourAfter  float64 `json:"five_hour_after,omitempty"`
	SevenDay       float64 `json:"seven_day,omitempty"`
}
```

(Only the `StartedAt` line is new; `omitempty` means old JSONL lines with no `started_at` key still decode fine, as `""`.)

- [ ] **Step 4: Stamp `StartedAt` in `runner.go`**

In `runner.go`, inside `launch()`, modify the per-job goroutine to capture the start time and pass it through:

```go
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()

			start := time.Now().UTC()
			// Nonce only defeats identical-prompt server caching; the work is real.
			prompt := fmt.Sprintf("%s (nonce=%d)", cfg.Goal, i)
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
			})
			if runErr != nil {
				fmt.Fprintf(os.Stderr, "job %d error: %v\n", i, runErr)
			}
		}(i)
```

(Only the `start := time.Now().UTC()` line and the `StartedAt: start.Format(time.RFC3339),` field are new.)

- [ ] **Step 5: Create `overview.go`**

```go
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"
)

// GoalSummary aggregates all "session" records sharing one Goal (or, for
// Overview.Total, across all goals).
type GoalSummary struct {
	Goal            string  `json:"goal"`
	Sessions        int     `json:"sessions"`
	CostUSD         float64 `json:"cost_usd"`
	Turns           int     `json:"turns"`
	Errors          int     `json:"errors"`
	DurationSeconds float64 `json:"duration_seconds"`
	PartialDuration bool    `json:"partial_duration"`
	FirstRun        string  `json:"first_run"`
	LastRun         string  `json:"last_run"`
}

// Overview is the full `burn overview` result: one GoalSummary per distinct
// goal (sorted alphabetically), plus a Total row across all of them.
type Overview struct {
	Goals []GoalSummary `json:"goals"`
	Total GoalSummary   `json:"total"`
}

type goalAcc struct {
	sessions, turns, errors int
	cost, durationSec       float64
	partial                 bool
	first, last             time.Time
}

// aggregateByGoal groups "session"-kind records by Goal and computes
// per-goal totals, sorted alphabetically by goal, plus an overall Total row.
// "watch"-kind records are ignored — they track usage polling, not sessions.
// Sessions written before StartedAt existed count 0 duration and mark
// PartialDuration so the output doesn't silently under-report.
func aggregateByGoal(records []Record) Overview {
	byGoal := map[string]*goalAcc{}
	var order []string

	for _, r := range records {
		if r.Kind != "session" {
			continue
		}
		a, ok := byGoal[r.Goal]
		if !ok {
			a = &goalAcc{}
			byGoal[r.Goal] = a
			order = append(order, r.Goal)
		}
		a.sessions++
		a.cost += r.CostUSD
		a.turns += r.NumTurns
		if r.IsError {
			a.errors++
		}

		ts, tsErr := time.Parse(time.RFC3339, r.TS)
		if tsErr == nil {
			if a.first.IsZero() || ts.Before(a.first) {
				a.first = ts
			}
			if ts.After(a.last) {
				a.last = ts
			}
		}

		if r.StartedAt == "" {
			a.partial = true
			continue
		}
		started, err := time.Parse(time.RFC3339, r.StartedAt)
		if err != nil || tsErr != nil {
			a.partial = true
			continue
		}
		a.durationSec += ts.Sub(started).Seconds()
	}

	sort.Strings(order)

	ov := Overview{Goals: make([]GoalSummary, 0, len(order))}
	total := &goalAcc{}
	for _, goal := range order {
		a := byGoal[goal]
		ov.Goals = append(ov.Goals, toSummary(goal, a))

		total.sessions += a.sessions
		total.turns += a.turns
		total.errors += a.errors
		total.cost += a.cost
		total.durationSec += a.durationSec
		total.partial = total.partial || a.partial
		if total.first.IsZero() || (!a.first.IsZero() && a.first.Before(total.first)) {
			total.first = a.first
		}
		if a.last.After(total.last) {
			total.last = a.last
		}
	}
	ov.Total = toSummary("TOTAL", total)
	return ov
}

func toSummary(goal string, a *goalAcc) GoalSummary {
	return GoalSummary{
		Goal:            goal,
		Sessions:        a.sessions,
		CostUSD:         a.cost,
		Turns:           a.turns,
		Errors:          a.errors,
		DurationSeconds: a.durationSec,
		PartialDuration: a.partial,
		FirstRun:        formatTimeOrEmpty(a.first),
		LastRun:         formatTimeOrEmpty(a.last),
	}
}

func formatTimeOrEmpty(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

// loadRecords reads a JSONL store, skipping (and warning to stderr about)
// malformed lines rather than aborting the whole read. A missing file is not
// an error — it means burn has never run yet.
func loadRecords(path string) ([]Record, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var records []Record
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := sc.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		var r Record
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			fmt.Fprintf(os.Stderr, "burn: skipping malformed line %d in %s: %v\n", lineNo, path, err)
			continue
		}
		records = append(records, r)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return records, nil
}

// cmdOverview implements `burn overview`.
func cmdOverview(args []string) error {
	home, _ := os.UserHomeDir()
	fs := flag.NewFlagSet("overview", flag.ExitOnError)
	var storePath string
	var asJSON bool
	fs.StringVar(&storePath, "store", filepath.Join(home, ".claude", "burn", "worker.jsonl"), "JSONL log path")
	fs.BoolVar(&asJSON, "json", false, "emit JSON instead of a table")
	if err := fs.Parse(args); err != nil {
		return err
	}

	records, err := loadRecords(storePath)
	if err != nil {
		return fmt.Errorf("reading store %s: %w", storePath, err)
	}

	ov := aggregateByGoal(records)
	if len(ov.Goals) == 0 {
		fmt.Println("no burn activity recorded yet")
		return nil
	}

	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(ov)
	}
	printOverviewTable(ov)
	return nil
}

func printOverviewTable(ov Overview) {
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "GOAL\tSESSIONS\tCOST\tTURNS\tERRORS\tTIME SPENT\tFIRST RUN\tLAST RUN")
	for _, g := range ov.Goals {
		fmt.Fprintln(w, formatGoalRow(g))
	}
	fmt.Fprintln(w, formatGoalRow(ov.Total))
	w.Flush()
}

func formatGoalRow(g GoalSummary) string {
	goal := g.Goal
	if len(goal) > 48 {
		goal = goal[:45] + "..."
	}
	dur := time.Duration(g.DurationSeconds * float64(time.Second)).Round(time.Second).String()
	if g.PartialDuration {
		dur += " (partial)"
	}
	return fmt.Sprintf("%s\t%d\t$%.4f\t%d\t%d\t%s\t%s\t%s",
		goal, g.Sessions, g.CostUSD, g.Turns, g.Errors, dur, g.FirstRun, g.LastRun)
}
```

- [ ] **Step 6: Wire `overview` into `main.go`**

In `main.go`, update `commandHelp`:

```go
var commandHelp = map[string]string{
	"run":      "launch or watch sessions against the subscription 5-hour quota",
	"overview": "summarize past burn activity from the JSONL store",
	"setup":    "check burn's configuration (claude, token, endpoint, dirs)",
}
```

Update `resolveCommand`'s switch:

```go
	case "run", "setup", "overview":
		return args[0], args[1:]
```

Update `dispatch`'s switch:

```go
	case "overview":
		return cmdOverview(rest)
```

- [ ] **Step 7: Extend `main_test.go`**

Add two cases to the `cases` slice in `TestResolveCommand` (in `main_test.go`):

```go
		{"overview bare", []string{"overview"}, "overview", []string{}},
		{"overview with flags", []string{"overview", "--json"}, "overview", []string{"--json"}},
```

- [ ] **Step 8: Run the full test suite**

Run: `go test ./... -v`
Expected: PASS — all `TestResolveCommand` subtests (including the two new ones), `TestAggregateByGoal`, `TestAggregateByGoalEmpty`, `TestLoadRecordsMissingFile`, `TestLoadRecordsSkipsMalformedLines`, plus the pre-existing `TestChildEnvScrubsBillingRiskAndPinsToken` and `TestCredsBlobParse`.

- [ ] **Step 9: Manual smoke test**

Run:
```sh
go build -o burn .
./burn overview                                  # no store yet -> "no burn activity recorded yet"
mkdir -p /tmp/burn-smoke
cat > /tmp/burn-smoke/worker.jsonl <<'EOF'
{"ts":"2026-08-18T10:00:30Z","kind":"session","goal":"smoke test","started_at":"2026-08-18T10:00:00Z","cost_usd":0.05,"num_turns":3}
{"ts":"2026-08-18T10:05:10Z","kind":"session","goal":"smoke test","started_at":"2026-08-18T10:05:00Z","cost_usd":0.07,"num_turns":4}
EOF
./burn overview --store /tmp/burn-smoke/worker.jsonl
./burn overview --store /tmp/burn-smoke/worker.jsonl --json
rm -rf /tmp/burn-smoke
```
Expected: first `overview` call (no store) prints `no burn activity recorded yet`; the table run shows one `smoke test` row (2 sessions, $0.12, 7 turns, 0 errors, 40s) plus a matching TOTAL row; the `--json` run prints the equivalent JSON.

- [ ] **Step 10: Commit**

```bash
git add store.go runner.go main.go main_test.go overview.go overview_test.go
git commit -m "feat: add burn overview command"
```

---

### Task 3: `burn connect jira` (#19)

**Files:**
- Modify: `main.go` (wire the `connect` subcommand)
- Modify: `main_test.go` (add `connect` cases)
- Create: `connect.go`
- Create: `jira.go`
- Create: `jira_test.go`

**Interfaces:**
- Consumes: nothing new beyond stdlib.
- Produces: `WorkItem{Key, Summary string}`, `Source interface{ ListItems(query string) ([]WorkItem, error) }`, `sources map[string]Source`, `jiraSource struct{}`, `parseAcliSearchOutput(data []byte) ([]WorkItem, error)`, `cmdConnect(args []string) error`.

- [ ] **Step 1: Write the failing tests**

Create `jira_test.go`. The sample JSON below is trimmed from a real `acli jira workitem search --json` response (verified interactively — top-level `key`, `fields.summary`):

```go
package main

import "testing"

func TestParseAcliSearchOutput(t *testing.T) {
	data := []byte(`[
  {
    "id": "48004",
    "key": "SUDS-1496",
    "fields": {
      "summary": "Exclude Aikido scanner traffic from GuardDuty findings via Trusted IP set",
      "status": {"name": "In Progress"}
    }
  },
  {
    "id": "48005",
    "key": "SUDS-1497",
    "fields": {
      "summary": "Rotate the shared CI deploy token"
    }
  }
]`)

	items, err := parseAcliSearchOutput(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	if items[0].Key != "SUDS-1496" || items[0].Summary != "Exclude Aikido scanner traffic from GuardDuty findings via Trusted IP set" {
		t.Fatalf("unexpected first item: %+v", items[0])
	}
	if items[1].Key != "SUDS-1497" || items[1].Summary != "Rotate the shared CI deploy token" {
		t.Fatalf("unexpected second item: %+v", items[1])
	}
}

func TestParseAcliSearchOutputEmpty(t *testing.T) {
	items, err := parseAcliSearchOutput([]byte(`[]`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected 0 items, got %d", len(items))
	}
}

func TestParseAcliSearchOutputInvalid(t *testing.T) {
	if _, err := parseAcliSearchOutput([]byte(`not json`)); err == nil {
		t.Fatal("expected an error for invalid JSON")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./... -run TestParseAcliSearchOutput -v`
Expected: build failure — `undefined: parseAcliSearchOutput`

- [ ] **Step 3: Create `connect.go`**

```go
package main

import (
	"flag"
	"fmt"
)

// WorkItem is one item returned by a connected data source.
type WorkItem struct {
	Key     string
	Summary string
}

// Source lists work items matching a query string (source-specific syntax,
// e.g. JQL for Jira). One interface, one implementation today — sized so a
// second source is "implement this interface", not a rewrite.
type Source interface {
	ListItems(query string) ([]WorkItem, error)
}

// sources maps a `burn connect <name>` argument to its Source implementation.
var sources = map[string]Source{
	"jira": jiraSource{},
}

// cmdConnect implements `burn connect <source> --jql "..."`.
func cmdConnect(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: burn connect <source> [flags] (available: jira)")
	}
	name := args[0]
	src, ok := sources[name]
	if !ok {
		return fmt.Errorf("unknown source %q (available: jira)", name)
	}

	fs := flag.NewFlagSet("connect "+name, flag.ExitOnError)
	var jql string
	fs.StringVar(&jql, "jql", "", "JQL query to list matching issues (required)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if jql == "" {
		return fmt.Errorf("--jql is required")
	}

	items, err := src.ListItems(jql)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		fmt.Println("no matching issues")
		return nil
	}
	for _, it := range items {
		fmt.Printf("%s\t%s\n", it.Key, it.Summary)
	}
	return nil
}
```

- [ ] **Step 4: Create `jira.go`**

```go
package main

import (
	"encoding/json"
	"fmt"
	"os/exec"
)

// jiraSource lists Jira issues via the `acli` CLI (Atlassian CLI), reusing
// whatever auth acli already has configured rather than burn managing its own
// Jira credentials.
type jiraSource struct{}

// acliIssue is the subset of `acli jira workitem search --json` output we
// use — verified against a real `acli jira workitem search --json` response.
type acliIssue struct {
	Key    string `json:"key"`
	Fields struct {
		Summary string `json:"summary"`
	} `json:"fields"`
}

func (jiraSource) ListItems(jql string) ([]WorkItem, error) {
	if _, err := exec.LookPath("acli"); err != nil {
		return nil, fmt.Errorf("`acli` not on PATH — install the Atlassian CLI and run `acli jira auth login` first")
	}
	out, err := exec.Command("acli", "jira", "workitem", "search",
		"--jql", jql, "--fields", "summary", "--json").Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("acli jira workitem search failed: %s", ee.Stderr)
		}
		return nil, fmt.Errorf("acli jira workitem search failed: %w", err)
	}
	return parseAcliSearchOutput(out)
}

// parseAcliSearchOutput parses `acli jira workitem search --json` output into
// WorkItems. Kept separate from ListItems so it's unit-testable without
// shelling out to acli.
func parseAcliSearchOutput(data []byte) ([]WorkItem, error) {
	var issues []acliIssue
	if err := json.Unmarshal(data, &issues); err != nil {
		return nil, fmt.Errorf("parsing acli output: %w", err)
	}
	items := make([]WorkItem, 0, len(issues))
	for _, is := range issues {
		items = append(items, WorkItem{Key: is.Key, Summary: is.Fields.Summary})
	}
	return items, nil
}
```

- [ ] **Step 5: Wire `connect` into `main.go`**

Update `commandHelp`:

```go
var commandHelp = map[string]string{
	"run":      "launch or watch sessions against the subscription 5-hour quota",
	"overview": "summarize past burn activity from the JSONL store",
	"connect":  "verify/query an external data source (e.g. jira)",
	"setup":    "check burn's configuration (claude, token, endpoint, dirs)",
}
```

Update `resolveCommand`'s switch:

```go
	case "run", "setup", "overview", "connect":
		return args[0], args[1:]
```

Update `dispatch`'s switch:

```go
	case "connect":
		return cmdConnect(rest)
```

- [ ] **Step 6: Extend `main_test.go`**

Add two cases to the `cases` slice in `TestResolveCommand`:

```go
		{"connect jira", []string{"connect", "jira", "--jql", "q"}, "connect", []string{"jira", "--jql", "q"}},
		{"connect bare", []string{"connect"}, "connect", []string{}},
```

- [ ] **Step 7: Run the full test suite**

Run: `go test ./... -v`
Expected: PASS — all prior tests plus `TestParseAcliSearchOutput`, `TestParseAcliSearchOutputEmpty`, `TestParseAcliSearchOutputInvalid`, and the two new `TestResolveCommand` subtests.

- [ ] **Step 8: Manual smoke test**

Requires `acli` installed and authenticated (`acli jira auth status`). Run:
```sh
go build -o burn .
./burn connect bogus --jql "x"                       # expect: unknown source "bogus" ...
./burn connect jira                                   # expect: --jql is required
./burn connect jira --jql "project = SUDS AND summary ~ \"deploy\""
```
Expected: the first two calls error as described; the third lists matching `SUDS-*` issues as `KEY<tab>summary` lines (or `no matching issues` if none match).

- [ ] **Step 9: Commit**

```bash
git add main.go main_test.go connect.go jira.go jira_test.go
git commit -m "feat: add burn connect jira command"
```

---

### Task 4: Update README for the new subcommands

**Files:**
- Modify: `README.md`

**Interfaces:** none (documentation only).

- [ ] **Step 1: Update the Quick start block**

Replace:

```markdown
## Quick start

```sh
burn setup                                    # verify config (claude, token, endpoint, dirs)
burn --dry-run --goal test                    # print current 5h usage + planned jobs; run nothing
burn --goal "write tests for pkg/foo" --jobs 4 --target 80   # spend quota up to 80%
burn --watch --target 80                       # monitor only: notify when 5h usage hits 80%
```

Each `--goal` launch runs `--jobs` sessions to completion and stops — you start each launch.
Pick a `--target` above your current usage, or the launch refuses by design.
```

With:

```markdown
## Quick start

```sh
burn setup                                    # verify config (claude, token, endpoint, dirs)
burn run --dry-run --goal test                # print current 5h usage + planned jobs; run nothing
burn run --goal "write tests for pkg/foo" --jobs 4 --target 80   # spend quota up to 80%
burn run --watch --target 80                   # monitor only: notify when 5h usage hits 80%
burn overview                                  # summarize past sessions: cost, turns, errors, time spent
burn connect jira --jql 'project = SUDS AND status = "To Refine"'   # list matching Jira issues
```

`burn <command>` dispatches to a subcommand (`run`, `overview`, `connect`, `setup`); bare
`burn --goal ...` / `burn --dry-run ...` (no subcommand keyword) remain aliases for `burn run ...`.

Each `--goal` launch runs `--jobs` sessions to completion and stops — you start each launch.
Pick a `--target` above your current usage, or the launch refuses by design.
```

- [ ] **Step 2: Retitle the flags table to `burn run` flags**

Replace:

```markdown
## Flags
```

With:

```markdown
## `burn run` flags
```

- [ ] **Step 3: Add `burn overview` and `burn connect` sections**

Immediately after the flags table (before `## Safety`), insert:

```markdown
## `burn overview`

Summarizes the JSONL activity store (`--store`, same default as `run`) grouped by goal: session
count, total cost, turns, errors, time spent, and first/last run timestamps. Add `--json` for
scripting.

## `burn connect`

Verifies and queries an external data source. Today: `burn connect jira --jql "<JQL>"`, which
shells out to [`acli`](https://developer.atlassian.com/cloud/acli/) (install it and run `acli
jira auth login` first) and prints matching issues as `KEY<tab>summary` lines.
```

- [ ] **Step 4: Verify the README renders sensibly**

Run: `cat README.md` and read through it top to bottom — confirm no broken markdown (unclosed code fences), no duplicate headings, and the new sections read in a sensible order (Quick start → Flags → overview → connect → Safety → ...).

- [ ] **Step 5: Commit**

```bash
git add README.md
git commit -m "docs: document burn overview and burn connect"
```

---

## Post-plan (not part of this plan)

- #20 (`burn process`) builds on `Source`/`WorkItem` from Task 3 — out of scope here.
- Opening a PR for `feat/burn-cli-overview-connect` once all four tasks are committed and `just vet test` is clean.
