package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"text/template"
	"time"
)

// Modes for `burn process --mode`.
const (
	modeEnrich    = "enrich"
	modeImplement = "implement"
	modeAuto      = "auto"
)

// earlyStatuses are statuses meaning "not ready to build yet", so --mode auto
// refines the ticket instead of attempting an implementation. Compared
// lower-cased and trimmed; an unknown status is treated as ready.
var earlyStatuses = map[string]bool{
	"":             true,
	"new":          true,
	"open":         true,
	"backlog":      true,
	"triage":       true,
	"to refine":    true,
	"refinement":   true,
	"to do":        true,
	"todo":         true,
	"needs triage": true,
}

// processConfig is `burn process`'s own options; Run carries the whole shared
// `burn run` flag set (targets, jobs, model, sandbox, ...).
type processConfig struct {
	Run    Config
	Source string
	Query  ItemQuery
	Mode   string

	MaxItems     int
	MaxErrors    int
	MaxRuntime   time.Duration
	StopFile     string
	Redo         bool
	TemplatePath string
	DigestPath   string
	NotifyEach   bool
}

// promptData is what a goal template (built-in or --prompt-template) renders
// against.
type promptData struct {
	Key     string
	Summary string
	Status  string
	Labels  []string
	Source  string
	Mode    string
	Repo    string
}

// enrichPrompt refines a ticket and writes back exactly one comment. It never
// touches a repo, which is why enrich is the only mode allowed to run with
// --jobs > 1.
const enrichPrompt = `You are refining {{.Source}} work item {{.Key}}: {{.Summary}} (status: {{.Status}}).

Read the full item first: run "acli jira workitem view {{.Key}}".

Produce a refinement that makes the item ready to pick up:
- a one-paragraph problem statement in the team's own terms
- explicit acceptance criteria as a checklist
- the components, services, or files likely affected
- open questions that block implementation, each addressed to a role

Then post it as a SINGLE comment on {{.Key}} using acli.

Constraints:
- Do NOT edit the description, status, assignee, labels, or any other field.
- Do NOT create, modify, or delete any other work item.
- If the item is already well specified, post a short comment saying so and
  listing only what you would still clarify. Do not pad it.

Finish your reply with one line: SUMMARY: <what you added, in under 20 words>.`

// implementPrompt attempts a scoped change and opens a draft PR. Every
// guardrail lives in the prompt text because the session, not burn, holds the
// tools that could do damage.
const implementPrompt = `You are implementing {{.Source}} work item {{.Key}}: {{.Summary}} (status: {{.Status}}).

The repository is at {{.Repo}} and is your working directory.

Steps:
1. Read the full item: run "acli jira workitem view {{.Key}}".
2. Judge whether it is scoped well enough to implement without guessing. If it
   is NOT, stop: post a comment on {{.Key}} with acli explaining exactly what
   is missing, and finish with SKIPPED: <reason>. Do not write any code.
3. If it is, create a branch named claude/{{.Key}}-<short-slug>, make the
   smallest change that satisfies the item, and add or update tests.
4. Run the repository's own checks (its lint, vet, build, and test commands) and
   get them passing before you commit.
5. Commit, push the branch, and open a DRAFT pull request whose body references
   {{.Key}} and explains the change.
6. Post a comment on {{.Key}} with acli linking the pull request.

Hard constraints — violating any of these is a failed session:
- Never commit or push to the default branch. Never merge anything.
- Never force-push and never rewrite existing history.
- Open the pull request as a DRAFT. Do not request reviewers.
- Do not change the item's status or assignee.
- Do not touch unrelated files, dependencies, or CI configuration.

Finish your reply with one line: SUMMARY: <pull request URL, or why you skipped>.`

