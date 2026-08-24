package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"
)

// GoalSummary aggregates all "session" records sharing one Goal (or, for
// Overview.Total, across all goals).
type GoalSummary struct {
	Goal            string  `json:"goal"`
	Sessions        int     `json:"sessions"`
	CostUSD         float64 `json:"cost_usd"`
	Turns           int     `json:"turns"`
	Errors          int     `json:"errors"`
	DurationSeconds float64 `json:"duration_seconds"`
	PartialDuration bool    `json:"partial_duration"`
	FirstRun        string  `json:"first_run"`
	LastRun         string  `json:"last_run"`
}

// Overview is the full `burn overview` result: one GoalSummary per distinct
// goal (sorted alphabetically), plus a Total row across all of them.
type Overview struct {
	Goals []GoalSummary `json:"goals"`
	Total GoalSummary   `json:"total"`
}

type goalAcc struct {
	sessions, turns, errors int
	cost, durationSec       float64
	partial                 bool
	first, last             time.Time
}

// aggregateByGoal groups "session"-kind records by Goal and computes
// per-goal totals, sorted alphabetically by goal, plus an overall Total row.
// "watch"-kind records are ignored — they track usage polling, not sessions.
// Sessions written before StartedAt existed count 0 duration and mark
// PartialDuration so the output doesn't silently under-report.
func aggregateByGoal(records []Record) Overview {
	byGoal := map[string]*goalAcc{}
	var order []string

	for _, r := range records {
		if r.Kind != "session" {
			continue
		}
		a, ok := byGoal[r.Goal]
		if !ok {
			a = &goalAcc{}
			byGoal[r.Goal] = a
			order = append(order, r.Goal)
		}
		a.sessions++
		a.cost += r.CostUSD
		a.turns += r.NumTurns
		if r.IsError {
			a.errors++
		}

		ts, tsErr := time.Parse(time.RFC3339, r.TS)
		if tsErr == nil {
			if a.first.IsZero() || ts.Before(a.first) {
				a.first = ts
			}
			if ts.After(a.last) {
				a.last = ts
			}
		}

		if r.StartedAt == "" {
			a.partial = true
			continue
		}
		started, err := time.Parse(time.RFC3339, r.StartedAt)
		if err != nil || tsErr != nil {
			a.partial = true
			continue
		}
		a.durationSec += ts.Sub(started).Seconds()
	}

	sort.Strings(order)

	ov := Overview{Goals: make([]GoalSummary, 0, len(order))}
	total := &goalAcc{}
	for _, goal := range order {
		a := byGoal[goal]
		ov.Goals = append(ov.Goals, toSummary(goal, a))

		total.sessions += a.sessions
		total.turns += a.turns
		total.errors += a.errors
		total.cost += a.cost
		total.durationSec += a.durationSec
		total.partial = total.partial || a.partial
		if total.first.IsZero() || (!a.first.IsZero() && a.first.Before(total.first)) {
			total.first = a.first
		}
		if a.last.After(total.last) {
			total.last = a.last
		}
	}
	ov.Total = toSummary("TOTAL", total)
	return ov
}

func toSummary(goal string, a *goalAcc) GoalSummary {
	return GoalSummary{
		Goal:            goal,
		Sessions:        a.sessions,
		CostUSD:         a.cost,
		Turns:           a.turns,
		Errors:          a.errors,
		DurationSeconds: a.durationSec,
		PartialDuration: a.partial,
		FirstRun:        formatTimeOrEmpty(a.first),
		LastRun:         formatTimeOrEmpty(a.last),
	}
}

func formatTimeOrEmpty(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

// loadRecords reads a JSONL store, skipping (and warning to stderr about)
// malformed lines rather than aborting the whole read. A missing file is not
// an error — it means burn has never run yet.
func loadRecords(path string) ([]Record, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var records []Record
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := sc.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		var r Record
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			fmt.Fprintf(os.Stderr, "burn: skipping malformed line %d in %s: %v\n", lineNo, path, err)
			continue
		}
		records = append(records, r)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return records, nil
}

// cmdOverview implements `burn overview`.
func cmdOverview(args []string) error {
	home, _ := os.UserHomeDir()
	fs := flag.NewFlagSet("overview", flag.ExitOnError)
	var storePath string
	var asJSON bool
	fs.StringVar(&storePath, "store", filepath.Join(home, ".claude", "burn", "worker.jsonl"), "JSONL log path")
	fs.BoolVar(&asJSON, "json", false, "emit JSON instead of a table")
	if err := fs.Parse(args); err != nil {
		return err
	}

	records, err := loadRecords(storePath)
	if err != nil {
		return fmt.Errorf("reading store %s: %w", storePath, err)
	}

	ov := aggregateByGoal(records)
	if len(ov.Goals) == 0 {
		fmt.Println("no burn activity recorded yet")
		return nil
	}

	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(ov)
	}
	printOverviewTable(ov)
	return nil
}

func printOverviewTable(ov Overview) {
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "GOAL\tSESSIONS\tCOST\tTURNS\tERRORS\tTIME SPENT\tFIRST RUN\tLAST RUN")
	for _, g := range ov.Goals {
		fmt.Fprintln(w, formatGoalRow(g))
	}
	fmt.Fprintln(w, formatGoalRow(ov.Total))
	w.Flush()
}

func formatGoalRow(g GoalSummary) string {
	goal := g.Goal
	if len(goal) > 48 {
		goal = goal[:45] + "..."
	}
	dur := time.Duration(g.DurationSeconds * float64(time.Second)).Round(time.Second).String()
	if g.PartialDuration {
		dur += " (partial)"
	}
	return fmt.Sprintf("%s\t%d\t$%.4f\t%d\t%d\t%s\t%s\t%s",
		goal, g.Sessions, g.CostUSD, g.Turns, g.Errors, dur, g.FirstRun, g.LastRun)
}
