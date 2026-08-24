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
