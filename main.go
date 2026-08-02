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
	"strconv"
	"strings"
	"time"
)

// Config holds all run options.
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

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "burn: "+err.Error())
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) > 1 && os.Args[1] == "setup" {
		return setup()
	}

	home, _ := os.UserHomeDir()
	cfg := Config{}
	flag.Float64Var(&cfg.Target, "target", 25, "stop/refuse once 5-hour utilization reaches this percent")
	flag.IntVar(&cfg.Jobs, "jobs", 1, "number of parallel sessions to launch")
	flag.StringVar(&cfg.Model, "model", "opus", "model for launched sessions (opus|sonnet|haiku|id)")
	flag.StringVar(&cfg.Goal, "goal", "", "the task each session works on (required for launch)")
	flag.BoolVar(&cfg.Watch, "watch", false, "governor mode: poll usage and notify at target, spawn nothing")
	flag.StringVar(&cfg.Workdir, "workdir", filepath.Join(os.TempDir(), "dont-burn-it-all-scratch"), "working dir for sessions (a scratch dir, NOT a real repo)")
	flag.StringVar(&cfg.Store, "store", filepath.Join(home, ".claude", "burn", "worker.jsonl"), "JSONL log path")
	flag.IntVar(&cfg.MaxTurns, "max-turns", 30, "max agent turns per session")
	flag.BoolVar(&cfg.DryRun, "dry-run", false, "print usage, auth, and planned jobs; spawn nothing")
	flag.Float64Var(&cfg.MaxUSDGuard, "max-usd-guard", 0, "abort if reported session cost exceeds this ($); 0 disables")
	flag.BoolVar(&cfg.AllowBillsAPI, "i-know-this-bills-api", false, "override the refusal when billing-risk env vars are set")
	flag.BoolVar(&cfg.SkipPermissions, "dangerously-skip-permissions", false, "run sessions unattended with --dangerously-skip-permissions (opt-in)")
	flag.Parse()

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
