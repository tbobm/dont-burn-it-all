package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// --- --sandbox: an opt-in extra ---------------------------------------------
//
// Nothing in this file runs, and none of its dependencies (docker, osb, a
// sandbox image) are required, unless the --sandbox flag is set. Like a Python
// package extra (e.g. airflow[mysql]), the base tool works with zero awareness
// of this file existing.
//
// A sandboxed session still goes through runClaude (runner.go) — the branch
// there swaps the exec target from the host `claude` binary to
// `osb command run <id> -o raw -- claude ...`. That means the preflight probe
// (runner.go:preflight) is sandboxed automatically, proving the *sandboxed*
// claude bills to the subscription, not just the host one — as long as the
// preflight stamp is mode-keyed (it is, see stampPath in main.go).
//
// The osb CLI details below (--volumes-file schema, env persisting across
// `command run` calls, `sandbox create` requiring -o json for a parseable id)
// are verified against a live local `osb`/opensandbox-server v0.1.1, not
// assumed from docs — the CLI's actual flags differ from its own --help text
// and from the upstream README in more than one place.

// sandboxMountPath is where cfg.Repo is bind-mounted inside the sandbox.
const sandboxMountPath = "/workspace"

// sandboxAWSMountPath is where ~/.aws is bind-mounted read-only when
// --aws-profile is set. Coupled to Dockerfile.sandbox's `useradd -m -u 1001 …
// worker` — the container's $HOME is /home/worker, not root's.
const sandboxAWSMountPath = "/home/worker/.aws"

// Paths written inside the sandbox by ensureSandbox — never referenced by
// value anywhere on the host, only by path.
const (
	sandboxOAuthTokenFile = "/tmp/.burn-oauth-token"
	sandboxGHTokenFile    = "/tmp/.burn-gh-token"
	sandboxEntrypointFile = "/tmp/.burn-entrypoint.sh"
)

// sandboxCommandTimeout bounds a single sandboxed `claude` invocation
// (including the preflight probe), enforced both client-side (context on the
// `osb` exec) and server-side (`command run -t`). Without this, a hung
// osb/claude process blocks cmd.Output() forever, runClaude never returns, and
// the deferred cleanup that would kill the sandbox never runs.
// ponytail: fixed value; make it a flag if sessions legitimately need longer.
const sandboxCommandTimeout = 60 * time.Minute

// activeSandboxes tracks every sandbox this process has created, independent
// of any single goroutine's defer — killAllSandboxes (called from the
// SIGINT/SIGTERM handler installed in main.go) uses this to clean up sandboxes
// even when a Ctrl-C skips the normal deferred cleanup in runClaude.
var activeSandboxes sync.Map // sandbox id (string) -> struct{}

// sandboxShuttingDown and sandboxCreatesInFlight close the race where a
// SIGINT/SIGTERM lands while `osb sandbox create` is still running: the id
// doesn't exist yet, so activeSandboxes can't contain it, and killAllSandboxes
// would miss it entirely. ensureSandbox registers itself in the WaitGroup
// before that blocking call and checks the flag right after — see
// shutdownSandboxes below.
var sandboxShuttingDown atomic.Bool
var sandboxCreatesInFlight sync.WaitGroup

// sandboxShutdownGrace bounds how long shutdownSandboxes waits for an
// in-flight `osb sandbox create` to finish and self-clean before giving up —
// `osb sandbox create` normally completes in seconds, not minutes.
const sandboxShutdownGrace = 30 * time.Second

// killAllSandboxes force-kills every sandbox this process created. Go does not
// run deferred functions on SIGINT/SIGTERM, so without this an interrupted
// --sandbox run leaves a container mounted read-write against the real repo.
func killAllSandboxes() {
	activeSandboxes.Range(func(k, _ any) bool {
		id, _ := k.(string)
		_ = exec.Command("osb", "sandbox", "kill", id).Run()
		activeSandboxes.Delete(id)
		return true
	})
}

// shutdownSandboxes is what the SIGINT/SIGTERM handler (main.go) calls before
// exiting. It kills every already-registered sandbox, then gives any sandbox
// still mid-creation a bounded window to finish, notice the shutdown flag, and
// kill itself (see ensureSandbox) before sweeping activeSandboxes once more.
func shutdownSandboxes() {
	sandboxShuttingDown.Store(true)
	killAllSandboxes()

	done := make(chan struct{})
	go func() {
		sandboxCreatesInFlight.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(sandboxShutdownGrace):
	}
	killAllSandboxes()
}

