package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
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
