package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAggregateByGoal(t *testing.T) {
	records := []Record{
		{Kind: "session", Goal: "write tests", TS: "2026-08-18T10:00:30Z", StartedAt: "2026-08-18T10:00:00Z", CostUSD: 0.10, NumTurns: 5},
		{Kind: "session", Goal: "write tests", TS: "2026-08-18T10:05:20Z", StartedAt: "2026-08-18T10:05:00Z", CostUSD: 0.20, NumTurns: 8, IsError: true},
		{Kind: "session", Goal: "refactor pkg", TS: "2026-08-18T11:00:10Z", StartedAt: "2026-08-18T11:00:00Z", CostUSD: 0.05, NumTurns: 2},
		{Kind: "session", Goal: "refactor pkg", TS: "2026-08-18T12:00:00Z", CostUSD: 0.05, NumTurns: 3}, // pre-StartedAt record
		{Kind: "watch", FiveHourBefore: 42},                                                             // must be ignored
	}

	ov := aggregateByGoal(records)

	if len(ov.Goals) != 2 {
		t.Fatalf("expected 2 goal groups, got %d: %+v", len(ov.Goals), ov.Goals)
	}

	// sorted alphabetically: "refactor pkg" before "write tests"
	refactor := ov.Goals[0]
	if refactor.Goal != "refactor pkg" {
		t.Fatalf("expected first group 'refactor pkg', got %q", refactor.Goal)
	}
	if refactor.Sessions != 2 || refactor.Errors != 0 {
		t.Fatalf("refactor pkg: got sessions=%d errors=%d", refactor.Sessions, refactor.Errors)
	}
	if !refactor.PartialDuration {
		t.Fatal("refactor pkg: expected PartialDuration=true (one record has no StartedAt)")
	}
	if refactor.DurationSeconds != 10 {
		t.Fatalf("refactor pkg: expected 10s duration from the one timed record, got %v", refactor.DurationSeconds)
	}

	write := ov.Goals[1]
	if write.Goal != "write tests" {
		t.Fatalf("expected second group 'write tests', got %q", write.Goal)
	}
	if write.Sessions != 2 || write.Errors != 1 {
		t.Fatalf("write tests: got sessions=%d errors=%d", write.Sessions, write.Errors)
	}
	if write.PartialDuration {
		t.Fatal("write tests: expected PartialDuration=false, both records have StartedAt")
	}
	if write.DurationSeconds != 50 {
		t.Fatalf("write tests: expected 30s+20s=50s duration, got %v", write.DurationSeconds)
	}
	if diff := write.CostUSD - 0.30; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("write tests: expected cost 0.30, got %v", write.CostUSD)
	}

	if ov.Total.Sessions != 4 {
		t.Fatalf("total: expected 4 sessions, got %d", ov.Total.Sessions)
	}
	if ov.Total.Errors != 1 {
		t.Fatalf("total: expected 1 error, got %d", ov.Total.Errors)
	}
	if !ov.Total.PartialDuration {
		t.Fatal("total: expected PartialDuration=true (propagated from refactor pkg)")
	}
}

func TestAggregateByGoalIgnoresNegativeDuration(t *testing.T) {
	records := []Record{
		// StartedAt after TS (clock skew / bad data): must not go negative.
		{Kind: "session", Goal: "skewed", TS: "2026-08-18T10:00:00Z", StartedAt: "2026-08-18T10:05:00Z", CostUSD: 0.10, NumTurns: 1},
		{Kind: "session", Goal: "skewed", TS: "2026-08-18T11:00:10Z", StartedAt: "2026-08-18T11:00:00Z", CostUSD: 0.10, NumTurns: 1},
	}

	ov := aggregateByGoal(records)

	if len(ov.Goals) != 1 {
		t.Fatalf("expected 1 goal group, got %d", len(ov.Goals))
	}
	skewed := ov.Goals[0]
	if skewed.Sessions != 2 {
		t.Fatalf("expected 2 sessions, got %d", skewed.Sessions)
	}
	if !skewed.PartialDuration {
		t.Fatal("expected PartialDuration=true (one record has a negative duration)")
	}
	if skewed.DurationSeconds != 10 {
		t.Fatalf("expected only the valid 10s record counted, got %v", skewed.DurationSeconds)
	}
}

func TestAggregateByGoalEmpty(t *testing.T) {
	ov := aggregateByGoal(nil)
	if len(ov.Goals) != 0 {
		t.Fatalf("expected 0 goal groups, got %d", len(ov.Goals))
	}
	if ov.Total.Sessions != 0 {
		t.Fatalf("expected 0 total sessions, got %d", ov.Total.Sessions)
	}
}

func TestLoadRecordsMissingFile(t *testing.T) {
	records, err := loadRecords("/nonexistent/path/worker.jsonl")
	if err != nil {
		t.Fatalf("expected no error for a missing store file, got %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("expected no records, got %d", len(records))
	}
}

func TestLoadRecordsSkipsMalformedLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker.jsonl")
	content := "{\"kind\":\"session\",\"goal\":\"a\",\"ts\":\"2026-08-18T10:00:00Z\"}\n" +
		"not valid json\n" +
		"{\"kind\":\"session\",\"goal\":\"b\",\"ts\":\"2026-08-18T11:00:00Z\"}\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	records, err := loadRecords(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("expected 2 valid records (malformed line skipped), got %d", len(records))
	}
}
