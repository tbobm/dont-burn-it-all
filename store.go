package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// Record is one line in the JSONL log — either a finished session ("session")
// or a governor reading ("watch").
type Record struct {
	TS             string  `json:"ts"`
	Kind           string  `json:"kind"`
	SessionID      string  `json:"session_id,omitempty"`
	Goal           string  `json:"goal,omitempty"`
	Model          string  `json:"model,omitempty"`
	CostUSD        float64 `json:"cost_usd,omitempty"`
	NumTurns       int     `json:"num_turns,omitempty"`
	IsError        bool    `json:"is_error,omitempty"`
	FiveHourBefore float64 `json:"five_hour_before,omitempty"`
	FiveHourAfter  float64 `json:"five_hour_after,omitempty"`
	SevenDay       float64 `json:"seven_day,omitempty"`
}

// Store appends newline-delimited JSON records. Stdlib only, safe for concurrent
// writers within one process.
type Store struct {
	mu sync.Mutex
	f  *os.File
}

func openStore(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	return &Store{f: f}, nil
}

func (s *Store) Write(r Record) {
	if r.Kind == "" {
		r.Kind = "session"
	}
	line, err := json.Marshal(r)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.f.Write(line)
	s.f.Write([]byte("\n"))
}

func (s *Store) Close() error { return s.f.Close() }
