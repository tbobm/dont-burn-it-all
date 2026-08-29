package main

import (
	"flag"
	"fmt"
)

// WorkItem is one item returned by a connected data source.
type WorkItem struct {
	Key     string
	Summary string
}

// Source lists work items matching a query string (source-specific syntax,
// e.g. JQL for Jira). One interface, one implementation today — sized so a
// second source is "implement this interface", not a rewrite.
type Source interface {
	ListItems(query string) ([]WorkItem, error)
}

// sources maps a `burn connect <name>` argument to its Source implementation.
var sources = map[string]Source{
	"jira": jiraSource{},
}

// cmdConnect implements `burn connect <source> --jql "..."`.
func cmdConnect(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: burn connect <source> [flags] (available: jira)")
	}
	name := args[0]
	src, ok := sources[name]
	if !ok {
		return fmt.Errorf("unknown source %q (available: jira)", name)
	}

	fs := flag.NewFlagSet("connect "+name, flag.ExitOnError)
	var jql string
	fs.StringVar(&jql, "jql", "", "JQL query to list matching issues (required)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if jql == "" {
		return fmt.Errorf("--jql is required")
	}

	items, err := src.ListItems(jql)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		fmt.Println("no matching issues")
		return nil
	}
	for _, it := range items {
		fmt.Printf("%s\t%s\n", it.Key, it.Summary)
	}
	return nil
}
