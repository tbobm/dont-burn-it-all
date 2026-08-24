package main

import (
	"encoding/json"
	"fmt"
	"os/exec"
)

// jiraSource lists Jira issues via the `acli` CLI (Atlassian CLI), reusing
// whatever auth acli already has configured rather than burn managing its own
// Jira credentials.
type jiraSource struct{}

// acliIssue is the subset of `acli jira workitem search --json` output we
// use — verified against a real `acli jira workitem search --json` response.
type acliIssue struct {
	Key    string `json:"key"`
	Fields struct {
		Summary string `json:"summary"`
	} `json:"fields"`
}

func (jiraSource) ListItems(jql string) ([]WorkItem, error) {
	if _, err := exec.LookPath("acli"); err != nil {
		return nil, fmt.Errorf("`acli` not on PATH — install the Atlassian CLI and run `acli jira auth login` first")
	}
	out, err := exec.Command("acli", "jira", "workitem", "search",
		"--jql", jql, "--fields", "summary", "--json").Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("acli jira workitem search failed: %s", ee.Stderr)
		}
		return nil, fmt.Errorf("acli jira workitem search failed: %w", err)
	}
	return parseAcliSearchOutput(out)
}

// parseAcliSearchOutput parses `acli jira workitem search --json` output into
// WorkItems. Kept separate from ListItems so it's unit-testable without
// shelling out to acli.
func parseAcliSearchOutput(data []byte) ([]WorkItem, error) {
	var issues []acliIssue
	if err := json.Unmarshal(data, &issues); err != nil {
		return nil, fmt.Errorf("parsing acli output: %w", err)
	}
	items := make([]WorkItem, 0, len(issues))
	for _, is := range issues {
		items = append(items, WorkItem{Key: is.Key, Summary: is.Fields.Summary})
	}
	return items, nil
}
