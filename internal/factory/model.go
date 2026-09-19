package factory

import (
	"fmt"
	"time"
)

// RunState is the terminal or in-flight state of a factory run.
//
// The states are deliberately explicit: the MVP's most important property is
// that agent outcome and verification outcome are tracked SEPARATELY, so
// "the agent did something" can never be confused with "the change is good".
type RunState string

const (
	// StateRequested means the request was accepted and the workflow started.
	StateRequested RunState = "REQUESTED"
	// StateRunning means the workflow is executing.
	StateRunning RunState = "RUNNING"
	// StateAgentFailed means the coding agent exited non-zero or timed out.
	StateAgentFailed RunState = "AGENT_FAILED"
	// StateVerificationFailed means the agent reported success but a mandatory
	// deterministic gate failed. This is the state that proves the factory does
	// not trust the model.
	StateVerificationFailed RunState = "VERIFICATION_FAILED"
	// StateSucceeded means every mandatory gate passed.
	StateSucceeded RunState = "SUCCEEDED"
	// StateCancelled means the workflow was cancelled.
	StateCancelled RunState = "CANCELLED"
	// StateInfrastructureFailed means the factory could not complete the run
	// for a non-agent reason: Cube unreachable, provision failed, clone failed.
	StateInfrastructureFailed RunState = "INFRASTRUCTURE_FAILED"
	// StateInvalidRequest means the request was rejected by operator policy
	// before any sandbox was created: a disallowed repository, an unknown
	// harness, a missing revision.
	StateInvalidRequest RunState = "INVALID_REQUEST"
)

// Valid reports whether s is a known state.
func (s RunState) Valid() bool {
	switch s {
	case StateRequested, StateRunning, StateAgentFailed, StateVerificationFailed,
		StateSucceeded, StateCancelled, StateInfrastructureFailed, StateInvalidRequest:
		return true
	default:
		return false
	}
}

// Outcome is the result of one stage (agent, verification, cleanup).
type Outcome string

const (
	OutcomeSuccess   Outcome = "SUCCESS"
	OutcomeFailed    Outcome = "FAILED"
	OutcomeTimedOut  Outcome = "TIMED_OUT"
	OutcomeCancelled Outcome = "CANCELLED"
	OutcomeSkipped   Outcome = "SKIPPED"
	OutcomeError     Outcome = "ERROR"
)

// CleanupResult records whether the sandbox was actually destroyed.
//
// A leaked VM is an integration failure, so this is first-class evidence rather
// than a log line.
type CleanupResult struct {
	Attempted  bool          `json:"attempted"`
	Outcome    Outcome       `json:"outcome"`
	SandboxID  string        `json:"sandbox_id,omitempty"`
	Duration   time.Duration `json:"duration"`
	Verified   bool          `json:"verified"`
	Remaining  int           `json:"remaining_sandboxes"`
	Error      string        `json:"error,omitempty"`
	FinishedAt time.Time     `json:"finished_at"`
}

// RunManifest is the durable record of one factory run.
//
// Field groups below the MVP fields are the explicit Phase 2 extension points
// required by the design: speculative execution needs candidate identity,
// snapshot lineage, model accounting and human review outcome, and adding those
// fields later must not require rewriting the manifest.
type RunManifest struct {
	// ── Identity ────────────────────────────────────────────────────────────
	RunID          string `json:"run_id"`
	FactoryVersion string `json:"factory_version"`
	WorkflowID     string `json:"workflow_id"`
	WorkflowRunID  string `json:"workflow_run_id"`

	// ── Input ───────────────────────────────────────────────────────────────
	Repository        string `json:"repository"`
	RepositoryKind    string `json:"repository_kind,omitempty"`
	RequestedRevision string `json:"requested_revision"`
	BaselineSHA       string `json:"baseline_sha,omitempty"`
	ResultingSHA      string `json:"resulting_sha_if_any,omitempty"`
	TaskHash          string `json:"task_hash"`

	// ── Execution environment ───────────────────────────────────────────────
	AgentHarness        string `json:"agent_harness"`
	AgentVersion        string `json:"agent_version_if_available,omitempty"`
	SandboxProvider     string `json:"sandbox_provider"`
	CubeVersion         string `json:"cube_version_if_available,omitempty"`
	SandboxTemplate     string `json:"sandbox_template"`
	SandboxID           string `json:"sandbox_id,omitempty"`
	VerificationProfile string `json:"verification_profile"`

	// ── Timing ──────────────────────────────────────────────────────────────
	StartedAt   time.Time     `json:"started_at"`
	CompletedAt time.Time     `json:"completed_at"`
	Duration    time.Duration `json:"duration"`

	// ── Results (kept separate on purpose) ──────────────────────────────────
	AgentResult        Outcome       `json:"agent_result"`
	AgentExitCode      int           `json:"agent_exit_code"`
	VerificationResult Outcome       `json:"verification_result"`
	FactoryResult      RunState      `json:"factory_result"`
	CleanupResult      CleanupResult `json:"cleanup_result"`

	// ── Evidence pointers (store-relative) ──────────────────────────────────
	Artifacts string `json:"artifacts"`
	Patch     string `json:"patch,omitempty"`
	Error     string `json:"error,omitempty"`

	// VerificationMutatedTree reports that a verification gate changed the
	// working tree. The deliverable patch is captured BEFORE verification, so
	// such a change is never attributed to the agent; it is recorded separately
	// as evidence that the gate is not side-effect free.
	VerificationMutatedTree bool   `json:"verification_mutated_tree"`
	PostVerificationPatch   string `json:"post_verification_patch,omitempty"`

	// ── Phase 2 extension points (unused in the MVP) ────────────────────────
	Model          string   `json:"model,omitempty"`
	ModelProvider  string   `json:"model_provider,omitempty"`
	TokensIn       *int64   `json:"tokens_in,omitempty"`
	TokensOut      *int64   `json:"tokens_out,omitempty"`
	InferenceCost  *float64 `json:"inference_cost,omitempty"`
	SnapshotID     string   `json:"snapshot_id,omitempty"`
	CloneParent    string   `json:"clone_parent,omitempty"`
	CandidateID    string   `json:"candidate_id,omitempty"`
	Strategy       string   `json:"strategy,omitempty"`
	EvaluatorScore *float64 `json:"evaluator_score,omitempty"`
	ReviewFindings []string `json:"review_findings,omitempty"`
	HumanResult    string   `json:"human_result,omitempty"`
}

