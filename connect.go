package main

import (
	"flag"
	"fmt"
	"strings"
)

// WorkItem is one item returned by a connected data source.
type WorkItem struct {
	Key     string
	Summary string
	Status  string
	Labels  []string
}

// ItemQuery is burn's source-agnostic way to say "these items": either the
// shorthand fields, or a raw query in the source's own language. A source
// compiles it via QueryBuilder.
type ItemQuery struct {
	Project string
	Label   string
	Status  string // comma-separated list of status names
	Raw     string
}

// Source lists work items matching a query string (source-specific syntax,
// e.g. JQL for Jira). One interface, one implementation today — sized so a
// second source is "implement this interface", not a rewrite.
type Source interface {
	ListItems(query string) ([]WorkItem, error)
}

// QueryBuilder is the optional half of a Source: it compiles burn's generic
// ItemQuery into that source's query language. A source that cannot do this
// still works — callers then require a raw query.
type QueryBuilder interface {
	BuildQuery(q ItemQuery) (string, error)
}

// sources maps a `burn connect <name>` / `burn process <name>` argument to its
// Source implementation.
var sources = map[string]Source{
	"jira": jiraSource{},
}

// sourceNames lists the registered sources for error messages.
func sourceNames() string {
	names := make([]string, 0, len(sources))
	for n := range sources {
		names = append(names, n)
	}
	return strings.Join(names, ", ")
}

// lookupSource resolves a source name, with a consistent error for both
// `connect` and `process`.
func lookupSource(name string) (Source, error) {
	src, ok := sources[name]
	if !ok {
		return nil, fmt.Errorf("unknown source %q (available: %s)", name, sourceNames())
	}
	return src, nil
}

// registerQueryFlags wires the shared query flags used by both `burn connect`
// and `burn process`, so the two cannot drift. --jql is kept as a
// backwards-compatible alias of --query for `burn connect jira`.
func registerQueryFlags(fs *flag.FlagSet, q *ItemQuery, withJQLAlias bool) {
	fs.StringVar(&q.Project, "project", "", "project key to pick items from (compiled into a source query)")
	fs.StringVar(&q.Label, "label", "", "label items must carry (compiled into a source query)")
	fs.StringVar(&q.Status, "status", "", "comma-separated status names to include (compiled into a source query)")
	fs.StringVar(&q.Raw, "query", "", "raw source query (JQL for jira); mutually exclusive with the shorthand above")
	if withJQLAlias {
		fs.StringVar(&q.Raw, "jql", "", "alias of --query for the jira source")
	}
}

// resolveQuery turns an ItemQuery into the source's own query string.
func resolveQuery(src Source, q ItemQuery) (string, error) {
	qb, ok := src.(QueryBuilder)
	if !ok {
		if q.Raw == "" {
			return "", fmt.Errorf("this source has no --project/--label/--status shorthand; pass a raw --query")
		}
		return q.Raw, nil
	}
	return qb.BuildQuery(q)
}

// cmdConnect implements `burn connect <source> [--query|--project|--label|--status]`.
func cmdConnect(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: burn connect <source> [flags] (available: %s)", sourceNames())
	}
	name := args[0]
	src, err := lookupSource(name)
	if err != nil {
		return err
	}

	fs := flag.NewFlagSet("connect "+name, flag.ExitOnError)
	var q ItemQuery
	registerQueryFlags(fs, &q, name == "jira")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	query, err := resolveQuery(src, q)
	if err != nil {
		return err
	}
	fmt.Printf("query: %s\n", query)

	items, err := src.ListItems(query)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		fmt.Println("no matching issues")
		return nil
	}
	for _, it := range items {
		fmt.Printf("%s\t%s\t%s\n", it.Key, it.Status, it.Summary)
	}
	return nil
}