// cmdProcess implements `burn process <source> [flags]`.
func cmdProcess(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: burn process <source> [flags] (available: %s)", sourceNames())
	}
	name := args[0]
	src, err := lookupSource(name)
	if err != nil {
		return err
	}

	home, _ := os.UserHomeDir()
	fs := flag.NewFlagSet("process "+name, flag.ExitOnError)
	pc := processConfig{Source: name}
	registerRunFlags(fs, &pc.Run, home)
	registerQueryFlags(fs, &pc.Query, name == "jira")
	fs.StringVar(&pc.Mode, "mode", modeEnrich, "what to do with each item: enrich|implement|auto")
	fs.IntVar(&pc.MaxItems, "max-items", 5, "hard cap on items started in this run")
	fs.IntVar(&pc.MaxErrors, "max-errors", 2, "stop after this many consecutive failing items; 0 disables")
	fs.DurationVar(&pc.MaxRuntime, "max-runtime", 0, "stop starting new items past this duration (e.g. 6h); 0 disables")
	fs.StringVar(&pc.StopFile, "stop-file", filepath.Join(home, ".claude", "burn", "STOP"), "kill switch: stop before the next item if this file exists")
	fs.BoolVar(&pc.Redo, "redo", false, "reprocess items the store already records as done")
	fs.StringVar(&pc.TemplatePath, "prompt-template", "", "text/template file overriding the built-in per-mode prompt")
	fs.StringVar(&pc.DigestPath, "digest", "", "write the run digest as markdown to this path")
	fs.BoolVar(&pc.NotifyEach, "notify-each", false, "notify per finished item, not only at the end of the run")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	if err := validateProcessConfig(&pc); err != nil {
		return err
	}
	query, err := resolveQuery(src, pc.Query)
	if err != nil {
		return err
	}

	tmplText, err := loadPromptTemplate(pc.TemplatePath)
	if err != nil {
		return err
	}

	items, err := src.ListItems(query)
	if err != nil {
		return err
	}

	store, err := openStore(pc.Run.Store)
	if err != nil {
		return err
	}
	defer store.Close()
	records, err := loadRecords(pc.Run.Store)
	if err != nil {
		return fmt.Errorf("reading store %s: %w", pc.Run.Store, err)
	}
	done := map[string]bool{}
	if !pc.Redo {
		done = processedKeys(records, pc.Source)
	}
	picked, skipped := filterItems(items, done, pc.MaxItems)

	uc, err := newUsageClient()
	if err != nil {
		return err
	}

	if pc.Run.DryRun {
		return processDryRun(pc, uc, query, picked, skipped, tmplText)
	}
	if len(picked) == 0 {
		fmt.Printf("query: %s\nno items to process (%d matched, %d already done)\n", query, len(items), skipped)
		return nil
	}

	// Gate before preflight, not only inside the loop: preflight spends a probe
	// session and waits ~3m for the server to reflect it, so a kill switch or an
	// already-breached target has to be honoured before that, exactly as
	// doLaunch does for `burn run`.
	if fileExists(pc.StopFile) {
		return fmt.Errorf("stop file %s exists — remove it to allow a run", pc.StopFile)
	}
	u, err := uc.Get()
	if err != nil {
		return err
	}
	if reason := breachMessage(u, pc.Run.Target, pc.Run.WeeklyTarget); reason != "" {
		return fmt.Errorf("at/over threshold: %s — stop starting sessions", reason)
	}

	// The scratch workdir is where a session runs when there is no --repo, and
	// exec fails outright on a non-existent Dir.
	if err := os.MkdirAll(pc.Run.Workdir, 0o755); err != nil {
		return err
	}
	if err := preflight(pc.Run, uc, uc.Token()); err != nil {
		return err
	}

	digest, err := processItems(pc, uc, store, query, picked, skipped, tmplText)
	if err != nil {
		return err
	}
	return publishDigest(pc, digest)
}

// validateProcessConfig checks the mode/jobs/repo/permission combination
// before anything is queried or spawned. Pure apart from the sandbox and repo
// path checks it delegates to.
func validateProcessConfig(pc *processConfig) error {
	switch pc.Mode {
	case modeEnrich, modeImplement, modeAuto:
	default:
		return fmt.Errorf("--mode must be one of enrich|implement|auto, got %q", pc.Mode)
	}
	if pc.Run.Jobs > 1 && pc.Mode != modeEnrich {
		return fmt.Errorf("--jobs %d is only supported with --mode enrich (a repo mounted into concurrent sessions would race)", pc.Run.Jobs)
	}
	if pc.Run.Jobs < 1 {
		return fmt.Errorf("--jobs must be at least 1")
	}
	if pc.MaxItems < 1 {
		return fmt.Errorf("--max-items must be at least 1")
	}
	if pc.Mode == modeImplement && pc.Run.Repo == "" {
		return fmt.Errorf("--mode implement needs --repo pointing at the repository to work in")
	}
	if !pc.Run.DryRun && !pc.Run.SkipPermissions {
		return fmt.Errorf("burn process runs unattended, so it needs --dangerously-skip-permissions " +
			"(pair it with --sandbox --repo to isolate writes), or --dry-run to preview")
	}
	if pc.Run.Sandbox {
		return validateSandboxConfig(&pc.Run)
	}
	// Host mode: the session's working directory IS the repo for implement
	// work, so point the scratch workdir at it rather than silently working in
	// an empty temp dir.
	if pc.Run.Repo != "" {
		abs, err := filepath.Abs(pc.Run.Repo)
		if err != nil {
			return fmt.Errorf("resolving --repo %q: %w", pc.Run.Repo, err)
		}
		if _, err := os.Stat(filepath.Join(abs, ".git")); err != nil {
			return fmt.Errorf("--repo %q has no .git — point it at a git repository", abs)
		}
		pc.Run.Repo = abs
		pc.Run.Workdir = abs
	}
	return nil
}

