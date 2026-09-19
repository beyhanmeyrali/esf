# ADR 0003 — Agent harness abstraction

- **Status:** Accepted
- **Date:** 2026-09-19
- **Deciders:** Factory engineering

## Context

The factory must be able to change coding agents without changing the workflow,
and Phase 2 must run different agents (Codex, Claude Code, OpenCode) against
cloned candidates. Principle: *agents are replaceable, models are replaceable,
harnesses are replaceable*.

At the same time, an API caller must **never** be able to choose an executable.
Machinist already established this pattern with its `worker.toml`
`[executors]` map — a control-plane caller names a command; the worker decides
what that means. The factory must preserve that property.

The measured environment constrains the mechanism:

- The base Cube template has no `git` and no JavaScript runtime.
- The pinned `opencode2` is a 198 MB standalone glibc ELF, which runs inside a
  Debian 12 microVM without Node.
- `opencode2 run --format json` accepts the prompt on **stdin** when no message
  argument is given, and reads its model catalog from the network unless a cache
  is supplied.
- The model flag must appear **after** the `run` subcommand.

## Decision

Introduce `internal/agentharness` with:

```go
type Harness interface {
    Name() string
    Model() string
    Provision(ctx context.Context, sb sandbox.Sandbox) error
    Run(ctx context.Context, sb sandbox.Sandbox, task Task) (Result, error)
}
```

- A `Registry` resolves a harness by **name only**. There is deliberately no
  "arbitrary command" harness.
- `GenericCommandHarness` is the only concrete implementation, parameterised by
  an operator `Spec`: executable, fixed argument vector, model flag, timeout,
  environment allowlist, prompt transport and provisioning steps.
- `NewOpenCode` is a *configuration* of that generic harness, not a separate
  code path. OpenAI's Codex and Anthropic's Claude Code would be further
  configurations.

Two mechanisms deserve specific justification:

**Prompt transport.** The default is `stdin_file`: the factory writes the task
to a file inside the sandbox and redirects the agent's stdin from it. The
untrusted text therefore never appears in a command string at all. `argv`
transport also exists and is safe because the provider single-quotes every
argument, but it is bounded by `ARG_MAX`.

**Model flag placement.** `Spec.Args` contains the placeholder
`{{model_args}}`, expanded in place or dropped entirely when no model is set. A
fixed insertion point cannot express both `opencode2 run --model X` and
`agent --model X`; guessing produces a run that fails with a confusing CLI error
— which is exactly what happened before the placeholder was introduced.

**Provisioning** is split deliberately: the factory installs its **own**
prerequisites (`sandbox.base_packages`, e.g. `git`) before the harness runs,
because repository preparation needs `git` regardless of which agent is used.
Making that a harness detail left the factory unable to clone for any harness
that forgot to declare it — a bug the integration test caught.

## Alternatives considered

| Alternative | Rejected because |
| --- | --- |
| Let callers pass an executable + args | Directly violates the security principle. It is the exact capability Machinist refuses to expose. |
| One bespoke harness type per agent | Duplicates invocation, timeout, provisioning and capture logic for every agent. |
| Require a dedicated factory Cube template now | A deployment mutation. Provisioning at start-up is the least invasive MVP mechanism; the template is the production direction. |
| Install the agent from npm inside each sandbox | Slower and less reproducible than pushing a pinned binary; the envd files API transfers 189 MiB in ~0.5 s. |
| Drive agents over a protocol (ACP/MCP) instead of a CLI | Attractive long-term, but no stable non-interactive surface was verified for the agents available here. The interface leaves room for it. |

## Consequences

**Positive**

- Adding Codex or Claude Code is a configuration block plus, at most, a
  provisioning variant — no workflow change.
- The agent's executable, arguments and timeout are operator policy.
- Untrusted task text is delivered without interpolation.
- Harness failures are recorded with the harness name, command line, exit code,
  duration and attempt number.

**Negative / accepted**

- The generic harness must anticipate agent CLI shape via placeholders, which is
  slightly less ergonomic than passing argv directly.
- Provisioning a 198 MB binary per run costs ~0.5 s and ~190 MiB of sandbox disk
  (of ~1 GiB). A factory template removes this cost in Phase 2.
- The prompt-file path inside the sandbox is a fixed constant rather than
  configurable per harness.

## Security implications

- **A caller cannot name an executable.** Harness resolution is by registry
  name; an unregistered or path-like name is rejected.
- **Environment is allowlisted.** Only variables named in `pass_env` reach the
  agent; everything else on the host (CI variables, cloud credentials, proxy
  settings) is dropped.
- **Agent output is untrusted and redacted on write.** Registered secret values
  and credential-shaped patterns are scrubbed before evidence is persisted,
  because an agent can echo an environment variable or a curl command.
- **Provider credentials, when provisioned, are opt-in operator configuration**
  (`provider_files`) and are never derived from task text, never logged, and
  never written into the agent's config file — which references them by
  environment variable name instead.
- The richer alternative — an egress proxy that injects the credential header so
  it never enters the sandbox — is a Phase 2 item. The Cube egress proxy in this
  deployment already supports credential injection.

## Future implications

- Phase 2 "different harness per stage" becomes: register a `planner`, a
  `codex`, and a `reviewer` harness, and name them at different workflow stages.
- Per-harness token and cost fields (`TokensIn`, `TokensOut`, `CostUSD`) already
  exist on `Result` and in the manifest, so model comparison is a data question
  rather than a schema change.
- A future `Harness.Capabilities()` could declare whether an agent supports
  structured output, so the workflow can adapt without type switches.
