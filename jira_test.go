package main

import "testing"

func TestParseAcliSearchOutput(t *testing.T) {
	data := []byte(`[
  {
    "id": "48004",
    "key": "DEMO-101",
    "fields": {
      "summary": "Add a health check endpoint to the billing service",
      "status": {"name": "In Progress"}
    }
  },
  {
    "id": "48005",
    "key": "DEMO-102",
    "fields": {
      "summary": "Rotate the shared CI deploy token"
    }
  }
]`)

	items, err := parseAcliSearchOutput(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	if items[0].Key != "DEMO-101" || items[0].Summary != "Add a health check endpoint to the billing service" {
		t.Fatalf("unexpected first item: %+v", items[0])
	}
	if items[1].Key != "DEMO-102" || items[1].Summary != "Rotate the shared CI deploy token" {
		t.Fatalf("unexpected second item: %+v", items[1])
	}
}

func TestParseAcliSearchOutputEmpty(t *testing.T) {
	items, err := parseAcliSearchOutput([]byte(`[]`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected 0 items, got %d", len(items))
	}
}

func TestParseAcliSearchOutputInvalid(t *testing.T) {
	if _, err := parseAcliSearchOutput([]byte(`not json`)); err == nil {
		t.Fatal("expected an error for invalid JSON")
	}
}

func TestParseAcliSearchOutputStatusAndLabels(t *testing.T) {
	data := []byte(`[
  {
    "key": "DEMO-101",
    "fields": {
      "summary": "Add a health check endpoint",
      "status": {"name": "To Refine"},
      "labels": ["ops", "claude-ready"]
    }
  },
  {
    "key": "DEMO-102",
    "fields": {"summary": "No status or labels here"}
  }
]`)
	items, err := parseAcliSearchOutput(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if items[0].Status != "To Refine" {
		t.Fatalf("status: got %q", items[0].Status)
	}
	// Sorted so a prompt (and a test) sees a stable label order.
	if len(items[0].Labels) != 2 || items[0].Labels[0] != "claude-ready" || items[0].Labels[1] != "ops" {
		t.Fatalf("labels: got %#v, want sorted", items[0].Labels)
	}
	// A project without status/labels must parse, not error — those fields are
	// optional everywhere burn uses them.
	if items[1].Status != "" || len(items[1].Labels) != 0 {
		t.Fatalf("expected empty status/labels, got %+v", items[1])
	}
}

func TestJiraBuildQuery(t *testing.T) {
	cases := []struct {
		name string
		q    ItemQuery
		want string
	}{
		{
			"label only",
			ItemQuery{Label: "claude-ready"},
			`labels = "claude-ready" ORDER BY created ASC`,
		},
		{
			"project and label",
			ItemQuery{Project: "SUDS", Label: "claude-ready"},
			`project = "SUDS" AND labels = "claude-ready" ORDER BY created ASC`,
		},
		{
			"statuses are a list",
			ItemQuery{Project: "SUDS", Status: "To Refine, In Progress"},
			`project = "SUDS" AND status IN ("To Refine", "In Progress") ORDER BY created ASC`,
		},
		{
			"empty status entries are dropped",
			ItemQuery{Label: "l", Status: "New,,  ,Open"},
			`labels = "l" AND status IN ("New", "Open") ORDER BY created ASC`,
		},
		{
			"raw query passes through untouched",
			ItemQuery{Raw: "assignee = currentUser() ORDER BY priority DESC"},
			"assignee = currentUser() ORDER BY priority DESC",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := jiraSource{}.BuildQuery(c.q)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != c.want {
				t.Fatalf("got  %s\nwant %s", got, c.want)
			}
		})
	}
}

func TestJiraBuildQueryErrors(t *testing.T) {
	cases := []struct {
		name string
		q    ItemQuery
	}{
		{"nothing to select on", ItemQuery{}},
		{"status alone is not a selector", ItemQuery{Status: "New"}},
		{"raw plus shorthand is ambiguous", ItemQuery{Raw: "project = X", Label: "l"}},
		{"a quote would change the query's meaning", ItemQuery{Label: `l" OR labels = "x`}},
		{"a backslash could escape out", ItemQuery{Project: `SU\DS`}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := (jiraSource{}).BuildQuery(c.q); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestResolveQueryRequiresRawForSourcesWithoutABuilder(t *testing.T) {
	if _, err := resolveQuery(stubSource{}, ItemQuery{Label: "l"}); err == nil {
		t.Fatal("a source with no QueryBuilder cannot compile the shorthand")
	}
	got, err := resolveQuery(stubSource{}, ItemQuery{Raw: "anything"})
	if err != nil || got != "anything" {
		t.Fatalf("got %q, %v", got, err)
	}
}

// stubSource implements Source but not QueryBuilder.
type stubSource struct{}

func (stubSource) ListItems(string) ([]WorkItem, error) { return nil, nil }