// resolveMode picks the effective mode for one item. --mode auto degrades to
// enrich whenever there is no repo to work in, or the item's status says it is
// not ready to build.
func resolveMode(mode string, it WorkItem, haveRepo bool) string {
	if mode != modeAuto {
		return mode
	}
	if !haveRepo {
		return modeEnrich
	}
	if earlyStatuses[strings.ToLower(strings.TrimSpace(it.Status))] {
		return modeEnrich
	}
	return modeImplement
}

// loadPromptTemplate reads --prompt-template, or returns "" meaning "use the
// built-in prompt for each item's resolved mode".
func loadPromptTemplate(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading --prompt-template %s: %w", path, err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return "", fmt.Errorf("--prompt-template %s is empty", path)
	}
	return string(data), nil
}

// builtinPrompt returns the prompt for a resolved mode.
func builtinPrompt(mode string) string {
	if mode == modeImplement {
		return implementPrompt
	}
	return enrichPrompt
}

// renderGoal renders a prompt template against one item. Missing template
// fields are an error rather than an empty string, so a typo in a custom
// --prompt-template fails before quota is spent.
func renderGoal(tmplText string, d promptData) (string, error) {
	t, err := template.New("goal").Option("missingkey=error").Parse(tmplText)
	if err != nil {
		return "", fmt.Errorf("parsing prompt template: %w", err)
	}
	var sb strings.Builder
	if err := t.Execute(&sb, d); err != nil {
		return "", fmt.Errorf("rendering prompt template: %w", err)
	}
	return sb.String(), nil
}

// goalFor resolves an item's mode and renders its prompt.
func goalFor(pc processConfig, it WorkItem, tmplText string) (mode, goal string, err error) {
	mode = resolveMode(pc.Mode, it, pc.Run.Repo != "")
	text := tmplText
	if text == "" {
		text = builtinPrompt(mode)
	}
	repo := pc.Run.Repo
	if pc.Run.Sandbox && repo != "" {
		repo = sandboxMountPath
	}
	goal, err = renderGoal(text, promptData{
		Key:     it.Key,
		Summary: it.Summary,
		Status:  it.Status,
		Labels:  it.Labels,
		Source:  pc.Source,
		Mode:    mode,
		Repo:    repo,
	})
	return mode, goal, err
}

// storeGoal is the Goal recorded for every session in a process run. It is
// deliberately the run shape, not the per-item prompt, so `burn overview`
// (grouped by goal) shows the run in aggregate while `--group item` breaks it
// down per work item.
func storeGoal(source, mode string) string {
	return "process " + source + " " + mode
}

// processedKeys returns the item keys this source already completed without
// error, so a re-run — or a weekend run resumed after a laptop sleep — never
// redoes finished work.
func processedKeys(records []Record, source string) map[string]bool {
	done := map[string]bool{}
	for _, r := range records {
		if r.Kind != "session" || r.ItemKey == "" || r.IsError {
			continue
		}
		if r.ItemSource != "" && r.ItemSource != source {
			continue
		}
		done[r.ItemKey] = true
	}
	return done
}

// filterItems drops already-done items and caps the rest at maxItems,
// preserving the source's ordering. skipped counts only the already-done ones,
// not items left out by the cap.
func filterItems(items []WorkItem, done map[string]bool, maxItems int) (picked []WorkItem, skipped int) {
	for _, it := range items {
		if done[it.Key] {
			skipped++
			continue
		}
		if len(picked) >= maxItems {
			continue
		}
		picked = append(picked, it)
	}
	return picked, skipped
}

