package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResolveMode(t *testing.T) {
	cases := []struct {
		name     string
		mode     string
		status   string
		haveRepo bool
		want     string
	}{
		{"explicit enrich stays enrich", modeEnrich, "In Progress", true, modeEnrich},
		{"explicit implement stays implement", modeImplement, "To Refine", true, modeImplement},
		{"auto without repo degrades to enrich", modeAuto, "Ready", false, modeEnrich},
		{"auto on early status enriches", modeAuto, "To Refine", true, modeEnrich},
		{"auto on early status is case-insensitive", modeAuto, "  BACKLOG ", true, modeEnrich},
		{"auto on empty status enriches", modeAuto, "", true, modeEnrich},
		{"auto on ready status implements", modeAuto, "Selected for Development", true, modeImplement},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := resolveMode(c.mode, WorkItem{Status: c.status}, c.haveRepo)
			if got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestRenderGoalBuiltinPrompts(t *testing.T) {
	d := promptData{Key: "DEMO-101", Summary: "Add a health check", Status: "To Refine", Source: "jira", Repo: "/workspace"}

	enrich, err := renderGoal(builtinPrompt(modeEnrich), d)
	if err != nil {
		t.Fatalf("enrich: %v", err)
	}
	for _, want := range []string{"DEMO-101", "Add a health check", "To Refine", "SINGLE comment", "SUMMARY:"} {
		if !strings.Contains(enrich, want) {
			t.Fatalf("enrich prompt missing %q:\n%s", want, enrich)
		}
	}
	if strings.Contains(enrich, "{{") {
		t.Fatalf("enrich prompt left an unrendered action:\n%s", enrich)
	}

	impl, err := renderGoal(builtinPrompt(modeImplement), d)
	if err != nil {
		t.Fatalf("implement: %v", err)
	}
	// The guardrails are the whole reason this prompt is safe to run
	// unattended; assert each one survives an edit.
	for _, want := range []string{
		"/workspace",
		"DRAFT pull request",
		"Never commit or push to the default branch",
		"Never merge anything",
		"Never force-push",
		"SKIPPED:",
	} {
		if !strings.Contains(impl, want) {
			t.Fatalf("implement prompt missing guardrail %q:\n%s", want, impl)
		}
	}
}

func TestRenderGoalCustomTemplate(t *testing.T) {
	got, err := renderGoal("work {{.Key}} in {{.Repo}} ({{.Mode}})", promptData{Key: "X-1", Repo: "/r", Mode: modeEnrich})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "work X-1 in /r (enrich)" {
		t.Fatalf("got %q", got)
	}
}

func TestRenderGoalRejectsBadTemplate(t *testing.T) {
	if _, err := renderGoal("{{.Key", promptData{}); err == nil {
		t.Fatal("expected a parse error for an unterminated action")
	}
	if _, err := renderGoal("{{.Nope}}", promptData{}); err == nil {
		t.Fatal("expected an error for a field that does not exist")
	}
}

func TestProcessedKeys(t *testing.T) {
	records := []Record{
		{Kind: "session", ItemKey: "DEMO-1", ItemSource: "jira"},
		{Kind: "session", ItemKey: "DEMO-2", ItemSource: "jira", IsError: true},
		{Kind: "session", ItemKey: "OTHER-1", ItemSource: "clickup"},
		{Kind: "watch", ItemKey: "DEMO-3", ItemSource: "jira"},
		{Kind: "session", Goal: "plain run"},
	}
	done := processedKeys(records, "jira")
	if !done["DEMO-1"] {
		t.Fatal("a successful jira session should mark its item done")
	}
	if done["DEMO-2"] {
		t.Fatal("a failed session must not mark its item done — it should be retried")
	}
	if done["OTHER-1"] {
		t.Fatal("another source's item must not count as done for jira")
	}
	if done["DEMO-3"] {
		t.Fatal("a watch record is not a session")
	}
	if len(done) != 1 {
		t.Fatalf("expected exactly 1 done key, got %v", done)
	}
}

func TestFilterItems(t *testing.T) {
	items := []WorkItem{{Key: "A"}, {Key: "B"}, {Key: "C"}, {Key: "D"}}
	picked, skipped := filterItems(items, map[string]bool{"B": true}, 2)
	if skipped != 1 {
		t.Fatalf("skipped: got %d, want 1", skipped)
	}
	if len(picked) != 2 || picked[0].Key != "A" || picked[1].Key != "C" {
		t.Fatalf("picked: got %+v, want A and C in source order", picked)
	}
}

func TestFilterItemsCapDoesNotCountAsSkipped(t *testing.T) {
	_, skipped := filterItems([]WorkItem{{Key: "A"}, {Key: "B"}}, nil, 1)
	if skipped != 0 {
		t.Fatalf("items left out by --max-items are not 'already done'; got skipped=%d", skipped)
	}
}

func TestStopStateReason(t *testing.T) {
	base := stopState{UsageOK: true, Target: 80, MaxItems: 5, MaxErrors: 2}
	usage := func(fiveHour, sevenDay float64) Usage {
		var u Usage
		u.FiveHour.Utilization = fiveHour
		u.SevenDay.Utilization = sevenDay
		return u
	}

	t.Run("keeps going with headroom", func(t *testing.T) {
		s := base
		s.Usage = usage(10, 5)
		if r := s.reason(); r != "" {
			t.Fatalf("expected no stop, got %q", r)
		}
	})

	t.Run("stop file wins over everything", func(t *testing.T) {
		s := base
		s.Usage, s.StopFileExists, s.Processed = usage(99, 99), true, 99
		if r := s.reason(); r != "stop file present" {
			t.Fatalf("got %q", r)
		}
	})

	t.Run("max items", func(t *testing.T) {
		s := base
		s.Usage, s.Processed = usage(10, 5), 5
		if r := s.reason(); !strings.Contains(r, "max-items") {
			t.Fatalf("got %q", r)
		}
	})

	t.Run("consecutive errors", func(t *testing.T) {
		s := base
		s.Usage, s.ConsecutiveErrors = usage(10, 5), 2
		if r := s.reason(); !strings.Contains(r, "consecutive error") {
			t.Fatalf("got %q", r)
		}
	})

	t.Run("max runtime", func(t *testing.T) {
		s := base
		s.Usage, s.MaxRuntime, s.Elapsed = usage(10, 5), time.Hour, 2*time.Hour
		if r := s.reason(); !strings.Contains(r, "max-runtime") {
			t.Fatalf("got %q", r)
		}
	})

	t.Run("five hour target", func(t *testing.T) {
		s := base
		s.Usage = usage(80, 5)
		if r := s.reason(); !strings.Contains(r, "5-hour usage") {
			t.Fatalf("got %q", r)
		}
	})

	t.Run("weekly target", func(t *testing.T) {
		s := base
		s.Usage, s.WeeklyTarget = usage(10, 41), 40
		if r := s.reason(); !strings.Contains(r, "7-day usage") {
			t.Fatalf("got %q", r)
		}
	})

	t.Run("usage read failure does not stop the loop", func(t *testing.T) {
		s := base
		s.UsageOK, s.Usage = false, usage(99, 99)
		if r := s.reason(); r != "" {
			t.Fatalf("a transient usage error must not be read as a breach; got %q", r)
		}
	})

	t.Run("zero max-errors disables the breaker", func(t *testing.T) {
		s := base
		s.Usage, s.MaxErrors, s.ConsecutiveErrors = usage(10, 5), 0, 9
		if r := s.reason(); r != "" {
			t.Fatalf("got %q", r)
		}
	})
}

func TestValidateProcessConfig(t *testing.T) {
	base := func() processConfig {
		return processConfig{
			Source:   "jira",
			Mode:     modeEnrich,
			MaxItems: 5,
			Run:      Config{Jobs: 1, SkipPermissions: true},
		}
	}

	t.Run("accepts a minimal enrich run", func(t *testing.T) {
		pc := base()
		if err := validateProcessConfig(&pc); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("rejects an unknown mode", func(t *testing.T) {
		pc := base()
		pc.Mode = "refactor"
		if err := validateProcessConfig(&pc); err == nil {
			t.Fatal("expected an error")
		}
	})

	t.Run("rejects parallel jobs outside enrich", func(t *testing.T) {
		pc := base()
		pc.Mode, pc.Run.Jobs = modeAuto, 3
		if err := validateProcessConfig(&pc); err == nil {
			t.Fatal("parallel sessions against one repo would race — expected a refusal")
		}
	})

	t.Run("allows parallel jobs for enrich", func(t *testing.T) {
		pc := base()
		pc.Run.Jobs = 4
		if err := validateProcessConfig(&pc); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("implement needs a repo", func(t *testing.T) {
		pc := base()
		pc.Mode = modeImplement
		if err := validateProcessConfig(&pc); err == nil {
			t.Fatal("expected an error")
		}
	})

	t.Run("requires unattended permissions", func(t *testing.T) {
		pc := base()
		pc.Run.SkipPermissions = false
		err := validateProcessConfig(&pc)
		if err == nil {
			t.Fatal("a headless loop that prompts would hang — expected a refusal")
		}
		if !strings.Contains(err.Error(), "dangerously-skip-permissions") {
			t.Fatalf("error should name the flag to pass, got %v", err)
		}
	})

	t.Run("dry-run needs no permissions flag", func(t *testing.T) {
		pc := base()
		pc.Run.SkipPermissions, pc.Run.DryRun = false, true
		if err := validateProcessConfig(&pc); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("foreground needs no skip-permissions flag either", func(t *testing.T) {
		pc := base()
		pc.Run.SkipPermissions, pc.Run.Foreground = false, true
		if err := validateProcessConfig(&pc); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("foreground still rejects parallel jobs", func(t *testing.T) {
		pc := base()
		pc.Run.SkipPermissions, pc.Run.Foreground, pc.Run.Jobs = false, true, 2
		if err := validateProcessConfig(&pc); err == nil {
			t.Fatal("one terminal can't attach to more than one interactive session — expected a refusal")
		}
	})

	t.Run("host repo becomes the session workdir", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		pc := base()
		pc.Mode, pc.Run.Repo = modeImplement, dir
		if err := validateProcessConfig(&pc); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if pc.Run.Workdir != pc.Run.Repo {
			t.Fatalf("workdir %q should be the repo %q", pc.Run.Workdir, pc.Run.Repo)
		}
	})

	t.Run("host repo must be a git repo", func(t *testing.T) {
		pc := base()
		pc.Mode, pc.Run.Repo = modeImplement, t.TempDir()
		if err := validateProcessConfig(&pc); err == nil {
			t.Fatal("expected an error for a directory with no .git")
		}
	})
}

func TestGoalForUsesSandboxMountPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	pc := processConfig{
		Source: "jira",
		Mode:   modeImplement,
		Run:    Config{Repo: dir, Sandbox: true},
	}
	mode, goal, err := goalFor(pc, WorkItem{Key: "DEMO-9", Summary: "x"}, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mode != modeImplement {
		t.Fatalf("mode: got %q", mode)
	}
	// A sandboxed session sees the repo at the mount path, never at the host path.
	if !strings.Contains(goal, sandboxMountPath) {
		t.Fatalf("sandboxed prompt should point at %s:\n%s", sandboxMountPath, goal)
	}
	if strings.Contains(goal, dir) {
		t.Fatalf("sandboxed prompt leaked the host path %s:\n%s", dir, goal)
	}
}

func TestStoreGoal(t *testing.T) {
	if got := storeGoal("jira", modeEnrich); got != "process jira enrich" {
		t.Fatalf("got %q", got)
	}
}

func TestFormatDigest(t *testing.T) {
	start := time.Date(2026, 9, 8, 20, 0, 0, 0, time.UTC)
	d := runDigest{
		Source:     "jira",
		Query:      `labels = "claude-ready" ORDER BY created ASC`,
		Mode:       modeEnrich,
		Jobs:       2,
		StartedAt:  start,
		FinishedAt: start.Add(12 * time.Minute),
		Picked:     2,
		Skipped:    3,
		Target:     80,
		Weekly:     50,
		Items: []itemOutcome{
			{Key: "DEMO-1", Mode: modeEnrich, Outcome: "ok", Turns: 8, CostUSD: 0.5},
			{Key: "DEMO-2", Mode: modeEnrich, Outcome: "error", Turns: 2},
		},
		StopReason: "max-items reached (2)",
		AfterOK:    true,
	}
	d.UsageAfter.FiveHour.Utilization = 62.5

	out := formatDigest(d)
	for _, want := range []string{
		"# burn process jira — 2026-09-08T20:00:00Z",
		`labels = "claude-ready"`,
		"jobs: 2",
		"5h 80.0% / 7d 50.0%",
		"picked 2 item(s), skipped 3 already done",
		"ran for 12m0s",
		"| DEMO-1 | enrich | ok | 8 | $0.5000 |",
		"| DEMO-2 | enrich | error | 2 | $0.0000 |",
		"stopped: max-items reached (2)",
		"usage after: 5h 62.5%",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("digest missing %q:\n%s", want, out)
		}
	}

	if d.errors() != 1 {
		t.Fatalf("errors: got %d, want 1", d.errors())
	}
	head := d.headline()
	if !strings.Contains(head, "2 item(s), 1 error(s)") || !strings.Contains(head, "max-items reached") {
		t.Fatalf("headline: got %q", head)
	}
}

func TestFormatDigestNoItems(t *testing.T) {
	out := formatDigest(runDigest{Source: "jira", Mode: modeEnrich})
	if !strings.Contains(out, "No items were processed.") {
		t.Fatalf("got:\n%s", out)
	}
	if !strings.Contains(out, "stopped: all picked items processed") {
		t.Fatalf("an empty run with no stop reason should say so:\n%s", out)
	}
	if !strings.Contains(out, "usage after: unavailable") {
		t.Fatalf("got:\n%s", out)
	}
}

func TestLoadPromptTemplate(t *testing.T) {
	if got, err := loadPromptTemplate(""); err != nil || got != "" {
		t.Fatalf("no path should mean the built-in prompt; got %q, %v", got, err)
	}
	path := filepath.Join(t.TempDir(), "tmpl.txt")
	if err := os.WriteFile(path, []byte("do {{.Key}}"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := loadPromptTemplate(path)
	if err != nil || got != "do {{.Key}}" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := loadPromptTemplate(filepath.Join(t.TempDir(), "nope.txt")); err == nil {
		t.Fatal("expected an error for a missing template file")
	}
	empty := filepath.Join(t.TempDir(), "empty.txt")
	if err := os.WriteFile(empty, []byte("   \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadPromptTemplate(empty); err == nil {
		t.Fatal("an empty template would spend quota on an empty prompt — expected an error")
	}
}

func TestFileExists(t *testing.T) {
	if fileExists("") {
		t.Fatal("an unset stop file must never read as present")
	}
	path := filepath.Join(t.TempDir(), "STOP")
	if fileExists(path) {
		t.Fatal("expected absent")
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if !fileExists(path) {
		t.Fatal("expected present")
	}
}

func TestClaimItemHonoursHookExitCode(t *testing.T) {
	pc := processConfig{Source: "jira"}
	it := WorkItem{Key: "DEMO-7", Summary: "s"}

	t.Setenv("BURN_CLAIM_CMD", "")
	if !claimItem(pc, it, modeEnrich) {
		t.Fatal("no hook set means every item is claimable")
	}

	t.Setenv("BURN_CLAIM_CMD", "exit 1")
	if claimItem(pc, it, modeEnrich) {
		t.Fatal("a non-zero hook exit means the item is already claimed elsewhere")
	}

	// The hook must see the item's identity so it can label the real ticket.
	out := filepath.Join(t.TempDir(), "claimed")
	t.Setenv("BURN_CLAIM_CMD", `printf '%s %s' "$BURN_ITEM_KEY" "$BURN_ITEM_MODE" > `+out)
	if !claimItem(pc, it, modeEnrich) {
		t.Fatal("a zero-exit hook claims the item")
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "DEMO-7 enrich" {
		t.Fatalf("hook env: got %q", data)
	}
}

func TestRunHook(t *testing.T) {
	t.Setenv("BURN_TEST_HOOK", "")
	if err := runHook("BURN_TEST_HOOK", "msg", nil); err != nil {
		t.Fatalf("an unset hook is a no-op; got %v", err)
	}
	out := filepath.Join(t.TempDir(), "msg")
	t.Setenv("BURN_TEST_HOOK", `printf '%s' "$BURN_MSG" > `+out)
	if err := runHook("BURN_TEST_HOOK", "hello digest", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello digest" {
		t.Fatalf("got %q", data)
	}
}

func TestFormatDigestEscapesPipesInOutcome(t *testing.T) {
	out := formatDigest(runDigest{
		Items: []itemOutcome{{Key: "DEMO-1", Mode: modeEnrich, Outcome: "error: sh -c a | b failed"}},
	})
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "| DEMO-1 ") {
			if strings.Count(line, "|") != 6 {
				t.Fatalf("a pipe in the outcome broke the table row: %q", line)
			}
			return
		}
	}
	t.Fatal("no row for DEMO-1 in digest")
}
