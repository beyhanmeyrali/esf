# ADR 0001 — Use CubeSandbox as the execution fabric

- **Status:** Accepted
- **Date:** 2026-09-19
- **Deciders:** Factory engineering

## Context

The factory must execute untrusted code: a coding agent's edits, a repository's
build scripts, and a repository's tests. Machinist's own `SECURITY.md` documents
the gap precisely: repository mappings and executor allowlists constrain
*assignment*, but they "do not sandbox a command from other files or tools
available to the worker OS user".

A pre-existing CubeSandbox deployment is installed and running on the factory
host (`v0.7.1`, commit `31d911e4`), providing KVM-backed microVMs with their own
kernel, filesystem and network namespace. It already offers snapshot, clone and
rollback in its SDK, which the long-term speculative-execution design needs.

Measured facts from the original development deployment that shaped this decision:

- Sandboxes reach the public internet but **no** host or internal CIDR.
- Per-run network policy overrides via the E2B create API were **not honoured**
  on this deployment; the baked template configuration wins.
- The base template has **no `git`** and no Node runtime.
- Creating a sandbox takes ~0.25 s; a 189 MiB agent binary transfers via the
  envd files API in ~0.5 s.

## Decision

Use the existing CubeSandbox deployment as the factory's only execution fabric,
behind a narrow `sandbox.Provider` interface
(`Create`, `Reattach`, `Destroy`, `List`, `Ping`).

The factory **consumes** the deployment. It does not install, reconfigure,
reset, or remove Cube components, and it never creates, mutates, or deletes
templates. It selects an existing READY template by ID.

Factory-owned prerequisites (for example `git`, which repository preparation
needs regardless of harness) are installed **inside each sandbox** at prepare
time, rather than by changing the deployment.

## Alternatives considered

| Alternative | Rejected because |
| --- | --- |
| Run agents on the worker host, as Machinist does | Documented capability gap: the agent runs with the worker user's full permissions. Unacceptable for untrusted repository content. |
| Docker / containerd containers | Shares the host kernel; weaker isolation than a microVM, and the host already runs an unrelated container fleet. |
| Create a dedicated factory Cube template now | It is a deployment-level mutation. The design says a versioned factory template is the *production* direction, but it must be an operator decision, not something this project does unilaterally. |
| A different microVM/sandbox vendor | CubeSandbox is installed, proven working, and already provides snapshot/clone/rollback — the Phase 2 primitives. |
| Kubectl/Kubernetes-based execution | Explicitly out of scope for the MVP. |

## Consequences

**Positive**

- Untrusted code runs in a microVM with its own kernel. "The prompt cannot reach
  the host" becomes an enforced property rather than a documented caveat.
- Sandboxes are cheap and fast enough for one-per-run.
- Snapshot/clone/rollback already exist in the SDK, so Phase 2 fan-out does not
  require changing fabric.
- The `Provider` interface keeps the vendor out of workflow code: no package
  above `internal/sandbox/cube` imports the Cube SDK.

**Negative / accepted**

- CubeSandbox's data plane takes a **command string** executed by
  `/bin/bash -lc`, not an `execve` argument vector. The provider therefore
  single-quotes every argument through one tested function
  (`sandbox.QuoteArgv`) to restore structured-argv semantics.
- Sandboxes cannot reach host-local services, so a host-local model endpoint is
  unusable from inside an agent run.
- Provisioning `git` per run costs ~6 s and ~300 MiB of a ~1 GiB disk.

## Security implications

- The Cube control API on this deployment is **unauthenticated and loopback
  bound**. It is treated as operator infrastructure: it is never exposed to API
  clients and never reachable from a sandbox (CubeVS denies internal CIDRs).
- The factory **never weakens** the deployment's egress policy. It does not
  request an allowlist entry for an internal CIDR. `sandbox.Spec.Network` exists
  so a future policy can be layered in, but a zero value means "leave the
  deployment default alone".
- Cube management credentials are never placed inside a sandbox.
- Run metadata tags every factory sandbox (`origin=factory`, `run_id`,
  `harness`) so a leak is attributable and the factory can distinguish its own
  sandboxes from anyone else's.

## Future implications

- Phase 2 snapshots and clones are reached through the same provider, by
  implementing `sandbox.Snapshotter` and flipping
  `Capabilities{Snapshot,Clone,Rollback}` once tested. No call sites change.
- A versioned factory template (`factory-agent-base-v1`) would remove per-run
  provisioning by moving it into the image — a deployment change, tracked as
  Phase 2 work, not a code change.
- If egress policy can be applied per run, `SandboxSpec.Network` is the seam.
  The intended default is deny-by-default with allowlists for repositories,
  package registries and model APIs.