// stopState is everything the loop checks before starting another item.
// Separated out and pure so every stop path is unit-testable.
type stopState struct {
	Usage             Usage
	UsageOK           bool
	Target            float64
	WeeklyTarget      float64
	Processed         int
	MaxItems          int
	ConsecutiveErrors int
	MaxErrors         int
	Elapsed           time.Duration
	MaxRuntime        time.Duration
	StopFileExists    bool
}

// reason returns a non-empty explanation once the loop must stop.
func (s stopState) reason() string {
	if s.StopFileExists {
		return "stop file present"
	}
	if s.MaxItems > 0 && s.Processed >= s.MaxItems {
		return fmt.Sprintf("max-items reached (%d)", s.MaxItems)
	}
	if s.MaxErrors > 0 && s.ConsecutiveErrors >= s.MaxErrors {
		return fmt.Sprintf("%d consecutive error(s) >= max-errors %d", s.ConsecutiveErrors, s.MaxErrors)
	}
	if s.MaxRuntime > 0 && s.Elapsed >= s.MaxRuntime {
		return fmt.Sprintf("max-runtime reached (%s)", s.MaxRuntime)
	}
	if s.UsageOK {
		if r := breachMessage(s.Usage, s.Target, s.WeeklyTarget); r != "" {
			return r
		}
	}
	return ""
}

// itemOutcome is one processed item's result, for the digest.
type itemOutcome struct {
	Key     string
	Summary string
	Mode    string
	Outcome string
	Turns   int
	CostUSD float64
}

// runDigest is everything the end-of-run report needs.
type runDigest struct {
	Source      string
	Query       string
	Mode        string
	Jobs        int
	StartedAt   time.Time
	FinishedAt  time.Time
	Picked      int
	Skipped     int
	Items       []itemOutcome
	StopReason  string
	UsageBefore Usage
	UsageAfter  Usage
	AfterOK     bool
	Target      float64
	Weekly      float64
}

// errors counts failed items.
func (d runDigest) errors() int {
	n := 0
	for _, it := range d.Items {
		if it.Outcome != "ok" {
			n++
		}
	}
	return n
}

// headline is the one-line form sent through notify().
func (d runDigest) headline() string {
	msg := fmt.Sprintf("burn process %s (%s): %d item(s), %d error(s)",
		d.Source, d.Mode, len(d.Items), d.errors())
	if d.StopReason != "" {
		msg += " — stopped: " + d.StopReason
	}
	return msg
}

// formatDigest renders the markdown report a returning human reads.
func formatDigest(d runDigest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# burn process %s — %s\n\n", d.Source, d.StartedAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "- query: `%s`\n", d.Query)
	fmt.Fprintf(&b, "- mode: %s | jobs: %d | target: 5h %.1f%%", d.Mode, d.Jobs, d.Target)
	if d.Weekly > 0 {
		fmt.Fprintf(&b, " / 7d %.1f%%", d.Weekly)
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "- picked %d item(s), skipped %d already done\n", d.Picked, d.Skipped)
	if !d.FinishedAt.IsZero() {
		fmt.Fprintf(&b, "- ran for %s\n", d.FinishedAt.Sub(d.StartedAt).Round(time.Second))
	}
	b.WriteString("\n")

	if len(d.Items) == 0 {
		b.WriteString("No items were processed.\n")
	} else {
		b.WriteString("| item | mode | outcome | turns | cost |\n|---|---|---|---|---|\n")
		for _, it := range d.Items {
			// An error outcome is claude's own text; a pipe in it would break
			// the row this digest is read as.
			fmt.Fprintf(&b, "| %s | %s | %s | %d | $%.4f |\n",
				it.Key, it.Mode, strings.ReplaceAll(it.Outcome, "|", "/"), it.Turns, it.CostUSD)
		}
	}

	b.WriteString("\n")
	if d.StopReason != "" {
		fmt.Fprintf(&b, "stopped: %s\n", d.StopReason)
	} else {
		b.WriteString("stopped: all picked items processed\n")
	}
	fmt.Fprintf(&b, "usage before: 5h %.1f%% / 7d %.1f%%\n",
		d.UsageBefore.FiveHour.Utilization, d.UsageBefore.SevenDay.Utilization)
	if d.AfterOK {
		fmt.Fprintf(&b, "usage after: 5h %.1f%% / 7d %.1f%% (may lag up to %s)\n",
			d.UsageAfter.FiveHour.Utilization, d.UsageAfter.SevenDay.Utilization, minPollInterval)
	} else {
		b.WriteString("usage after: unavailable\n")
	}
	return b.String()
}

