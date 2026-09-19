# Machinist: Current State

Analysis of the upstream control-plane foundation this project builds on.

| Property | Value |
| --- | --- |
| Upstream | <https://github.com/owainlewis/machinist> |
| Commit analysed | `7b08de0` — *refactor(examples): replace foreman and shepherd with simple commands (#488)* |
| Module | `github.com/mitkox/esf` |
| Toolchain | `go 1.26.6` |
| Licence | MIT |

Machinist's own `ARCHITECTURE.md` states its boundary precisely:

> Machinist owns process execution, not orchestration.

That single sentence is the reason it is a good foundation. This document records
what Machinist actually does today, so the factory layer can be added *beside* it
rather than by rewriting it.

---

## 1. How a job enters Machinist

There are three entry points.

**a. Direct CLI run.** `internal/cli/root.go` exposes `machinist run`, which maps
a configured command name to a resolved command and executes it locally:

```
internal/cli/root.go        cobra command definitions
internal/config/config.go   Config.ResolveCommand(name) (config.ResolveCommand, error)
```

**b. Managed submission.** A job is created through the control-plane HTTP API
and stored as a `jobs` row in SQLite. `internal/controlplane/server.go` mounts
the routes; the store owns all state transitions.

**c. Triggers.** `internal/triggers` plus `internal/controlplane/trigger_scheduler.go`
create jobs on a schedule. Three families are supported, all defined in
`config.toml` under `[triggers]`:

| Family | Struct | Fields |
| --- | --- | --- |
| GitHub | `config.GitHubTrigger` | `every`, `label`, `command`, `model` |
| Interval | `config.IntervalTrigger` | `every`, `repository`, `prompt`, `command` |
| Cron | `config.CronTrigger` | `schedule`, `timezone`, `repository`, `prompt` |

Trigger state is persisted (`trigger_state`, `github_trigger_requests`) so a
restart cannot duplicate an occurrence — the same durability instinct the factory
applies to whole runs.

> **Relevance to the factory.** Machinist already has the "something asked for
> work" concept. The factory reuses the *idea* — a recorded request with an
> identity and a prompt — but puts a durable workflow, not a lease queue, behind
> it.

## 2. How a job becomes a run

`internal/controlplane/store.go` defines the schema. Two tables matter:

```sql
CREATE TABLE IF NOT EXISTS jobs (
  id TEXT PRIMARY KEY, prompt TEXT NOT NULL, repository TEXT NOT NULL,
  command TEXT NOT NULL, trigger_identity TEXT NOT NULL DEFAULT '', ...,
  state TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);

CREATE TABLE IF NOT EXISTS runs (
  id TEXT PRIMARY KEY, job_id TEXT NOT NULL UNIQUE REFERENCES jobs(id),
  command TEXT NOT NULL, command_hash TEXT NOT NULL, executor TEXT NOT NULL,
  model TEXT NOT NULL DEFAULT '', repository TEXT NOT NULL,
  rendered_prompt TEXT NOT NULL, timeout_ms INTEGER NOT NULL, state TEXT NOT NULL,
  worker_instance TEXT, worker_name TEXT NOT NULL DEFAULT '',
  lease_token TEXT, lease_expires_at INTEGER, exit_code INTEGER, error TEXT,
  result TEXT, events TEXT, started_at TEXT, completed_at TEXT,
  duration_millis INTEGER, token_usage INTEGER);
```

`Store.CreateJob(ctx, prompt, repository, name, command)` inserts both rows.

The load-bearing constraint is the architecture document's guarantee:

> Each job has exactly one run. The database enforces this with a unique
> `runs.job_id`.

`runs.job_id` is `UNIQUE`. **There is no internal stage model, no DAG, and no
resumable checkpointing.** Terminal state comes only from the process result.

> **Relevance to the factory.** This is the constraint that makes Temporal
> necessary rather than optional. The factory needs *many* steps per request
> (sandbox, clone, agent, verify, collect, destroy) each independently retryable.
> Machinist deliberately does not model that; extending it into a DAG engine
> would fight its design. Temporal owns orchestration instead, and Machinist's
> `runs.job_id` uniqueness is left intact.

## 3. How workers lease work

`Store.Poll` (and the unexported `poll`) implement the lease protocol:

1. **Reclaim expired leases** — `DELETE FROM worker_repositories` for the
   instance, then `ReclaimExpiredLeases`.
2. **Resume own in-flight run** — a worker re-polling returns its own
   `state='running'` run rather than starting a second one:
   ```sql
   SELECT id,job_id,command,... FROM runs
    WHERE worker_instance=? AND state='running' LIMIT 1
   ```
3. **Admission control** — `SELECT COUNT(*) FROM jobs WHERE state='running'`
   compared with `max_concurrent_jobs`.
4. **Claim the oldest queued run**:
   ```sql
   SELECT r.id,r.job_id,... FROM runs r JOIN jobs j ON j.id=r.job_id
    WHERE r.state='queued' ORDER BY r.rowid
   ```
5. **Lease it with a compare-and-set**, which is what rejects a stale or
   duplicate claim:
   ```sql
   UPDATE runs SET state='running',worker_instance=?,worker_name=?,
     lease_token=?,lease_expires_at=?,started_at=?
    WHERE id=? AND state='queued'
   ```
   The `AND state='queued'` predicate is the race guard: two workers polling
   concurrently cannot both win, because the second `UPDATE` matches zero rows.
6. **Heartbeat** extends `lease_expires_at`; **Complete** rejects a completion
   whose `lease_token` no longer matches.

`internal/managedworker/worker.go` drives this: `poll` → `execute` →
`deliver`, with `withHeartbeats` wrapping both the execution and the delivery
phase so a long run cannot be reclaimed mid-flight.

## 4. How executor names are resolved

Executor resolution is a two-file, two-layer design — and it is already the
security boundary the factory needs.

`config.toml` (`internal/config/config.go`):

```toml
[commands.task-to-pr]
executor = "codex"
prompt_file = "prompts/task-to-pr.md"
timeout = "120m"
```

`worker.toml` — the worker owns which executables actually exist:

```go
type Worker struct {
    Name          string                `toml:"name"`
    DataDirectory string                `toml:"data_directory"`
    ControlPlane  ControlPlane          `toml:"control_plane"`
    Executors     map[string]Executor   `toml:"executors"`
    Repositories  map[string]Repository `toml:"repositories"`
}

type Executor struct {
    Command []string          `toml:"command"`
    Models  map[string]string `toml:"models"`
}
```

| Concept | Type | Notes |
| --- | --- | --- |
| Executor name → argv | `Executor.Command []string` | A fixed argument array, never a shell string |
| Model aliasing | `Executor.Models map[string]string` | Substituted for `{{machinist.model}}` |
| Timeout | `ResolvedCommand.Timeout time.Duration` | Parsed from the `timeout` string |
| Executor resolution | `Config.ResolveCommand(name)` | Command name → `ResolvedCommand` |
| Model resolution | `Worker.ResolveCommandModel(cmd, requested)` | Rejects an unknown executor |

`ResolvedCommand` is the value that crosses into the runner:

```go
type ResolvedCommand struct {
    Name, Executor string
    Command        []string
    Model          string
    Prompt         string
    Timeout        time.Duration
    Definition     string
    Hash           string
}
```

A control-plane caller supplies a **command name**; the worker decides what
executable that means. `internal/managedworker` resolves only worker-owned
executor and repository names.

> **Relevance to the factory.** The factory keeps this property exactly. A
> request names `agent_harness = "opencode2"`; the operator's configuration
> decides that this means a specific binary with a fixed argument vector. There
> is deliberately no "arbitrary command" path in either system.

## 5. How repositories are resolved

`runner.ResolveRepository(value string) (string, error)`
(`internal/runner/runner.go:364`) is the single function:

1. Empty input becomes `"."`.
2. `filepath.Abs` resolves it.
3. `os.Stat` requires a directory.
4. `git -C <path> rev-parse --show-toplevel` requires the path to be inside a Git
   worktree, and the **canonical worktree root** — not the caller's string — is
   what the runner uses.
5. `sanitizedEnvironment` strips every `GIT_*` variable
   (`GIT_DIR`, `GIT_WORK_TREE`, `GIT_CONFIG*`, `GIT_ALTERNATE_OBJECT_DIRECTORIES`,
   …) so an inherited environment cannot redirect Git at a different repository.

For managed submissions, `Worker.ResolveRepository(name)` resolves a logical name
against `worker.toml`'s `[repositories]` map.

> **Relevance to the factory.** Machinist resolves repositories **on the worker
> host**. The factory must resolve them **into a sandbox**, which is a different
> mechanism (clone or bundle inside the microVM). The *policy* instinct is
> reused: validate before executing, and never let untrusted input name a host
> path. `internal/repository` enforces that, and additionally rejects
> `file://` URLs and credentials embedded in URLs.

## 6. How stdin/stdout/stderr and artifacts work

`runner.Execute(ctx, Options) (Result, error)` (`internal/runner/runner.go:91`):

1. Resolve the repository (§5).
2. Create `<data_directory>/runs/<run_id>[/<artifact_key>]` durably.
3. Open `<runDir>/events.jsonl` as a bounded, append-only event log.
4. `exec.Command(executorCommand[0], executorCommand[1:]...)` with
   `Dir = repository`.
5. `command.Env = sanitizedEnvironment(os.Environ()) + MACHINIST_RUN_ID +
   MACHINIST_REPOSITORY + MACHINIST_TOKEN_USAGE_PATH`.
6. Three `os.Pipe` pairs for stdin/stdout/stderr.
7. **The prompt is written to stdin** by `writePrompt` — a goroutine that closes
   stdin when done, so the child sees EOF. This is the core "prompt transport"
   decision: prompts are never interpolated into a command line.
8. `pumpStream` copies each output stream to the caller's writer *and* to the
   event log.
9. `supervise` enforces one overall timeout and cancellation, then terminates the
   whole process tree (`terminateProcessTree`), not just the leader.

Result states are explicit and small:

```go
const (
    StateSucceeded State = "succeeded"
    StateFailed    State = "failed"
    StateCancelled State = "cancelled"
    StateTimedOut  State = "timed_out"
)
```

Artifact layout, from `Execute`:

```
<data_directory>/runs/<run_id>/[<artifact_key>/]
    events.jsonl        bounded, ordered run.started / process.started / stdout / stderr
    token_usage         written by the executor when it reports usage
```

Artifact keys are validated by `validArtifactKey`; run IDs by `validRunID`
(`run_` + 24 hex characters), which is what makes them safe path components.

Two optional structured-usage collectors (`internal/runner/codex_usage.go`)
recognise Codex and Claude invocations and extract token usage from their
structured output. This is the precedent for the factory's
`TokensIn`/`TokensOut`/`CostUSD` manifest fields.

> **Relevance to the factory.** The factory adopts both decisions — prompt on
> stdin, output captured to durable artifacts — but moves execution into a
> microVM and adds structured `argv` quoting, because CubeSandbox's data plane
> takes a command *string* rather than an `execve` argument vector.

## 7. The security boundary

From `SECURITY.md` and the code:

| Control | Implementation |
| --- | --- |
| Bind address | Control plane binds **loopback only** |
| CLI / worker auth | Shared bearer token (`worker_token_file`, `Server.WorkerToken()`) |
| Browser requests | Random per-process CSRF token |
| Remote control plane | A managed worker may reach a **non-loopback** control plane only over HTTPS (`managedworker.controlPlaneEndpoint`, `loopbackHost`) |
| Executables | Worker-owned `[executors]` argument arrays; callers never supply a command |
| Repositories | Worker-owned `[repositories]` map, plus worktree-root canonicalisation |
| Git environment | All `GIT_*` variables stripped before invoking Git |
| Prompts | Delivered on stdin; never interpolated into argv or a shell |
| Local data | `~/.machinist` holds prompts, output, tokens — protected by filesystem permissions |

What is **trusted**: command prompts, executor commands, repository mappings and
worker configuration — that is, *operator policy*.

What is **untrusted**: a submitted work prompt.

The documented limitation is explicit and important:

> Repository mappings constrain Machinist assignment and path resolution. They do
> not sandbox a command from other files or tools available to the worker OS
> user. […] Use OS permissions, repository permissions, and narrowly scoped
> credentials to enforce capability boundaries.

> **Relevance to the factory.** This is precisely the gap CubeSandbox closes. In
> Machinist a prompt runs with the worker user's full capability. In the factory
> the agent runs inside a microVM with its own kernel, filesystem and network
> namespace, so "the prompt cannot reach the host" becomes an enforced property
> rather than a documented caveat.

## 8. What the factory reuses, and what it does not

### Reused without modification

| Building block | Import path | Why it fits |
| --- | --- | --- |
| Module and toolchain | `github.com/mitkox/esf` | One module, one dependency graph, no fork of the build |
| Configuration conventions | `internal/config` | TOML, `DisallowUnknownFields`, relative-path resolution, bounded file reads |
| Event-log and artifact instincts | `internal/runner` (pattern) | Bounded append-only evidence, validated path components |
| Structured usage collection | `internal/runner/codex_usage.go` (pattern) | Precedent for per-harness token/cost accounting |
| Auth model | `internal/controlplane`, `SECURITY.md` | Loopback bind, bearer token, loopback-or-HTTPS rule |

Machinist's own packages are **unmodified**. Its full test suite still passes
(`go test ./...`), which is the regression guard for the "evolutionary, not
rewrite" requirement.

### Needs an adapter

| Need | Machinist today | Factory layer |
| --- | --- | --- |
| Execute somewhere other than the worker host | `runner.Execute` → local `exec.Command` | `internal/sandbox.Provider` → CubeSandbox microVM |
| More than one step per request | One process, one run, no stages | `SoftwareChangeWorkflow` (Temporal) |
| Independent verification | Terminal state *is* the process result | `internal/verification` — separate gates with separate exit codes |
| Repository acquisition | Host-local path | `internal/repository` — clone or bundle **into** the sandbox |
| Durable artifacts | `~/.machinist/runs/…` | `internal/artifacts` — run-scoped store, redaction on write |

### Deliberately unchanged

- `runs.job_id` uniqueness and the single-run-per-job invariant.
- No internal DAG, stage model, or checkpointing inside Machinist.
- The control plane's loopback-only bind and bearer-token auth.
- The `config.toml` / `worker.toml` split between portable command definitions
  and worker-owned executables.

The factory adds a **second, parallel** control plane rather than converting
Machinist's. Both can run in one process; neither owns the other.

## 9. What moves into the factory orchestration layer

| Concern | Owner after this work |
| --- | --- |
| Multi-step run lifecycle, retries, cancellation | Temporal + `internal/factory` |
| Sandbox lifecycle (create, exec, destroy) | `internal/sandbox` |
| Agent selection and invocation | `internal/agentharness` |
| Deterministic verification | `internal/verification` |
| Patch and evidence extraction | `internal/repository` + `internal/artifacts` |
| Run identity, states, manifest | `internal/factory` |
| Reproducible dev environment (Temporal) | `deployments/dev/temporal` |
| Operator CLI | `cmd/factory` |

Machinist retains ownership of what it already owns: single-process execution on
the worker host, its own job/run store, and its own web UI.

## 10. Existing tests in the analysed tree

| Package | Test files | Covers |
| --- | --- | --- |
| `internal/runner` | `runner_test.go`, `codex_usage_test.go`, `claude_usage_test.go`, `process_unix_test.go` | Streaming, prompt delivery, timeout/cancel, process-tree termination, artifact persistence, token usage |
| `internal/controlplane` | `server_test.go`, `store_test.go`, `trigger_scheduler_test.go`, `github_cli_test.go`, `github_cli_integration_test.go` | Job/run lifecycle, leasing, stale completions, HTTP auth, trigger scheduling |
| `internal/config` | `config_test.go`, `triggers_test.go` | Loading, defaults, unknown-field rejection, model aliases, prompt rendering |
| `internal/managedworker` | `worker_test.go` | Poll/execute/deliver, heartbeats |
| `internal/cli` | `root_test.go` | Command wiring |
| `internal/triggers` | `cron_test.go` | Cron parsing and occurrence calculation |
| `internal/updater` | `*_test.go` | Self-update plumbing |
| `cmd/machinist` | `main_test.go` | Entry point |

All of these still pass after the factory layer was added.
