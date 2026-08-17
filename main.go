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
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Config holds all `run` subcommand options.
type Config struct {
	Target          float64
	WeeklyTarget    float64
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

	// Sandbox is an opt-in extra (like a Python package extra): none of this is
	// checked or required unless the flag is set. See sandbox.go.
	Sandbox      bool
	SandboxImage string
	Repo         string
	GHTokenEnv   string
}

// commandHelp describes each top-level subcommand for printUsage. Later tasks
// add entries here as they add subcommands.
var commandHelp = map[string]string{
	"run":      "launch or watch sessions against the subscription 5-hour quota",
	"overview": "summarize past burn activity from the JSONL store",
	"connect":  "verify/query an external data source (e.g. jira)",
	"setup":    "check burn's configuration (claude, token, endpoint, dirs)",
}

func main() {
	installSandboxSignalHandler()
	if err := dispatch(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "burn: "+err.Error())
		os.Exit(1)
	}
}

// installSandboxSignalHandler ensures an interrupted --sandbox run doesn't
// leave a container mounted read-write against the real repo. Go does not run
// deferred functions (like runClaude's cleanup) on SIGINT/SIGTERM, so this
// catches them explicitly and force-kills every sandbox this process created,
// including one still being created when the signal arrives (see
// shutdownSandboxes in sandbox.go). A no-op if --sandbox was never used.
func installSandboxSignalHandler() {
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		shutdownSandboxes()
		os.Exit(1)
	}()
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
	case "run", "setup", "overview", "connect":
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
		return cmdSetup(rest)
	case "overview":
		return cmdOverview(rest)
	case "connect":
		return cmdConnect(rest)
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
	fs.Float64Var(&cfg.WeeklyTarget, "weekly-target", 0, "stop/refuse once 7-day utilization reaches this percent; 0 disables")
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
	fs.BoolVar(&cfg.Sandbox, "sandbox", false, "run sessions in a local OpenSandbox (Docker) instead of on the host — opt-in extra, see 'burn setup'")
	fs.StringVar(&cfg.SandboxImage, "sandbox-image", "burn-sandbox:latest", "image to use for --sandbox sessions")
	fs.StringVar(&cfg.Repo, "repo", "", "local repo to mount read-write into the sandbox (--sandbox only; defaults to --workdir)")
	fs.StringVar(&cfg.GHTokenEnv, "gh-token-env", "GH_TOKEN", "env var holding a GitHub token to forward into the sandbox for PR creation (falls back to 'gh auth token')")
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

// cmdSetup implements `burn setup`. --sandbox-image is parsed here (rather than
// only on `run`) so `burn setup --sandbox-image foo` checks the image a real
// `burn run --sandbox --sandbox-image foo` would actually use.
func cmdSetup(args []string) error {
	fs := flag.NewFlagSet("setup", flag.ExitOnError)
	var sandboxImage string
	fs.StringVar(&sandboxImage, "sandbox-image", "burn-sandbox:latest", "image to check for --sandbox (see 'burn run --sandbox')")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return setup(sandboxImage)
}

// breachMessage returns a non-empty reason once usage is at/over a threshold, checking the
// 5-hour window first, then the 7-day window (weeklyTarget <= 0 disables that check).
func breachMessage(u Usage, target, weeklyTarget float64) string {
	if u.FiveHour.Utilization >= target {
		return fmt.Sprintf("5-hour usage %.1f%% >= target %.1f%%", u.FiveHour.Utilization, target)
	}
	if weeklyTarget > 0 && u.SevenDay.Utilization >= weeklyTarget {
		return fmt.Sprintf("7-day usage %.1f%% >= weekly-target %.1f%%", u.SevenDay.Utilization, weeklyTarget)
	}
	return ""
}

func dryRun(cfg Config, uc *UsageClient) error {
	// Run the same validation a real launch would hit (--jobs>1 refusal, repo
	// resolution) so dry-run actually previews what doLaunch will do.
	target := cfg.Workdir
	if cfg.Sandbox {
		if err := validateSandboxConfig(&cfg); err != nil {
			return err
		}
		target = cfg.Repo + " (sandboxed, mounted read-write at /workspace)"
	}
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
	if cfg.WeeklyTarget > 0 {
		fmt.Printf("weekly target     : %.1f%%\n", cfg.WeeklyTarget)
	} else {
		fmt.Printf("weekly target     : %.1f%% (disabled)\n", cfg.WeeklyTarget)
	}
	fmt.Printf("planned           : %d session(s) of model %q in %s\n", cfg.Jobs, cfg.Model, target)
	fmt.Printf("goal              : %q\n", cfg.Goal)
	return nil
}

func doLaunch(cfg Config, uc *UsageClient, store *Store) error {
	if strings.TrimSpace(cfg.Goal) == "" {
		return fmt.Errorf("--goal is required for a launch (or use --watch)")
	}
	if cfg.Sandbox {
		if err := validateSandboxConfig(&cfg); err != nil {
			return err
		}
	}
	u, err := uc.Get()
	if err != nil {
		return err
	}
	if reason := breachMessage(u, cfg.Target, cfg.WeeklyTarget); reason != "" {
		return fmt.Errorf("at/over threshold: %s — stop starting sessions", reason)
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
	weeklySuffix := ""
	if cfg.WeeklyTarget > 0 {
		weeklySuffix = fmt.Sprintf(", weekly-target %.1f%%", cfg.WeeklyTarget)
	}
	fmt.Printf("watching 5-hour usage; will notify at target %.1f%%%s (poll every %s)\n", cfg.Target, weeklySuffix, minPollInterval)
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
		if reason := breachMessage(u, cfg.Target, cfg.WeeklyTarget); reason != "" {
			notify(reason + " — stop starting sessions")
			return nil
		}
	}
}

// notify emits a terminal bell, a best-effort macOS notification, and, when
// BURN_NOTIFY_CMD is set, runs it via `sh -c` with the message in BURN_MSG so
// the alert can be forwarded anywhere (webhook, Slack, another agent's inbox).
func notify(msg string) {
	fmt.Print("\a")
	fmt.Println("NOTICE: " + msg)
	if _, err := exec.LookPath("osascript"); err == nil {
		exec.Command("osascript", "-e", fmt.Sprintf("display notification %q with title \"dont-burn-it-all\"", msg)).Run()
	}
	if c := os.Getenv("BURN_NOTIFY_CMD"); c != "" {
		cmd := exec.Command("sh", "-c", c)
		cmd.Env = append(os.Environ(), "BURN_MSG="+msg)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Fprintln(os.Stderr, "burn: BURN_NOTIFY_CMD failed: "+err.Error())
		}
	}
}

// --- preflight stamp: lets repeat manual launches skip the ~3m metering proof
// within the same usage window. ---

// stampPath is keyed by sandbox vs host so a metering proof from one mode
// never skips the probe for the other — a host-mode stamp proves nothing about
// whether the sandboxed claude bills to the subscription, and vice versa.
func stampPath(sandbox bool) string {
	home, _ := os.UserHomeDir()
	name := ".preflight-ok"
	if sandbox {
		name = ".preflight-ok-sandbox"
	}
	return filepath.Join(home, ".claude", "burn", name)
}

func isPreflightFresh(sandbox bool) bool {
	data, err := os.ReadFile(stampPath(sandbox))
	if err != nil {
		return false
	}
	ts, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return false
	}
	return time.Since(time.Unix(ts, 0)) < 4*time.Hour
}

func markPreflightFresh(sandbox bool) {
	p := stampPath(sandbox)
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte(strconv.FormatInt(time.Now().Unix(), 10)), 0o644)
}