// processDryRun previews exactly what a real run would do: the compiled query,
// the picked items with their resolved modes, and the first rendered prompt.
func processDryRun(pc processConfig, uc *UsageClient, query string, picked []WorkItem, skipped int, tmplText string) error {
	fmt.Printf("query             : %s\n", query)
	fmt.Printf("mode              : %s\n", pc.Mode)
	fmt.Printf("jobs              : %d\n", pc.Run.Jobs)
	fmt.Printf("picked / skipped  : %d / %d (max-items %d)\n", len(picked), skipped, pc.MaxItems)
	fmt.Printf("stop file         : %s\n", pc.StopFile)
	if pc.Run.Repo != "" {
		fmt.Printf("repo              : %s\n", pc.Run.Repo)
	} else {
		fmt.Printf("repo              : none (implement work is not possible)\n")
	}
	if u, err := uc.Get(); err == nil {
		fmt.Printf("usage now         : 5h %.1f%% / 7d %.1f%% (targets %.1f%% / %.1f%%)\n",
			u.FiveHour.Utilization, u.SevenDay.Utilization, pc.Run.Target, pc.Run.WeeklyTarget)
	} else {
		fmt.Printf("usage now         : unavailable: %v\n", err)
	}
	fmt.Println()
	for _, it := range picked {
		mode, _, err := goalFor(pc, it, tmplText)
		if err != nil {
			return err
		}
		fmt.Printf("  %-14s %-10s %s\n", it.Key, mode, it.Summary)
	}
	if len(picked) == 0 {
		fmt.Println("  (nothing to do)")
		return nil
	}
	_, goal, err := goalFor(pc, picked[0], tmplText)
	if err != nil {
		return err
	}
	fmt.Printf("\n--- prompt for %s ---\n%s\n", picked[0].Key, goal)
	return nil
}

// processItems runs one session per picked item, re-checking every stop
// condition before starting each one. Items run concurrently up to
// --jobs (enrich only, enforced by validateProcessConfig).
func processItems(pc processConfig, uc *UsageClient, store *Store, query string, picked []WorkItem, skipped int, tmplText string) (runDigest, error) {
	before, err := uc.Get()
	if err != nil {
		return runDigest{}, err
	}
	d := runDigest{
		Source:      pc.Source,
		Query:       query,
		Mode:        pc.Mode,
		Jobs:        pc.Run.Jobs,
		StartedAt:   time.Now().UTC(),
		Picked:      len(picked),
		Skipped:     skipped,
		UsageBefore: before,
		Target:      pc.Run.Target,
		Weekly:      pc.Run.WeeklyTarget,
	}

	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		sem      = make(chan struct{}, pc.Run.Jobs)
		consecut int
		started  int
	)

	for _, it := range picked {
		u, uErr := uc.Get()
		mu.Lock()
		st := stopState{
			Usage:             u,
			UsageOK:           uErr == nil,
			Target:            pc.Run.Target,
			WeeklyTarget:      pc.Run.WeeklyTarget,
			Processed:         started,
			MaxItems:          pc.MaxItems,
			ConsecutiveErrors: consecut,
			MaxErrors:         pc.MaxErrors,
			Elapsed:           time.Since(d.StartedAt),
			MaxRuntime:        pc.MaxRuntime,
			StopFileExists:    fileExists(pc.StopFile),
		}
		reason := st.reason()
		mu.Unlock()
		if reason != "" {
			d.StopReason = reason
			break
		}

		mode, goal, err := goalFor(pc, it, tmplText)
		if err != nil {
			return d, err
		}
		if !claimItem(pc, it, mode) {
			fmt.Printf("skip %s: claim command refused it\n", it.Key)
			continue
		}

		started++
		itemCfg := pc.Run
		wg.Add(1)
		sem <- struct{}{}
		go func(it WorkItem, mode, goal string) {
			defer wg.Done()
			defer func() { <-sem }()

			fmt.Printf("start %s (%s): %s\n", it.Key, mode, it.Summary)
			start := time.Now().UTC()
			res, runErr := runClaude(itemCfg, uc.Token(), goal)
			failed := runErr != nil || res.IsError
			outcome := "ok"
			if failed {
				outcome = "error"
				if runErr != nil {
					outcome = "error: " + firstLine(runErr.Error())
				}
			}

			store.Write(Record{
				TS:             time.Now().UTC().Format(time.RFC3339),
				SessionID:      res.SessionID,
				Goal:           storeGoal(pc.Source, mode),
				ItemKey:        it.Key,
				ItemSource:     pc.Source,
				Mode:           mode,
				StartedAt:      start.Format(time.RFC3339),
				Model:          itemCfg.Model,
				CostUSD:        res.TotalCostUSD,
				NumTurns:       res.NumTurns,
				IsError:        failed,
				FiveHourBefore: before.FiveHour.Utilization,
			})

			mu.Lock()
			if failed {
				consecut++
			} else {
				consecut = 0
			}
			d.Items = append(d.Items, itemOutcome{
				Key:     it.Key,
				Summary: it.Summary,
				Mode:    mode,
				Outcome: outcome,
				Turns:   res.NumTurns,
				CostUSD: res.TotalCostUSD,
			})
			mu.Unlock()

			fmt.Printf("done %s (%s): %s, %d turn(s)\n", it.Key, mode, outcome, res.NumTurns)
			if pc.NotifyEach {
				notifyItem(fmt.Sprintf("%s %s: %s", it.Key, mode, outcome), pc.Source, it, mode)
			}
		}(it, mode, goal)
	}
	wg.Wait()

	if after, err := uc.Get(); err == nil {
		d.UsageAfter, d.AfterOK = after, true
	}
	d.FinishedAt = time.Now().UTC()

	// Guard on aggregate reported cost the same way `burn run` does: under a
	// subscription this should stay ~0, and a climbing total means the
	// sessions were routed to API billing.
	if pc.Run.MaxUSDGuard > 0 {
		total := 0.0
		for _, it := range d.Items {
			total += it.CostUSD
		}
		if total > pc.Run.MaxUSDGuard {
			publishDigest(pc, d)
			return d, fmt.Errorf("ABORT: reported cost $%.2f exceeded --max-usd-guard $%.2f — possible API billing",
				total, pc.Run.MaxUSDGuard)
		}
	}
	return d, nil
}

