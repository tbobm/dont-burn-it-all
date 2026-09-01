package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The env scrub is the safety path: a stray ANTHROPIC_API_KEY must never reach a
// child session (it would route to pay-per-token API billing), and the
// subscription token must be pinned.
func TestChildEnvScrubsBillingRiskAndPinsToken(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-should-be-dropped")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "should-be-dropped")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "stale-should-be-replaced")
	t.Setenv("PATH_KEEPME", "keep")

	env := childEnv("TOK123")

	var oauthCount int
	for _, kv := range env {
		if strings.HasPrefix(kv, "ANTHROPIC_API_KEY=") {
			t.Fatalf("ANTHROPIC_API_KEY leaked into child env: %q", kv)
		}
		if strings.HasPrefix(kv, "ANTHROPIC_AUTH_TOKEN=") {
			t.Fatalf("ANTHROPIC_AUTH_TOKEN leaked into child env: %q", kv)
		}
		if kv == "CLAUDE_CODE_OAUTH_TOKEN=TOK123" {
			oauthCount++
		}
		if strings.HasPrefix(kv, "CLAUDE_CODE_OAUTH_TOKEN=") && kv != "CLAUDE_CODE_OAUTH_TOKEN=TOK123" {
			t.Fatalf("stale CLAUDE_CODE_OAUTH_TOKEN survived: %q", kv)
		}
	}
	if oauthCount != 1 {
		t.Fatalf("expected exactly one pinned CLAUDE_CODE_OAUTH_TOKEN, got %d", oauthCount)
	}

	var keptPath bool
	for _, kv := range env {
		if kv == "PATH_KEEPME=keep" {
			keptPath = true
		}
	}
	if !keptPath {
		t.Fatal("unrelated env var was dropped")
	}
}

// --sandbox is opt-in: without the flag, validateSandboxConfig must never be
// reached and no docker/osb dependency should be implied. This test covers the
// validation that does run once --sandbox is set: repo must be a real git repo,
// and jobs>1 is refused rather than silently racing on one mounted repo.
func TestValidateSandboxConfigRequiresGitRepo(t *testing.T) {
	cfg := Config{Sandbox: true, Workdir: t.TempDir(), Jobs: 1}
	if err := validateSandboxConfig(&cfg); err == nil {
		t.Fatal("expected error for non-git workdir, got nil")
	}
}

func TestValidateSandboxConfigDefaultsRepoToWorkdir(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(dir+"/.git", 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Sandbox: true, Workdir: dir, Jobs: 1}
	if err := validateSandboxConfig(&cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Repo != dir {
		t.Fatalf("expected Repo to default to Workdir %q, got %q", dir, cfg.Repo)
	}
}

// A git worktree has .git as a *file* (pointing at the real gitdir), not a
// directory — validateSandboxConfig must accept that too.
func TestValidateSandboxConfigAcceptsWorktreeGitFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/.git", []byte("gitdir: /elsewhere/.git/worktrees/x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Sandbox: true, Workdir: dir, Jobs: 1}
	if err := validateSandboxConfig(&cfg); err != nil {
		t.Fatalf("unexpected error for worktree-style .git file: %v", err)
	}
}