// Artifact paths, relative to a run's artifact directory.
//
// They are constants so the CLI, the operator guide and the tests cannot drift.
const (
	ArtifactTask               = "task.json"
	ArtifactRun                = "run.json"
	ArtifactEnvironment        = "environment.json"
	ArtifactGitBefore          = "git-before.txt"
	ArtifactGitAfter           = "git-after.txt"
	ArtifactPatch              = "changes.patch"
	ArtifactCleanup            = "cleanup.json"
	ArtifactAgentStdout        = "agent/stdout.log"
	ArtifactAgentStderr        = "agent/stderr.log"
	ArtifactAgentResult        = "agent/result.json"
	ArtifactAgentPrompt        = "agent/prompt.txt"
	ArtifactVerificationResult = "verification/result.json"
	ArtifactBaseline           = "baseline.json"
	// ArtifactPostVerificationPatch records the repository state AFTER the
	// deterministic gates ran. If it is non-empty, a gate mutated the working
	// tree — recorded as evidence rather than silently folded into the
	// deliverable, which is captured before verification.
	ArtifactPostVerificationPatch  = "verification/post-verification.patch"
	ArtifactPostVerificationStatus = "verification/post-verification.status"
	ArtifactManifestDir            = "."
)

// RunRequest is the input to a factory run.
//
// Everything here is either operator policy or the small, explicitly allowed
// set of user-controlled values: task text, an approved repository, and an
// approved revision.
type RunRequest struct {
	RunID      string `json:"run_id"`
	Repository string `json:"repository"`
	// RepositoryKind is "remote" or "local".
	RepositoryKind string `json:"repository_kind,omitempty"`
	// LocalPath is a host path, used by tests and by the acceptance fixture.
	LocalPath string `json:"local_path,omitempty"`
	Revision  string `json:"revision"`
	Task      string `json:"task"`

	SandboxTemplate     string `json:"sandbox_template"`
	AgentHarness        string `json:"agent_harness"`
	AgentModel          string `json:"agent_model,omitempty"`
	VerificationProfile string `json:"verification_profile"`

	// Timeouts.
	AgentTimeout        time.Duration `json:"agent_timeout"`
	VerificationTimeout time.Duration `json:"verification_timeout"`
	TotalTimeout        time.Duration `json:"total_timeout"`

	// RepositoryDir is where the repository is prepared inside the sandbox.
	RepositoryDir string `json:"repository_dir,omitempty"`
}

// Validate rejects a request that the factory cannot honour.
func (r RunRequest) Validate() error {
	var problems []string
	if r.RunID == "" {
		problems = append(problems, "run_id is required")
	}
	if r.Task == "" {
		problems = append(problems, "task is required")
	}
	if r.Repository == "" && r.LocalPath == "" {
		problems = append(problems, "a repository is required")
	}
	if r.Revision == "" {
		problems = append(problems, "revision is required (runs must be reproducible)")
	}
	if r.AgentHarness == "" {
		problems = append(problems, "agent_harness is required")
	}
	if r.VerificationProfile == "" {
		problems = append(problems, "verification_profile is required")
	}
	if len(problems) > 0 {
		return fmt.Errorf("invalid run request: %v", problems)
	}
	return nil
}

// DefaultRepositoryDir is where repositories are prepared inside a sandbox.
const DefaultRepositoryDir = "/workspace/repository"

// EffectiveRepositoryDir returns the configured or default repository path.
func (r RunRequest) EffectiveRepositoryDir() string {
	if r.RepositoryDir != "" {
		return r.RepositoryDir
	}
	return DefaultRepositoryDir
}
