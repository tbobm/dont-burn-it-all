package main

import (
	"reflect"
	"testing"
)

func TestResolveCommand(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantName string
		wantRest []string
	}{
		{"bare no args", nil, "run", nil},
		{"bare flags", []string{"--goal", "x"}, "run", []string{"--goal", "x"}},
		{"explicit run", []string{"run", "--goal", "x"}, "run", []string{"--goal", "x"}},
		{"setup", []string{"setup"}, "setup", []string{}},
		{"overview bare", []string{"overview"}, "overview", []string{}},
		{"overview with flags", []string{"overview", "--json"}, "overview", []string{"--json"}},
		{"connect jira", []string{"connect", "jira", "--jql", "q"}, "connect", []string{"jira", "--jql", "q"}},
		{"connect bare", []string{"connect"}, "connect", []string{}},
		{"short help", []string{"-h"}, "help", nil},
		{"long help", []string{"--help"}, "help", nil},
		{"help word", []string{"help"}, "help", nil},
		{"unknown command", []string{"bogus"}, "unknown", []string{"bogus"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotName, gotRest := resolveCommand(c.args)
			if gotName != c.wantName {
				t.Fatalf("name: got %q, want %q", gotName, c.wantName)
			}
			if !reflect.DeepEqual(gotRest, c.wantRest) {
				t.Fatalf("rest: got %#v, want %#v", gotRest, c.wantRest)
			}
		})
	}
}