// validateSandboxConfig fills in defaults, resolves the repo to an absolute
// path (a relative --repo would validate fine via os.Stat here but break the
// bind-mount source osb receives, since that isn't resolved against burn's
// cwd), and checks the repo is real. Only called when cfg.Sandbox is set.
func validateSandboxConfig(cfg *Config) error {
	if cfg.Repo == "" {
		cfg.Repo = cfg.Workdir
	}
	abs, err := filepath.Abs(cfg.Repo)
	if err != nil {
		return fmt.Errorf("--sandbox: resolving --repo %q: %w", cfg.Repo, err)
	}
	cfg.Repo = abs
	// .git is a directory in a normal clone but a file in a worktree (it points
	// at the real gitdir) — existence is what matters, not IsDir.
	if _, err := os.Stat(filepath.Join(cfg.Repo, ".git")); err != nil {
		return fmt.Errorf("--sandbox requires --repo (or --workdir) to point at a git repo; %q has no .git", cfg.Repo)
	}
	if cfg.Jobs > 1 {
		// ponytail: v1 bind-mounts one repo read-write, which races across
		// concurrent sandboxes. Per-session `git clone --local` isolation
		// (see fireworker's internal/worktree) is the upgrade if this bites.
		return fmt.Errorf("--sandbox only supports --jobs 1 for now (mounting one repo read-write into %d sandboxes would race)", cfg.Jobs)
	}
	if cfg.AWSProfile != "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("--aws-profile: resolving home dir: %w", err)
		}
		if _, err := os.Stat(filepath.Join(home, ".aws")); err != nil {
			return fmt.Errorf("--aws-profile %q requires %s to exist on the host", cfg.AWSProfile, filepath.Join(home, ".aws"))
		}
	}
	return nil
}

// resolveAWSRegion finds a region to export into the sandbox, checking
// AWS_REGION then AWS_DEFAULT_REGION on the host. Empty return means none set
// — the sandboxed aws CLI falls back to whatever the mounted profile/config
// specifies.
func resolveAWSRegion() string {
	if v := os.Getenv("AWS_REGION"); v != "" {
		return v
	}
	return os.Getenv("AWS_DEFAULT_REGION")
}

