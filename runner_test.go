package main

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

var uuidV4Pattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestNewSessionIDIsAValidUUIDv4(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		id, err := newSessionID()
		if err != nil {
			t.Fatalf("newSessionID: %v", err)
		}
		if !uuidV4Pattern.MatchString(id) {
			t.Fatalf("newSessionID() = %q, not a valid UUIDv4", id)
		}
		if seen[id] {
			t.Fatalf("newSessionID() returned a duplicate: %q", id)
		}
		seen[id] = true
	}
}

func TestCountAssistantTurns(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	lines := []string{
		`{"type":"system"}`,
		`{"type":"user"}`,
		`{"type":"assistant"}`,
		`{"type":"user"}`,
		`{"type":"assistant"}`,
		``, // blank lines are skipped, not counted or errored on
		`{"type":"assistant"}`,
	}
	content := ""
	for _, l := range lines {
		content += l + "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	turns, err := countAssistantTurns(path)
	if err != nil {
		t.Fatalf("countAssistantTurns: %v", err)
	}
	if turns != 3 {
		t.Errorf("countAssistantTurns() = %d, want 3", turns)
	}
}

func TestCountAssistantTurnsMissingFile(t *testing.T) {
	if _, err := countAssistantTurns("/nonexistent/path/session.jsonl"); err == nil {
		t.Error("countAssistantTurns() on a missing file: want error, got nil")
	}
}

func TestFindTranscript(t *testing.T) {
	home := t.TempDir()
	projectDir := filepath.Join(home, ".claude", "projects", "-some-encoded-cwd")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sessionID := "11111111-2222-4333-8444-555555555555"
	transcript := filepath.Join(projectDir, sessionID+".jsonl")
	if err := os.WriteFile(transcript, []byte(`{"type":"assistant"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := findTranscript(home, sessionID)
	if err != nil {
		t.Fatalf("findTranscript: %v", err)
	}
	if got != transcript {
		t.Errorf("findTranscript() = %q, want %q", got, transcript)
	}
}

func TestFindTranscriptNotFound(t *testing.T) {
	home := t.TempDir()
	if _, err := findTranscript(home, "no-such-session-id"); err == nil {
		t.Error("findTranscript() for an unknown session: want error, got nil")
	}
}