// publishDigest writes the digest to --digest, forwards it to BURN_DIGEST_CMD
// (or BURN_NOTIFY_CMD), and prints the headline through notify().
func publishDigest(pc processConfig, d runDigest) error {
	text := formatDigest(d)
	fmt.Println()
	fmt.Print(text)

	if pc.DigestPath != "" {
		if err := os.MkdirAll(filepath.Dir(pc.DigestPath), 0o755); err != nil {
			return fmt.Errorf("creating digest dir: %w", err)
		}
		if err := os.WriteFile(pc.DigestPath, []byte(text), 0o644); err != nil {
			return fmt.Errorf("writing digest %s: %w", pc.DigestPath, err)
		}
		fmt.Printf("digest written to %s\n", pc.DigestPath)
	}

	cmdVar := "BURN_DIGEST_CMD"
	if os.Getenv(cmdVar) == "" {
		cmdVar = "BURN_NOTIFY_CMD"
	}
	runHook(cmdVar, text, nil)
	notify(d.headline())
	return nil
}

// claimItem runs BURN_CLAIM_CMD, if set, before an item is started. A non-zero
// exit means "not mine" (e.g. another runner already labelled the ticket) and
// the item is skipped. No hook set means every item is claimable.
func claimItem(pc processConfig, it WorkItem, mode string) bool {
	if os.Getenv("BURN_CLAIM_CMD") == "" {
		return true
	}
	return runHook("BURN_CLAIM_CMD", "claiming "+it.Key, itemEnv(pc.Source, it, mode)) == nil
}

// notifyItem is notify() with the item's identity exported to the hook, so a
// per-item Slack line can render the key and summary itself.
func notifyItem(msg, source string, it WorkItem, mode string) {
	fmt.Print("\a")
	fmt.Println("NOTICE: " + msg)
	runHook("BURN_NOTIFY_CMD", msg, itemEnv(source, it, mode))
}

func itemEnv(source string, it WorkItem, mode string) []string {
	return []string{
		"BURN_ITEM_KEY=" + it.Key,
		"BURN_ITEM_SUMMARY=" + it.Summary,
		"BURN_ITEM_STATUS=" + it.Status,
		"BURN_ITEM_MODE=" + mode,
		"BURN_ITEM_SOURCE=" + source,
	}
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
