package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
// claude bills to the subscription, not just the host one.

// validateSandboxConfig fills in defaults and checks the repo is real. Only
// called when cfg.Sandbox is set.
func validateSandboxConfig(cfg *Config) error {
	if cfg.Repo == "" {
		cfg.Repo = cfg.Workdir
	}
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

// ensureSandbox creates one local OpenSandbox for a single session: the
// configured image, the repo bind-mounted read-write at /workspace, and only
// the allowlisted vars forwarded — by name only (`-e VARNAME`, no value in
// argv), so the token is never `ps`-visible. The actual values travel via the
// `osb` process's own env (childEnv), same discipline as the host path.
func ensureSandbox(cfg Config, token, ghToken string) (id string, cleanup func(), err error) {
	args := []string{
		"sandbox", "create",
		"--image", cfg.SandboxImage,
		"--mount", fmt.Sprintf("%s:/workspace", cfg.Repo),
		"-e", "CLAUDE_CODE_OAUTH_TOKEN",
	}
	env := childEnv(token)
	if ghToken != "" {
		args = append(args, "-e", cfg.GHTokenEnv)
		env = append(env, cfg.GHTokenEnv+"="+ghToken)
	}

	cmd := exec.Command("osb", args...)
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		return "", nil, fmt.Errorf("osb sandbox create: %w", err)
	}
	id = strings.TrimSpace(string(out))
	if id == "" {
		return "", nil, fmt.Errorf("osb sandbox create: empty sandbox id")
	}
	cleanup = func() {
		_ = exec.Command("osb", "sandbox", "kill", id).Run()
	}
	return id, cleanup, nil
}
