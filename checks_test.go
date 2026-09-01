package main

import (
	"reflect"
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
