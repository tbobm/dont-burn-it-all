package main

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

// jiraSource lists Jira issues via the `acli` CLI (Atlassian CLI), reusing
// whatever auth acli already has configured rather than burn managing its own
// Jira credentials.
type jiraSource struct{}

// acliSearchFields is the --fields list requested from acli. summary is what
// the listing shows; status drives `--mode auto` (see resolveMode in
// process.go); labels are informational for the prompt. The issue *body* is
// deliberately not requested — its wire shape varies (plain text vs ADF) and
// the session reads it directly with `acli jira workitem view <key>`.
const acliSearchFields = "summary,status,labels"

// acliIssue is the subset of `acli jira workitem search --json` output we
// use. key and fields.summary are verified against a real response; status
// and labels use the stable Jira REST field shapes and are absent-tolerant
// (a project without them yields empty values, not an error).
type acliIssue struct {
	Key    string `json:"key"`
	Fields struct {
		Summary string `json:"summary"`
		Status  struct {
			Name string `json:"name"`
		} `json:"status"`
		Labels []string `json:"labels"`
	} `json:"fields"`
}

func (jiraSource) ListItems(jql string) ([]WorkItem, error) {
	if _, err := exec.LookPath("acli"); err != nil {
		return nil, fmt.Errorf("`acli` not on PATH — install the Atlassian CLI and run `acli jira auth login` first")
	}
	out, err := exec.Command("acli", "jira", "workitem", "search",
		"--jql", jql, "--fields", acliSearchFields, "--json").Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("acli jira workitem search failed: %s", ee.Stderr)
		}
		return nil, fmt.Errorf("acli jira workitem search failed: %w", err)
	}
	return parseAcliSearchOutput(out)
}

// BuildQuery compiles burn's generic --project/--label/--status shorthand into
// JQL, so the common "pick my labelled tickets" case needs no JQL knowledge.
// Ordering is `created ASC` (oldest first): unlike `priority`, `created`
// exists in every Jira project, so the pick order is deterministic everywhere.
func (jiraSource) BuildQuery(q ItemQuery) (string, error) {
	if q.Raw != "" {
		if q.Project != "" || q.Label != "" || q.Status != "" {
			return "", fmt.Errorf("use --query OR the --project/--label/--status shorthand, not both")
		}
		return q.Raw, nil
	}
	if q.Project == "" && q.Label == "" {
		return "", fmt.Errorf("need at least one of --project or --label (or a raw --query)")
	}

	var clauses []string
	if q.Project != "" {
		v, err := jqlValue("--project", q.Project)
		if err != nil {
			return "", err
		}
		clauses = append(clauses, "project = "+v)
	}
	if q.Label != "" {
		v, err := jqlValue("--label", q.Label)
		if err != nil {
			return "", err
		}
		clauses = append(clauses, "labels = "+v)
	}
	if q.Status != "" {
		var quoted []string
		for _, s := range strings.Split(q.Status, ",") {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			v, err := jqlValue("--status", s)
			if err != nil {
				return "", err
			}
			quoted = append(quoted, v)
		}
		if len(quoted) > 0 {
			clauses = append(clauses, "status IN ("+strings.Join(quoted, ", ")+")")
		}
	}
	return strings.Join(clauses, " AND ") + " ORDER BY created ASC", nil
}

// jqlValue quotes a shorthand value for JQL. Quotes and backslashes are
// rejected rather than escaped: no legitimate project key, label, or status
// name contains them, and refusing beats silently building a query that means
// something other than what the user typed.
func jqlValue(flag, v string) (string, error) {
	if strings.ContainsAny(v, `"\`) {
		return "", fmt.Errorf(`%s value %q contains a quote or backslash — use --query for a hand-written JQL query`, flag, v)
	}
	return `"` + v + `"`, nil
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
		labels := append([]string(nil), is.Fields.Labels...)
		sort.Strings(labels)
		items = append(items, WorkItem{
			Key:     is.Key,
			Summary: is.Fields.Summary,
			Status:  is.Fields.Status.Name,
			Labels:  labels,
		})
	}
	return items, nil
}
