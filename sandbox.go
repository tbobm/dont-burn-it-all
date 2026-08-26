package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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

// sandboxCommandTimeout bounds a single sandboxed `claude` invocation
// (including the preflight probe). Without this, a hung osb/claude process
// blocks cmd.Output() forever, runClaude never returns, and the deferred
// cleanup that would kill the sandbox never runs.
// ponytail: fixed value; make it a flag if sessions legitimately need longer.
const sandboxCommandTimeout = 60 * time.Minute

// activeSandboxes tracks every sandbox this process has created, independent
// of any single goroutine's defer — killAllSandboxes (called from the
// SIGINT/SIGTERM handler installed in main.go) uses this to clean up sandboxes
// even when a Ctrl-C skips the normal deferred cleanup in runClaude.
var activeSandboxes sync.Map // sandbox id (string) -> struct{}

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

// validateSandboxConfig fills in defaults, resolves the repo to an absolute
// path (a relative --repo would validate fine via os.Stat here but break the
// --mount source osb receives, since that isn't resolved against burn's cwd),
// and checks the repo is real. Only called when cfg.Sandbox is set.
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
	return nil
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

// sandboxEnvFlags returns the `-e VARNAME` (name-only) flags that tell osb
// which vars to forward into the sandbox by name, not value. Used at both
// `sandbox create` and `command run` — osb's exact semantics for whether
// create-time forwarding persists across later execs aren't verified against
// this codebase, so both call sites request it explicitly rather than assume.
func sandboxEnvFlags(cfg Config, ghToken string) []string {
	flags := []string{"-e", "CLAUDE_CODE_OAUTH_TOKEN"}
	if ghToken != "" {
		flags = append(flags, "-e", cfg.GHTokenEnv)
	}
	return flags
}

// sandboxProcessEnv builds the env for the `osb` process itself. The values
// behind sandboxEnvFlags' name-only flags travel this way — via osb's own
// process env, same discipline as the host path's childEnv — never through
// argv, so the token is never `ps`-visible.
func sandboxProcessEnv(token, ghToken string, cfg Config) []string {
	env := childEnv(token)
	if ghToken != "" {
		env = append(env, cfg.GHTokenEnv+"="+ghToken)
	}
	return env
}

// ensureSandbox creates one local OpenSandbox for a single session: the
// configured image, the repo bind-mounted read-write at /workspace, and the
// allowlisted vars forwarded by name only. Registers the id in
// activeSandboxes so an interrupted process can still kill it.
func ensureSandbox(cfg Config, token, ghToken string) (id string, cleanup func(), err error) {
	args := append([]string{
		"sandbox", "create",
		"--image", cfg.SandboxImage,
		"--mount", fmt.Sprintf("%s:/workspace", cfg.Repo),
	}, sandboxEnvFlags(cfg, ghToken)...)

	cmd := exec.Command("osb", args...)
	cmd.Env = sandboxProcessEnv(token, ghToken, cfg)
	out, err := cmd.Output()
	if err != nil {
		return "", nil, fmt.Errorf("osb sandbox create: %w", err)
	}
	id = strings.TrimSpace(string(out))
	if id == "" {
		return "", nil, fmt.Errorf("osb sandbox create: empty sandbox id")
	}
	activeSandboxes.Store(id, struct{}{})
	cleanup = func() {
		_ = exec.Command("osb", "sandbox", "kill", id).Run()
		activeSandboxes.Delete(id)
	}
	return id, cleanup, nil
}
