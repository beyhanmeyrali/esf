# ADR 0006 — Control-plane security boundary

- **Status:** Accepted
- **Date:** 2026-09-19
- **Deciders:** Factory engineering

## Context

The factory accepts requests that cause code to be written, built and tested. It
must be explicit about who controls what, because the two sides have
irreconcilable trust levels:

- **Operator policy** is trusted: which repositories, harnesses, templates,
  verification profiles, resource limits, credentials and egress rules exist.
- **Task input** is untrusted: the task text, a requested approved repository,
  and a requested revision.
- **Repository content** is untrusted: the agent runs inside the repository, and
  the repository's build and test gates are executed.

The design principles state the non-negotiables: *the control plane must never
expose arbitrary host shell execution to API clients*; *credentials must remain
outside model prompts whenever possible*; *security boundaries must exist from
the first implementation*.

Machinist's own `SECURITY.md` provides the template — loopback binding, bearer
tokens, an executor allowlist — and also documents the gap this design must
close: in Machinist a prompt runs with the worker user's full capability.

## Decision

Draw the boundary at four explicit points, and make each one structural rather
than advisory.

### 1. What a caller may control

| May control | May **not** control |
| --- | --- |
| Task text | A host executable or argument vector |
| An approved repository URL, or a local fixture path | An arbitrary host path, or a `file://` URL |
| An exact revision | A host environment variable |
| A harness **name** | A sandbox template id from a foreign source |
| A verification **profile name** | A shell string as a verification command |
| A model name (optional) | Credentials, secrets, Cube management access |

### 2. Enforcement points

| Boundary | Where | Mechanism |
| --- | --- | --- |
| Executable selection | `agentharness.Registry` | Resolve by name only; unregistered and path-like names are rejected |
| Repository access | `repository.Request.validate` | Prefix allowlist; `https`/`ssh`/`git` only; no `file://`; no embedded credentials; non-empty, non-option-like revision |
| Verification | `verification.Profile.Validate` / `Step.Argv` | Structured argv only; no shell-string form exists; relative non-escaping `Dir` |
| Sandbox environment | `agentharness.FilterEnv` / `pass_env` | Allowlist; everything else on the host is dropped |
| Task delivery | `SandboxSpec` + `Command.StdinPath` | The prompt is written to a file and stdin is redirected from it; it never enters a command string |
| Argument safety | `sandbox.QuoteArgv` | Every argv element single-quoted; the shell cannot reinterpret it |
| Artifact paths | `artifacts.RunStore.resolve` | Traversal rejected in both run-ID and relative-path dimensions |
| Evidence | `factory.Redactor` | Secret values and credential patterns scrubbed on every write |
| Temporal exposure | `deployments/dev/temporal/docker-compose.yaml` | Every published port bound to `127.0.0.1` |

### 3. Credential handling

Three rules:

1. **Cube management credentials never enter a sandbox.** The factory does not
   need them there, so they are not there.
2. **Model credentials are operator-provisioned, opt-in, and referenced by name.**
   The generative config file uses `{env:NAME}`; the value arrives through the
   `pass_env` allowlist. A credential value is never written into a config file.
3. **Redaction is the last line, not the first.** Registered secrets are scrubbed
   from every artifact, and credential-shaped patterns are scrubbed regardless.

Where a credential must be readable inside the sandbox for the agent to
authenticate, that is an **explicit operator decision** (`provider_files`),
documented in the operator guide, never derived from task text, and never placed
in the task prompt.

### 4. Infrastructure isolation

- The factory never weakens Cube's egress policy. It does not request allowlist
  entries for internal CIDRs and does not change network configuration.
- The Cube control API (unauthenticated, loopback-bound on this deployment) is
  operator infrastructure. It is never exposed to API clients and never
  reachable from a sandbox.
- The factory host never executes repository code. Repository packing uses
  `git bundle`, which reads objects and writes a file; it is not a code path.

## Alternatives considered

| Alternative | Rejected because |
| --- | --- |
| Expose a generic "run this command" API | Explicitly forbidden by the design principles; it would make the control plane a remote shell. |
| Trust the agent's self-report | ADR 0004. |
| Put all verification in a `factory.yaml` committed to the repository | The agent can edit the file it is judged by. Operator-approved profiles only, until a validated schema exists. |
| A web UI in the MVP | Adds an authenticated network surface without adding capability. The CLI is the control plane. |
| Bind Temporal to `0.0.0.0` as upstream does | The workflow engine holds task text and repository identifiers; it must not be reachable off-host. |
| Deploy a credential-injecting gateway now | The cleanest long-term answer for model credentials, but it is a Phase 2 component. The current design documents the trade-off explicitly rather than hiding it. |

## Consequences

**Positive**

- No capability reachable from an API caller that the operator did not
  explicitly register.
- A compromised or confused agent cannot reach the factory host: the microVM
  has its own kernel and CubeVS denies internal CIDRs.
- Credentials are absent from the task prompt and from evidence by construction,
  with redaction as backup.
- Every run is attributable: sandboxes are tagged `origin=factory` with `run_id`
  and `harness`, so leaks are detectable (`factory sandboxes` fails the command
  when a factory sandbox is still alive).

**Negative / accepted**

- Repositories must be pre-registered for remote use; this is friction by design.
- Harnesses and verification profiles require operator configuration before a
  new agent or gate can be used.
- When a provider credential is provisioned into a sandbox, it **is** readable by
  the agent. This is a documented MVP trade-off, not an oversight.

## Security implications

This ADR *is* the security model; see the enforcement table in §2.
Operationally:

- `factory doctor` validates configuration, Cube reachability and template
  existence, and confirms Temporal is reachable, before any run.
- `factory sandboxes` is the leak-detection command and exits non-zero when a
  factory-owned sandbox is still alive.
- Every commit is scanned for credentials; `.env.example` contains placeholders
  only and the local `factory.toml` is gitignored because it contains
  host-specific paths.

## Future implications

- **OIDC/RBAC** would replace the current single-operator assumption, and would
  attach identity to the "who may request what" column in §1.
- **Vault integration** would replace `pass_env` and `provider_files`, moving
  credentials to short-lived, audited leases — the natural next step now that
  the credential surface is explicit and small.
- **OPA policies** would express the allowlists in §1 as reviewable policy rather
  than TOML, which matters once more than one team uses one factory.
- **Egress allowlisting per run** would implement deny-by-default with explicit
  destinations for repositories, package registries and model APIs. The
  `sandbox.Network` seam exists for it.
