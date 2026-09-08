# Testing

`go vet ./...` and `go test ./...` (wired into CI) cover unit-level logic —
env scrubbing, sandbox config validation, the volumes-file schema, the
entrypoint script. They do **not** exercise a real `osb`/Docker/claude
pipeline. The smoke test below does, and is the only thing that would have
caught the real bugs found by actually running `--sandbox` end to end:
`--mount` doesn't exist on `osb sandbox create` (must be `--volumes-file`, an
undocumented JSON schema), `sandbox create` needs `-o json` for a parseable
id (the default is a human-readable table), and `-e KEY=VALUE` puts the
value directly in this process's own argv — none of that surfaces from
reading osb's `--help` or its docs, only from running it.

## When to run this

**Required** before merging any change that touches `sandbox.go`, the
sandboxed branch of `runClaude` in `runner.go`, or `Dockerfile.sandbox`.
Optional (but recommended) for other changes that touch `main.go`'s flag
parsing or `setup.go`.

## 1. Host-mode regression (no external dependencies)

Always run this first — it must pass unmodified regardless of what changed:

```sh
go build -o burn . && go vet ./... && gofmt -l . && go test ./...
./burn --dry-run --goal test          # unaffected by any --sandbox change
./burn setup                          # sandbox checks are informational only, never [FAIL]
```

## 2. One-time local OpenSandbox setup

```sh
uv tool install opensandbox-cli                 # osb
uvx opensandbox-server init-config ~/.sandbox.toml --example docker
```

Edit `~/.sandbox.toml` before starting the server:

- Set `api_key` under `[server]` to a random local value (`openssl rand -hex 24`).
  Do **not** rely on `OPENSANDBOX_INSECURE_SERVER=YES` — treat it the way you'd
  treat any other auth bypass flag.
- Set `allowed_host_paths` under `[storage]` to include wherever your smoke-test
  repo will live (e.g. `["/tmp"]`). An empty list means **nothing** is allowed,
  not everything — the config file's own comment says the opposite; don't
  trust it.

```sh
uvx opensandbox-server            # foreground or backgrounded; binds 127.0.0.1:8080

osb config init
osb config set connection.domain localhost:8080
osb config set connection.protocol http
osb config set connection.api_key <the key you put in ~/.sandbox.toml>

just build-sandbox-image          # builds burn-sandbox:latest from Dockerfile.sandbox
docker run --rm burn-sandbox:latest sh -c "claude --version && git --version && gh --version"
```

## 3. The smoke test

This spends real subscription quota (that's what `burn` is for) — keep it
minimal. Use a throwaway git repo, never a real project.

```sh
mkdir -p /tmp/burn-smoke && cd /tmp/burn-smoke
git init -q && git config user.email t@t.com && git config user.name t
git commit --allow-empty -q -m init
cd -

./burn setup                       # confirm all "sandbox extra" lines are [ ok ]

./burn --sandbox --repo /tmp/burn-smoke --dry-run \
  --goal "create hello.txt containing: hello from burn sandbox"
# expect: "planned ... in /tmp/burn-smoke (sandboxed, mounted read-write at /workspace)"
# — an absolute path, even if --repo was given relative.

./burn --sandbox --repo /tmp/burn-smoke --jobs 2 --goal test
# expect: immediate refusal ("would race"), no sandbox created.

./burn --sandbox --repo /tmp --goal test
# expect: immediate refusal ("has no .git"), no sandbox created.

./burn --sandbox --repo /tmp/burn-smoke --max-turns 3 --dangerously-skip-permissions \
  --goal "create hello.txt containing exactly: hello from burn sandbox"
```

Verify, in order:

1. The run's own output shows `preflight: ... subscription metering confirmed`
   (first run in a 4h window) or `preflight: recent metering proof found`
   (subsequent runs) — either way, no error.
2. `cat /tmp/burn-smoke/hello.txt` shows the exact content on the **host** —
   proves the bind mount is real, not a container-local write.
3. `osb sandbox list` shows no sandboxes still running — proves cleanup ran.
4. `docker ps -a | grep burn-sandbox` shows nothing — same, via the other tool.
5. (Only if changing the token/env path) confirm no secret value appears in
   argv: run a longer sandboxed goal in one terminal and `ps auxww | grep osb`
   in another while it's in flight. You should see `osb command run` with a
   path argument (`sh /tmp/.burn-entrypoint.sh ...`), never a raw token.

## 4. Cleanup

```sh
pkill -f opensandbox_server   # stop the local server
rm -rf /tmp/burn-smoke
```

`~/.sandbox.toml` and `~/.opensandbox/config.toml` can stay — steps 2 onward
are instant to repeat on the next smoke test.

## 4. Jira source smoke test (`connect jira`, `process jira`)

**Required** before merging any change to `jira.go`, `connect.go`, or `process.go`.

Unit tests cover the pure parts (JQL building, mode resolution, prompt rendering, dedup, stop
conditions, digest formatting) against a fixture. What they cannot cover is the **`acli`
contract**: whether `--fields summary,status,labels` is accepted, and whether the JSON keys
`fields.status.name` / `fields.labels` actually come back on the site being queried. Only a
real `acli` proves that — the same reason `--sandbox` needs a smoke test.

Needs `acli` on PATH, `acli jira auth login` done, and a Jira project you may write to.

```sh
go build -o burn . && go vet ./... && gofmt -l . && go test ./...

# 1. the acli field contract: status and labels must be populated, not blank
./burn connect jira --project <KEY> --label <LABEL>
#    expect: KEY<tab>STATUS<tab>SUMMARY — a blank middle column means the
#    status field did not parse, and --mode auto would silently enrich everything

# 2. raw JQL still passes through untouched
./burn connect jira --jql 'project = <KEY> ORDER BY created DESC'

# 3. dry-run: query compiles, tickets are picked, prompt renders
./burn process jira --project <KEY> --label <LABEL> --mode auto --dry-run

# 4. one real enrich pass against ONE ticket, then check the comment in Jira
./burn process jira --project <KEY> --label <LABEL> --mode enrich --max-items 1 \
  --target 90 --dangerously-skip-permissions --digest /tmp/burn-digest.md

# 5. dedup: the same command must now skip that ticket
./burn process jira --project <KEY> --label <LABEL> --mode enrich --max-items 1 \
  --target 90 --dangerously-skip-permissions
#    expect: "no items to process (N matched, 1 already done)"

# 6. kill switch refuses to start, before the preflight probe is spent
touch ~/.claude/burn/STOP
./burn process jira --project <KEY> --label <LABEL> --mode enrich --redo \
  --target 90 --dangerously-skip-permissions
#    expect: "burn: stop file ... exists", exit 1, NO preflight, no sessions.
#    A stop file created mid-run instead stops before the next ticket and the
#    digest reads "stopped: stop file present" — check that by touching it
#    while a --max-items 2 run is on its first ticket.
rm ~/.claude/burn/STOP

# 7. per-item view of what ran
./burn overview --group item
```

An `--mode implement` pass is worth one manual run per change to the implement prompt: confirm
the PR it opens is a **draft**, on a `claude/<KEY>-*` branch, and that the default branch has no
new commits.
