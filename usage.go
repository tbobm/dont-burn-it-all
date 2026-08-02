package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// usageEndpoint is the authoritative source for the real subscription 5-hour and
// weekly utilization — the same numbers Claude Code's /usage command shows.
const usageEndpoint = "https://api.anthropic.com/api/oauth/usage"

// minPollInterval is the safe polling cadence. The endpoint rate-limits hard
// (persistent 429) if hit faster or without the claude-code User-Agent.
const minPollInterval = 180 * time.Second

// Window is one rate-limit window (5-hour or 7-day).
type Window struct {
	Utilization float64 `json:"utilization"`
	ResetsAt    string  `json:"resets_at"`
}

// Usage is the parsed /api/oauth/usage response (only the fields we use).
type Usage struct {
	FiveHour Window `json:"five_hour"`
	SevenDay Window `json:"seven_day"`
}

// UsageClient reads the usage endpoint, caching results for minPollInterval so
// repeated reads within a window don't trip the endpoint's rate limiter.
type UsageClient struct {
	token  string
	source string
	http   *http.Client

	last   time.Time
	cached Usage
	hasCad bool
}

func newUsageClient() (*UsageClient, error) {
	token, source, err := resolveToken()
	if err != nil {
		return nil, err
	}
	return &UsageClient{
		token:  token,
		source: source,
		http:   &http.Client{Timeout: 20 * time.Second},
	}, nil
}

// Token returns the resolved subscription OAuth token.
func (c *UsageClient) Token() string { return c.token }

// Source describes where the token came from (for --dry-run).
func (c *UsageClient) Source() string { return c.source }

// Get returns cached usage if the last fetch is younger than minPollInterval,
// otherwise fetches fresh.
func (c *UsageClient) Get() (Usage, error) {
	if c.hasCad && time.Since(c.last) < minPollInterval {
		return c.cached, nil
	}
	return c.fetch()
}

// GetWaitingFresh blocks until at least minPollInterval has elapsed since the
// last fetch, then fetches. Used by the preflight metering proof, where we need
// the server to reflect a just-finished burst without risking a 429.
func (c *UsageClient) GetWaitingFresh() (Usage, error) {
	if c.hasCad {
		if wait := minPollInterval - time.Since(c.last); wait > 0 {
			time.Sleep(wait)
		}
	}
	return c.fetch()
}

func (c *UsageClient) fetch() (Usage, error) {
	req, err := http.NewRequest(http.MethodGet, usageEndpoint, nil)
	if err != nil {
		return Usage{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("User-Agent", "claude-code/1.0 (dont-burn-it-all)")
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return Usage{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	switch resp.StatusCode {
	case http.StatusOK:
		// ok
	case http.StatusUnauthorized:
		return Usage{}, fmt.Errorf("usage endpoint returned 401: OAuth token expired or invalid — run `claude` once to refresh it")
	case http.StatusTooManyRequests:
		return Usage{}, fmt.Errorf("usage endpoint returned 429: polled too fast — wait %s between reads", minPollInterval)
	default:
		return Usage{}, fmt.Errorf("usage endpoint returned %d: %s", resp.StatusCode, string(body))
	}

	var u Usage
	if err := json.Unmarshal(body, &u); err != nil {
		return Usage{}, fmt.Errorf("parsing usage response: %w (body: %s)", err, string(body))
	}
	c.cached, c.last, c.hasCad = u, time.Now(), true
	return u, nil
}

// resolveToken finds the subscription OAuth access token, in the same precedence
// order that guarantees subscription (not API) billing for headless runs.
func resolveToken() (token, source string, err error) {
	if t := os.Getenv("CLAUDE_CODE_OAUTH_TOKEN"); t != "" {
		return t, "env CLAUDE_CODE_OAUTH_TOKEN", nil
	}
	if t, err := tokenFromKeychain(); err == nil && t != "" {
		return t, "macOS Keychain (Claude Code-credentials)", nil
	}
	if t, path, err := tokenFromCredsFile(); err == nil && t != "" {
		return t, "file " + path, nil
	}
	return "", "", fmt.Errorf("no OAuth token found: set CLAUDE_CODE_OAUTH_TOKEN (via `claude setup-token`), or log in with `claude`")
}

// credsBlob is the JSON shape stored in the Keychain and ~/.claude/.credentials.json.
type credsBlob struct {
	ClaudeAiOauth struct {
		AccessToken string `json:"accessToken"`
	} `json:"claudeAiOauth"`
}

func tokenFromKeychain() (string, error) {
	out, err := exec.Command("security", "find-generic-password", "-s", "Claude Code-credentials", "-w").Output()
	if err != nil {
		return "", err
	}
	var b credsBlob
	if err := json.Unmarshal(out, &b); err != nil {
		return "", err
	}
	return b.ClaudeAiOauth.AccessToken, nil
}

func tokenFromCredsFile() (string, string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", err
	}
	path := filepath.Join(home, ".claude", ".credentials.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	var b credsBlob
	if err := json.Unmarshal(data, &b); err != nil {
		return "", "", err
	}
	return b.ClaudeAiOauth.AccessToken, path, nil
}