// resolveGHToken finds a GitHub token to forward for `gh pr create` inside the
// sandbox. Empty return means no token available — sandboxed goals that need to
// open a PR will fail there, not here (burn stays goal-agnostic).
func resolveGHToken(cfg Config) string {
	if v := os.Getenv(cfg.GHTokenEnv); v != "" {
		return v
	}
	out, err := exec.Command("gh", "auth", "token").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// writeSandboxSecret writes content to path inside the sandbox via `osb file
// write`'s stdin form (the --content flag omitted) — confirmed live that this
// never puts the value in this host process's argv, unlike `sandbox create -e
// KEY=VALUE`, which does (osb has no --env-file or other non-argv form of -e).
func writeSandboxSecret(id, path, content string) error {
	cmd := exec.Command("osb", "file", "write", id, path, "--mode", "0600")
	cmd.Stdin = strings.NewReader(content)
	return cmd.Run()
}

// sandboxEntrypointScript builds the wrapper `command run` execs instead of
// `claude` directly: it exports the OAuth/GH tokens from the files
// writeSandboxSecret wrote, then hands off to claude with argv untouched. The
// script text itself has no secret values in it — only file paths and
// non-secret values (the configured GH env var name, an AWS profile/region
// name) — so writing it is safe by any transport, argv included. Kept pure
// (values in, no os.Getenv) so it stays table-testable on its output text.
func sandboxEntrypointScript(cfg Config, hasGHToken bool, awsProfile, awsRegion string) string {
	script := "#!/bin/sh\nset -e\n" +
		fmt.Sprintf("export CLAUDE_CODE_OAUTH_TOKEN=\"$(cat %s)\"\n", sandboxOAuthTokenFile)
	if hasGHToken {
		script += fmt.Sprintf("export %s=\"$(cat %s)\"\n", cfg.GHTokenEnv, sandboxGHTokenFile)
	}
	if awsProfile != "" {
		script += fmt.Sprintf("export AWS_PROFILE=%q\n", awsProfile)
		if awsRegion != "" {
			script += fmt.Sprintf("export AWS_REGION=%q\n", awsRegion)
		}
	}
	script += "exec claude \"$@\"\n"
	return script
}

// sandboxVolume is one entry of osb's --volumes-file JSON. osb has no --mount
// flag (unlike docker) — a bind mount is only expressible this way. Schema
// confirmed against a live server; osb's own --help does not document it.
type sandboxVolume struct {
	Name      string            `json:"name"`
	MountPath string            `json:"mountPath"`
	Host      sandboxHostVolume `json:"host"`
	ReadOnly  bool              `json:"readOnly,omitempty"`
}

type sandboxHostVolume struct {
	Path string `json:"path"`
}

// writeVolumesFile writes a --volumes-file describing the given volumes. The
// caller must remove the returned path once `osb sandbox create` has read it.
func writeVolumesFile(vols []sandboxVolume) (string, error) {
	data, err := json.Marshal(vols)
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp("", "burn-sandbox-volumes-*.json")
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// sandboxCreateResult is the subset of `osb sandbox create -o json` we need.
type sandboxCreateResult struct {
	ID string `json:"id"`
}

// ensureSandbox creates one local OpenSandbox for a single session: the
// configured image, the repo bind-mounted read-write at sandboxMountPath,
// and, when --aws-profile is set, ~/.aws bind-mounted read-only at
// sandboxAWSMountPath. No secret ever appears in an `osb sandbox
// create`/`command run` argument —
// tokens are written into the sandbox filesystem via stdin (writeSandboxSecret)
// and picked up by an entrypoint wrapper script (sandboxEntrypointScript),
// which runner.go execs instead of calling `claude` directly. Registers the id
// in activeSandboxes so an interrupted process can still kill it.
func ensureSandbox(cfg Config, token, ghToken string) (id string, cleanup func(), err error) {
	// Registered before the blocking create call below, so shutdownSandboxes
	// (SIGINT/SIGTERM path) knows a sandbox may exist server-side even before
	// we have an id to put in activeSandboxes.
	sandboxCreatesInFlight.Add(1)
	defer sandboxCreatesInFlight.Done()

	vols := []sandboxVolume{{
		Name:      "repo",
		MountPath: sandboxMountPath,
		Host:      sandboxHostVolume{Path: cfg.Repo},
	}}
	if cfg.AWSProfile != "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", nil, fmt.Errorf("--aws-profile: resolving home dir: %w", err)
		}
		vols = append(vols, sandboxVolume{
			Name:      "aws",
			MountPath: sandboxAWSMountPath,
			Host:      sandboxHostVolume{Path: filepath.Join(home, ".aws")},
			ReadOnly:  true,
		})
	}
	volFile, err := writeVolumesFile(vols)
	if err != nil {
		return "", nil, fmt.Errorf("sandbox volumes file: %w", err)
	}
	defer os.Remove(volFile)

	args := []string{
		"sandbox", "create",
		"--image", cfg.SandboxImage,
		"--volumes-file", volFile,
		"-o", "json", // default output is a human-readable table, not a bare id
	}
	cmd := exec.Command("osb", args...)
	out, err := cmd.Output()
	if err != nil {
		return "", nil, fmt.Errorf("osb sandbox create: %w", err)
	}
	var res sandboxCreateResult
	if jErr := json.Unmarshal(out, &res); jErr != nil {
		return "", nil, fmt.Errorf("osb sandbox create: parsing output: %w", jErr)
	}
	if res.ID == "" {
		return "", nil, fmt.Errorf("osb sandbox create: empty sandbox id")
	}
	id = res.ID
	activeSandboxes.Store(id, struct{}{})
	cleanup = func() {
		_ = exec.Command("osb", "sandbox", "kill", id).Run()
		activeSandboxes.Delete(id)
	}

	// A shutdown may have been requested while `osb sandbox create` above was
	// still in flight — activeSandboxes.Store just above closes most of that
	// race, but shutdownSandboxes' first killAllSandboxes sweep could already
	// have run before this Store. Check the flag and self-clean rather than
	// leaving a container mounted read-write against the real repo.
	if sandboxShuttingDown.Load() {
		cleanup()
		return "", nil, fmt.Errorf("sandbox creation aborted: shutdown in progress")
	}

	if err := writeSandboxSecret(id, sandboxOAuthTokenFile, token); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("writing sandbox oauth token: %w", err)
	}
	if ghToken != "" {
		if err := writeSandboxSecret(id, sandboxGHTokenFile, ghToken); err != nil {
			cleanup()
			return "", nil, fmt.Errorf("writing sandbox gh token: %w", err)
		}
	}
	entrypoint := exec.Command("osb", "file", "write", id, sandboxEntrypointFile, "--mode", "0700")
	entrypoint.Stdin = strings.NewReader(sandboxEntrypointScript(cfg, ghToken != "", cfg.AWSProfile, resolveAWSRegion()))
	if err := entrypoint.Run(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("writing sandbox entrypoint: %w", err)
	}

	return id, cleanup, nil
}