// A relative --repo must resolve to an absolute path — os.Stat happily
// validates a relative path against the process cwd, but the same string
// later becomes osb's --mount source, which isn't resolved against burn's cwd.
func TestValidateSandboxConfigResolvesRelativeRepo(t *testing.T) {
	parent := t.TempDir()
	repo := parent + "/repo"
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(repo+"/.git", 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(parent)

	cfg := Config{Sandbox: true, Repo: "./repo", Jobs: 1}
	if err := validateSandboxConfig(&cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !filepath.IsAbs(cfg.Repo) {
		t.Fatalf("expected an absolute Repo path, got %q", cfg.Repo)
	}
	if cfg.Repo != repo {
		t.Fatalf("expected Repo %q, got %q", repo, cfg.Repo)
	}
}

func TestValidateSandboxConfigRejectsParallelJobs(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(dir+"/.git", 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Sandbox: true, Repo: dir, Jobs: 2}
	if err := validateSandboxConfig(&cfg); err == nil {
		t.Fatal("expected error for --sandbox with --jobs 2, got nil")
	}
}

// --aws-profile must fail fast (before ever calling osb) when the host has no
// ~/.aws to mount — a clearer error than whatever osb would report for a
// missing bind-mount source.
func TestValidateSandboxConfigRequiresAWSDirWhenProfileSet(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := t.TempDir()
	if err := os.Mkdir(dir+"/.git", 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Sandbox: true, Repo: dir, Jobs: 1, AWSProfile: "readonly"}
	if err := validateSandboxConfig(&cfg); err == nil {
		t.Fatal("expected error when ~/.aws does not exist, got nil")
	}

	if err := os.Mkdir(home+"/.aws", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := validateSandboxConfig(&cfg); err != nil {
		t.Fatalf("unexpected error once ~/.aws exists: %v", err)
	}
}

// The preflight freshness stamp must be mode-keyed: a host-mode metering proof
// must never be mistaken for a sandbox-mode one (and vice versa), or a
// --sandbox run could skip its billing-metering probe entirely on the strength
// of a proof that only ever exercised the host `claude` binary.
func TestPreflightStampIsolatedByMode(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if isPreflightFresh(false) || isPreflightFresh(true) {
		t.Fatal("expected no stamp to exist yet")
	}

	markPreflightFresh(false)
	if !isPreflightFresh(false) {
		t.Fatal("host stamp should be fresh after markPreflightFresh(false)")
	}
	if isPreflightFresh(true) {
		t.Fatal("a host-mode stamp must not count as a fresh sandbox-mode proof")
	}

	markPreflightFresh(true)
	if !isPreflightFresh(true) {
		t.Fatal("sandbox stamp should be fresh after markPreflightFresh(true)")
	}
}

// The --volumes-file schema is not documented in `osb --help` or the upstream
// README — this locks in the shape confirmed against a live local
// opensandbox-server v0.1.1 (`[{"name","mountPath","host":{"path"}}]`), so a
// future osb upgrade that silently changes it fails a test instead of failing
// a real --sandbox launch.
func TestWriteVolumesFileSchema(t *testing.T) {
	path, err := writeVolumesFile([]sandboxVolume{
		{Name: "repo", MountPath: sandboxMountPath, Host: sandboxHostVolume{Path: "/tmp/some-repo"}},
		{Name: "aws", MountPath: sandboxAWSMountPath, Host: sandboxHostVolume{Path: "/tmp/some-aws"}, ReadOnly: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var vols []sandboxVolume
	if err := json.Unmarshal(data, &vols); err != nil {
		t.Fatalf("volumes file is not valid JSON for the expected schema: %v", err)
	}
	if len(vols) != 2 {
		t.Fatalf("expected exactly two volumes, got %d", len(vols))
	}
	if vols[0].MountPath != sandboxMountPath || vols[0].ReadOnly {
		t.Fatalf("expected repo volume read-write at %q, got %+v", sandboxMountPath, vols[0])
	}
	if vols[0].Host.Path != "/tmp/some-repo" {
		t.Fatalf("expected host.path %q, got %q", "/tmp/some-repo", vols[0].Host.Path)
	}
	if vols[1].MountPath != sandboxAWSMountPath || !vols[1].ReadOnly {
		t.Fatalf("expected aws volume read-only at %q, got %+v", sandboxAWSMountPath, vols[1])
	}
}

// The entrypoint script is the mechanism that keeps secret values out of any
// host-visible argv (osb's `sandbox create -e KEY=VALUE` puts the value
// directly in this process's own argv, confirmed live against a real osb
// server — this script sidesteps that by only ever referencing file paths).
// It must never contain a literal secret, only paths and the configured env
// var name.
func TestSandboxEntrypointScriptNoSecretsEmbedded(t *testing.T) {
	cfg := Config{GHTokenEnv: "GH_TOKEN"}
	script := sandboxEntrypointScript(cfg, true, "", "")
	if !strings.Contains(script, sandboxOAuthTokenFile) {
		t.Fatal("expected script to reference the oauth token file path")
	}
	if !strings.Contains(script, "export GH_TOKEN=") || !strings.Contains(script, sandboxGHTokenFile) {
		t.Fatal("expected script to export GH_TOKEN from the gh token file path")
	}
	if !strings.HasSuffix(strings.TrimSpace(script), `exec claude "$@"`) {
		t.Fatalf("expected script to hand off to claude with args untouched, got %q", script)
	}
}

func TestSandboxEntrypointScriptOmitsGHExportWhenNoToken(t *testing.T) {
	cfg := Config{GHTokenEnv: "GH_TOKEN"}
	script := sandboxEntrypointScript(cfg, false, "", "")
	if strings.Contains(script, "GH_TOKEN") {
		t.Fatalf("expected no GH_TOKEN export when hasGHToken is false, got %q", script)
	}
}

func TestSandboxEntrypointScriptExportsAWSProfileAndRegion(t *testing.T) {
	cfg := Config{GHTokenEnv: "GH_TOKEN"}
	script := sandboxEntrypointScript(cfg, false, "readonly", "eu-west-1")
	if !strings.Contains(script, `export AWS_PROFILE="readonly"`) {
		t.Fatalf("expected script to export AWS_PROFILE, got %q", script)
	}
	if !strings.Contains(script, `export AWS_REGION="eu-west-1"`) {
		t.Fatalf("expected script to export AWS_REGION, got %q", script)
	}
}

func TestSandboxEntrypointScriptOmitsAWSWhenNoProfile(t *testing.T) {
	cfg := Config{GHTokenEnv: "GH_TOKEN"}
	script := sandboxEntrypointScript(cfg, false, "", "")
	if strings.Contains(script, "AWS_PROFILE") || strings.Contains(script, "AWS_REGION") {
		t.Fatalf("expected no AWS exports when awsProfile is empty, got %q", script)
	}
}

// shutdownSandboxes must not block indefinitely when nothing is in flight —
// a regression here would delay every SIGINT/SIGTERM exit by the full grace
// period even for host-mode runs that never touched a sandbox.
func TestShutdownSandboxesReturnsQuicklyWithNoActivity(t *testing.T) {
	sandboxShuttingDown.Store(false)
	t.Cleanup(func() { sandboxShuttingDown.Store(false) })

	start := time.Now()
	shutdownSandboxes()
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("shutdownSandboxes took %v with no in-flight sandboxes, expected a near-instant return", elapsed)
	}
	if !sandboxShuttingDown.Load() {
		t.Fatal("expected sandboxShuttingDown to be set after shutdownSandboxes")
	}
}

// A signal arriving mid-`osb sandbox create` (before ensureSandbox has an id
// to register) must not be missed: shutdownSandboxes should set the flag
// immediately, then wait for the in-flight create to finish and self-clean
// (via sandboxCreatesInFlight) rather than returning while a sandbox is still
// being created.
func TestShutdownSandboxesWaitsForInFlightCreate(t *testing.T) {
	sandboxShuttingDown.Store(false)
	t.Cleanup(func() { sandboxShuttingDown.Store(false) })

	sandboxCreatesInFlight.Add(1)
	done := make(chan struct{})
	go func() {
		shutdownSandboxes()
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)
	if !sandboxShuttingDown.Load() {
		t.Fatal("expected sandboxShuttingDown to be set immediately, before the in-flight create finishes")
	}
	select {
	case <-done:
		t.Fatal("shutdownSandboxes returned before the in-flight create finished")
	default:
	}

	sandboxCreatesInFlight.Done()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdownSandboxes did not return promptly after the in-flight create finished")
	}
}

func TestBreachMessage(t *testing.T) {
	cases := []struct {
		name            string
		u               Usage
		target          float64
		weeklyTarget    float64
		wantEmpty       bool
		wantFiveHourMsg bool
		wantWeeklyMsg   bool
	}{
		{
			name:         "both under threshold",
			u:            Usage{FiveHour: Window{Utilization: 10}, SevenDay: Window{Utilization: 10}},
			target:       25,
			weeklyTarget: 40,
			wantEmpty:    true,
		},
		{
			name:            "five-hour at/over target, weekly disabled",
			u:               Usage{FiveHour: Window{Utilization: 25}, SevenDay: Window{Utilization: 99}},
			target:          25,
			weeklyTarget:    0,
			wantFiveHourMsg: true,
		},
		{
			name:            "five-hour at/over target, weekly under",
			u:               Usage{FiveHour: Window{Utilization: 30}, SevenDay: Window{Utilization: 10}},
			target:          25,
			weeklyTarget:    40,
			wantFiveHourMsg: true,
		},
		{
			name:          "five-hour under, weekly at/over",
			u:             Usage{FiveHour: Window{Utilization: 10}, SevenDay: Window{Utilization: 40}},
			target:        25,
			weeklyTarget:  40,
			wantWeeklyMsg: true,
		},
		{
			name:         "weeklyTarget 0 with SevenDay at 99, five-hour under",
			u:            Usage{FiveHour: Window{Utilization: 10}, SevenDay: Window{Utilization: 99}},
			target:       25,
			weeklyTarget: 0,
			wantEmpty:    true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := breachMessage(tc.u, tc.target, tc.weeklyTarget)
			if tc.wantEmpty && got != "" {
				t.Fatalf("expected empty string, got %q", got)
			}
			if tc.wantFiveHourMsg {
				if !strings.Contains(got, "5-hour") {
					t.Fatalf("expected 5-hour message, got %q", got)
				}
				if strings.Contains(got, "7-day") || strings.Contains(got, "weekly-target") {
					t.Fatalf("weekly message must not appear, got %q", got)
				}
			}
			if tc.wantWeeklyMsg && !strings.Contains(got, "7-day") {
				t.Fatalf("expected 7-day/weekly message, got %q", got)
			}
		})
	}
}

func TestCredsBlobParse(t *testing.T) {
	raw := `{"claudeAiOauth":{"accessToken":"sk-ant-oat01-abc","refreshToken":"x","expiresAt":1}}`
	var b credsBlob
	if err := json.Unmarshal([]byte(raw), &b); err != nil {
		t.Fatal(err)
	}
	if b.ClaudeAiOauth.AccessToken != "sk-ant-oat01-abc" {
		t.Fatalf("got %q", b.ClaudeAiOauth.AccessToken)
	}
}

// BURN_NOTIFY_CMD must run with the alert message exposed in $BURN_MSG so it can
// be forwarded to a webhook, Slack, or another agent's inbox.
func TestNotifyCmdReceivesMessage(t *testing.T) {
	out := filepath.Join(t.TempDir(), "msg")
	t.Setenv("BURN_NOTIFY_CMD", "printf '%s' \"$BURN_MSG\" > "+out)
	notify("reserve hit 80%")
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("notify-cmd did not run: %v", err)
	}
	if string(got) != "reserve hit 80%" {
		t.Fatalf("BURN_MSG = %q, want %q", got, "reserve hit 80%")
	}
}

// claudeArgs is the sole place the `claude` invocation is built — this locks in
// the base flags and, critically, that passthrough args land LAST so a
// user-supplied --model/--max-turns in the `--` passthrough wins over burn's own.
func TestClaudeArgs(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want []string
	}{
		{
			name: "base args",
			cfg:  Config{Model: "opus", MaxTurns: 5},
			want: []string{"-p", "go", "--output-format", "json", "--model", "opus", "--max-turns", "5"},
		},
		{
			name: "skip permissions appended",
			cfg:  Config{Model: "opus", MaxTurns: 5, SkipPermissions: true},
			want: []string{"-p", "go", "--output-format", "json", "--model", "opus", "--max-turns", "5", "--dangerously-skip-permissions"},
		},
		{
			name: "passthrough appended last, after skip-permissions",
			cfg: Config{Model: "opus", MaxTurns: 5, SkipPermissions: true,
				ClaudeArgs: []string{"--resume", "abc123", "--model", "sonnet"}},
			want: []string{"-p", "go", "--output-format", "json", "--model", "opus", "--max-turns", "5",
				"--dangerously-skip-permissions", "--resume", "abc123", "--model", "sonnet"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := claudeArgs(tc.cfg, "go")
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("claudeArgs() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestResumeInArgs(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{name: "absent", args: []string{"--mcp-config", "m.json"}, want: false},
		{name: "empty", args: nil, want: false},
		{name: "long form with value", args: []string{"--resume", "abc"}, want: true},
		{name: "short form", args: []string{"-r", "abc"}, want: true},
		{name: "long form equals", args: []string{"--resume=abc"}, want: true},
		{name: "substring must not match", args: []string{"--resumed"}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resumeInArgs(tc.args); got != tc.want {
				t.Fatalf("resumeInArgs(%v) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}
