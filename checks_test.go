package main

import (
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

func TestParseGHChecks(t *testing.T) {
	raw := `[{"name":"spacelift/prod","state":"COMPLETED","bucket":"pass","link":"https://x/1"},
	         {"name":"ci/lint","state":"COMPLETED","bucket":"fail","link":"https://x/2"}]`
	got, err := parseGHChecks([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	want := []ghCheck{
		{Name: "spacelift/prod", State: "COMPLETED", Bucket: "pass", Link: "https://x/1"},
		{Name: "ci/lint", State: "COMPLETED", Bucket: "fail", Link: "https://x/2"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseGHChecks() = %+v, want %+v", got, want)
	}
}

func TestParseGHChecksInvalidJSON(t *testing.T) {
	if _, err := parseGHChecks([]byte("not json")); err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

// waitForCheckDir must match runClaude's actual exec directory (runner.go):
// host-mode sessions always run in cfg.Workdir (cmd.Dir there is never
// cfg.Repo), so a host-mode wait must watch cfg.Workdir, not cfg.Repo.
func TestWaitForCheckDir(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want string
	}{
		{name: "sandbox uses repo", cfg: Config{Sandbox: true, Repo: "/repo", Workdir: "/scratch"}, want: "/repo"},
		{name: "host uses workdir even if repo set", cfg: Config{Sandbox: false, Repo: "/repo", Workdir: "/scratch"}, want: "/scratch"},
		{name: "host, no repo set", cfg: Config{Sandbox: false, Workdir: "/scratch"}, want: "/scratch"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := waitForCheckDir(tc.cfg); got != tc.want {
				t.Fatalf("waitForCheckDir() = %q, want %q", got, tc.want)
			}
		})
	}
}

// classifyFetchResult is what used to be fetchChecks' silent `out, _ :=
// cmd.Output()`: a real gh failure (expired auth, deleted PR) must surface
// its stderr, while a gh exit code that still printed valid JSON (pending or
// failed check, expected mid-poll) must not be treated as an error at all.
func TestClassifyFetchResult(t *testing.T) {
	t.Run("valid JSON despite a non-nil exec error is not an error", func(t *testing.T) {
		out := []byte(`[{"name":"spacelift/prod","state":"COMPLETED","bucket":"fail","link":"https://x/1"}]`)
		checks, err := classifyFetchResult(out, errors.New("exit status 1"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(checks) != 1 {
			t.Fatalf("got %d checks, want 1", len(checks))
		}
	})

	t.Run("unparseable stdout with a real gh failure surfaces stderr", func(t *testing.T) {
		cmd := exec.Command("sh", "-c", "echo 'gh: authentication failed' 1>&2; exit 4")
		_, runErr := cmd.Output()
		if runErr == nil {
			t.Fatal("expected the helper command to fail")
		}
		_, err := classifyFetchResult([]byte(""), runErr)
		if err == nil {
			t.Fatal("expected an error for unparseable stdout")
		}
		if !strings.Contains(err.Error(), "authentication failed") {
			t.Fatalf("error %q does not surface gh's stderr", err)
		}
	})

	t.Run("unparseable stdout with no exec error still reports a failure", func(t *testing.T) {
		if _, err := classifyFetchResult([]byte("not json"), nil); err == nil {
			t.Fatal("expected a parse failure error")
		}
	})
}

func TestClassifyChecks(t *testing.T) {
	cases := []struct {
		name        string
		checks      []ghCheck
		pattern     string
		wantMatched int
		wantPending bool
		wantFailed  bool
	}{
		{
			name:        "no match keeps polling",
			checks:      []ghCheck{{Name: "ci/lint", Bucket: "pass"}},
			pattern:     "spacelift",
			wantMatched: 0,
		},
		{
			name: "matched, all pass",
			checks: []ghCheck{
				{Name: "spacelift/prod", Bucket: "pass"},
				{Name: "spacelift/staging", Bucket: "skipping"},
				{Name: "ci/lint", Bucket: "fail"}, // unmatched, must not affect outcome
			},
			pattern:     "spacelift",
			wantMatched: 2,
		},
		{
			name:        "matched, still pending",
			checks:      []ghCheck{{Name: "spacelift/prod", Bucket: "pending"}},
			pattern:     "spacelift",
			wantMatched: 1,
			wantPending: true,
		},
		{
			name: "matched, one failed",
			checks: []ghCheck{
				{Name: "spacelift/prod", Bucket: "pass"},
				{Name: "spacelift/staging", Bucket: "fail"},
			},
			pattern:     "spacelift",
			wantMatched: 2,
			wantFailed:  true,
		},
		{
			name:        "cancel counts as failed",
			checks:      []ghCheck{{Name: "spacelift/prod", Bucket: "cancel"}},
			pattern:     "spacelift",
			wantMatched: 1,
			wantFailed:  true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			matched, pending, failed := classifyChecks(tc.checks, tc.pattern)
			if len(matched) != tc.wantMatched {
				t.Fatalf("matched = %d, want %d", len(matched), tc.wantMatched)
			}
			if pending != tc.wantPending {
				t.Fatalf("pending = %v, want %v", pending, tc.wantPending)
			}
			if failed != tc.wantFailed {
				t.Fatalf("failed = %v, want %v", failed, tc.wantFailed)
			}
		})
	}
}
