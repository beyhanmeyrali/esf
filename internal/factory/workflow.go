package factory

import (
	"fmt"
	"strings"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/mitkox/esf/internal/agentharness"
	"github.com/mitkox/esf/internal/repository"
	"github.com/mitkox/esf/internal/verification"
)

// TaskQueue is the default Temporal task queue for factory activities.
const TaskQueue = "factory"

// WorkflowName is the registered workflow type.
const WorkflowName = "SoftwareChangeWorkflow"

// WorkflowIDPrefix namespaces factory workflows inside a Temporal namespace.
const WorkflowIDPrefix = "factory-run-"

// WorkflowIDForRun derives the workflow ID for a run.
//
// It is deterministic so the CLI can locate a run's workflow without a separate
// registry, and so resubmitting the same run ID is idempotent.
func WorkflowIDForRun(runID string) string { return WorkflowIDPrefix + runID }

// RunIDFromWorkflowID reverses WorkflowIDForRun.
func RunIDFromWorkflowID(workflowID string) (string, bool) {
	if !strings.HasPrefix(workflowID, WorkflowIDPrefix) {
		return "", false
	}
	return strings.TrimPrefix(workflowID, WorkflowIDPrefix), true
}

// RunStatus is the workflow's queryable status.
//
// It is deliberately small: it is returned by a Temporal query while the
// workflow is still running, and the full record lives in the run manifest.
type RunStatus struct {
	RunID         string    `json:"run_id"`
	State         RunState  `json:"state"`
	SandboxID     string    `json:"sandbox_id,omitempty"`
	CurrentStep   string    `json:"current_step"`
	StartedAt     time.Time `json:"started_at"`
	BaselineSHA   string    `json:"baseline_sha,omitempty"`
	AgentOutcome  Outcome   `json:"agent_result"`
	VerifyOutcome Outcome   `json:"verification_result"`
	Error         string    `json:"error,omitempty"`
	SandboxGone   bool      `json:"sandbox_destroyed"`
}

// workflowState is the deterministic state carried across workflow steps.
//
// Every field is derived from an activity result, never from wall-clock time or
// randomness, so Temporal replay reconstructs it identically.
type workflowState struct {
	status          RunStatus
	executionID     string
	createAttempted bool
	totalTimedOut   bool

	validate          ValidateOutput
	baseline          repository.Baseline
	sandboxID         string
	template          string
	harnessVer        string
	agent             agentharness.Result
	agentOutcome      Outcome
	agentRan          bool
	agentAttmpt       int32
	verify            verification.Result
	verifyRan         bool
	patch             string
	patchPath         string
	postPatchPath     string
	verificationDrift bool
	resultingSHA      string
	cleanup           CleanupResult
	cleanupDone       bool
	collectionFailed  bool
}

// SoftwareChangeWorkflow is the factory's durable run:
//
//	validate → cube.create → sandbox.prepare → repo.prepare →
//	agent.run → verify → artifacts.collect → finalize
//
// with cleanup running on every exit path.
//
// The shape is deliberately linear for the MVP: one request, one sandbox, one
// agent, one deterministic verification, one patch. Speculative fan-out is a
// Phase 2 concern — but because every external operation is already an
// activity, fan-out can be added without restructuring this function.
//
// No network, filesystem or process call happens here: that is what keeps the
// workflow deterministic and therefore replayable.
func SoftwareChangeWorkflow(ctx workflow.Context, req RunRequest) (RunManifest, error) {
	// A zero-valued receiver identifies activity methods. It is never invoked
	// from workflow code; only its method name is used for dispatch.
	acts := &Activities{}
	info := workflow.GetInfo(ctx)
	startedAt := workflow.Now(ctx)

	state := &workflowState{
		executionID: info.WorkflowExecution.RunID,
		status: RunStatus{
			RunID:         req.RunID,
			State:         StateRunning,
			CurrentStep:   "starting",
			StartedAt:     startedAt,
			AgentOutcome:  OutcomeSkipped,
			VerifyOutcome: OutcomeSkipped,
		},
	}

	// Register the status query before any work so a caller can observe an
	// in-flight run.
	if err := workflow.SetQueryHandler(ctx, "status", func() (RunStatus, error) {
		return state.status, nil
	}); err != nil {
		return RunManifest{}, err
	}

	// Cleanup is registered BEFORE the sandbox exists, so it runs on every exit
	// path: success, agent failure, verification failure, timeout and
	// cancellation. A leaked microVM counts as a failed integration test, so
	// this is not best-effort even when the workflow is being cancelled.
	//
	// The defer is a SAFETY NET. The explicit call below runs first, because
	// the manifest is written after cleanup and must therefore contain the
	// cleanup result: a run whose manifest says cleanup was never attempted is
	// not auditable. cleanupSandbox is idempotent, so calling it twice is safe.
	defer func() { cleanupSandbox(ctx, acts, state) }()

	if err := state.run(ctx, acts, req); err != nil {
		state.status.Error = err.Error()
		if temporal.IsCanceledError(err) || ctx.Err() != nil {
			state.status.State = StateCancelled
		}
		if state.totalTimedOut {
			state.status.State = StateInfrastructureFailed
			state.status.Error = "total execution timeout exceeded"
		}
	}

	// Destroy the sandbox and verify it is gone BEFORE recording the result.
	state.status.CurrentStep = "cube.destroy"
	cleanupSandbox(ctx, acts, state)
	state.status.CurrentStep = "finalizing"
	// Success requires both durable evidence and verified sandbox teardown.
	if state.status.State == StateSucceeded && (state.collectionFailed || !state.cleanup.Verified) {
		state.status.State = StateInfrastructureFailed
		if state.status.Error == "" {
			state.status.Error = "sandbox cleanup was not verified: " + state.cleanup.Error
		}
	}

	manifest, err := finalizeRun(ctx, acts, state, req, info, startedAt)
	if err != nil {
		return manifest, fmt.Errorf("finalize run: %w", err)
	}
	state.status.State = manifest.FactoryResult
	state.status.AgentOutcome = manifest.AgentResult
	state.status.VerifyOutcome = manifest.VerificationResult
	return manifest, nil
}

// run executes the workflow body, recording outcome state as it proceeds.
func (s *workflowState) run(ctx workflow.Context, acts *Activities, req RunRequest) error {
	// ── 1. Validate ─────────────────────────────────────────────────────────
	// Validation is deterministic, so it is not retried: a rejected request
	// will be rejected again.
	s.status.CurrentStep = "validate"
	var validated ValidateOutput
	if err := executeActivity(ctx, acts.ValidateRequest, noRetry(), ValidateInput{Request: req}).Get(ctx, &validated); err != nil {
		s.status.State = StateInvalidRequest
		return fmt.Errorf("request rejected: %w", err)
	}
	s.validate = validated
	req.AgentTimeout = validated.AgentTimeout
	req.VerificationTimeout = validated.VerificationTimeout
	req.TotalTimeout = validated.TotalTimeout
	ctx, cancel := workflow.WithCancel(ctx)
	defer cancel()
	workflow.Go(ctx, func(timerCtx workflow.Context) {
		if workflow.NewTimer(timerCtx, req.TotalTimeout).Get(timerCtx, nil) == nil {
			s.totalTimedOut = true
			cancel()
		}
	})

	// ── 2. Create sandbox ───────────────────────────────────────────────────
	s.status.CurrentStep = "cube.create"
	s.createAttempted = true
	var created CreateSandboxOutput
	if err := executeActivity(ctx, acts.CreateSandbox, infraRetry(), CreateSandboxInput{
		ExecutionID: s.executionID,
		RunID:       req.RunID,
		Template:    req.SandboxTemplate,
		Harness:     req.AgentHarness,
		IdleTimeout: req.TotalTimeout,
	}).Get(ctx, &created); err != nil {
		s.status.State = StateInfrastructureFailed
		return fmt.Errorf("create sandbox: %w", err)
	}
	s.sandboxID = created.SandboxID
	s.template = created.Template
	s.status.SandboxID = created.SandboxID

	// ── 3. Provision the coding agent ───────────────────────────────────────
	s.status.CurrentStep = "sandbox.prepare"
	var prepared PrepareSandboxOutput
	if err := executeActivity(ctx, acts.PrepareSandbox, infraRetry(), PrepareSandboxInput{
		RunID:     req.RunID,
		SandboxID: s.sandboxID,
		Harness:   req.AgentHarness,
	}).Get(ctx, &prepared); err != nil {
		s.status.State = StateInfrastructureFailed
		return fmt.Errorf("prepare sandbox: %w", err)
	}
	s.harnessVer = prepared.HarnessVersion

	// ── 4. Prepare the repository ───────────────────────────────────────────
	s.status.CurrentStep = "repo.prepare"
	var preparedRepo PrepareRepositoryOutput
	if err := executeActivity(ctx, acts.PrepareRepository, infraRetry(), PrepareRepositoryInput{
		RunID:     req.RunID,
		SandboxID: s.sandboxID,
		Request:   req,
	}).Get(ctx, &preparedRepo); err != nil {
		s.status.State = StateInfrastructureFailed
		return fmt.Errorf("prepare repository: %w", err)
	}
	s.baseline = preparedRepo.Baseline
	s.status.BaselineSHA = preparedRepo.Baseline.SHA

	// ── 5. Run the coding agent ─────────────────────────────────────────────
	s.status.CurrentStep = "agent.run"
	var agentOut RunAgentOutput
	agentOpts := agentRetry()
	agentOpts.StartToCloseTimeout = req.AgentTimeout + time.Minute
	agentErr := executeActivity(ctx, acts.RunAgent, agentOpts, RunAgentInput{
		RunID:         req.RunID,
		SandboxID:     s.sandboxID,
		Harness:       req.AgentHarness,
		Model:         req.AgentModel,
		Prompt:        req.Task,
		RepositoryDir: req.EffectiveRepositoryDir(),
		Timeout:       req.AgentTimeout,
	}).Get(ctx, &agentOut)

	if agentErr != nil {
		s.agentRan = true
		// The activity exhausted its retries. A cancelled workflow is reported
		// as CANCELLED rather than as an agent failure.
		if isCancellation(agentErr) {
			s.agent.ExitCode = -1
			s.status.AgentOutcome = OutcomeCancelled
			s.agentOutcome = OutcomeCancelled
			s.status.State = StateCancelled
			return fmt.Errorf("run cancelled during agent execution: %w", agentErr)
		}
		s.agent.ExitCode = -1
		s.status.AgentOutcome = OutcomeError
		s.status.State = StateAgentFailed
		s.collect(ctx, acts, req, "")
		return fmt.Errorf("agent execution failed: %w", agentErr)
	}

	if agentOut.EvidenceError != "" {
		s.collectionFailed = true
		s.status.Error = "agent evidence persistence failed: " + agentOut.EvidenceError
	}
	s.agent = agentOut.Result
	s.agentRan = true
	s.agentAttmpt = agentOut.Attempt
	switch {
	case agentOut.Result.Succeeded():
		s.agentOutcome = OutcomeSuccess
	case agentOut.Result.TimedOut:
		s.agentOutcome = OutcomeTimedOut
	default:
		s.agentOutcome = OutcomeFailed
	}
	s.status.AgentOutcome = s.agentOutcome

	if !agentOut.Result.Succeeded() {
		// There is nothing meaningful to verify when the agent did not
		// succeed. Verification is recorded as SKIPPED, never silently absent.
		s.status.VerifyOutcome = OutcomeSkipped
		s.status.State = StateAgentFailed
		// The partial patch is still collected: evidence for a failed run is as
		// valuable as for a successful one.
		s.collect(ctx, acts, req, "")
		return fmt.Errorf("agent did not succeed: exit code %d, timed out=%v",
			agentOut.Result.ExitCode, agentOut.Result.TimedOut)
	}

	// ── 6. Capture the agent's contribution BEFORE verification ─────────────
	// This ordering matters: a verification gate can rewrite tracked files (a
	// build step regenerating a lockfile, for example). Capturing the patch
	// after the gates would attribute that side effect to the agent and put
	// sandbox-local paths into the deliverable.
	s.collect(ctx, acts, req, "")

	// ── 7. Deterministic verification ───────────────────────────────────────
	s.status.CurrentStep = "verify"
	var verified RunVerificationOutput
	if err := executeActivity(ctx, acts.RunVerification, verificationRetry(), RunVerificationInput{
		RunID:         req.RunID,
		SandboxID:     s.sandboxID,
		Timeout:       req.VerificationTimeout,
		Profile:       req.VerificationProfile,
		RepositoryDir: req.EffectiveRepositoryDir(),
	}).Get(ctx, &verified); err != nil {
		s.verifyRan = true
		s.status.VerifyOutcome = OutcomeError
		s.status.State = StateInfrastructureFailed
		s.collect(ctx, acts, req, VerificationPhase)
		return fmt.Errorf("verification could not be executed: %w", err)
	}

	if verified.EvidenceError != "" {
		s.collectionFailed = true
		s.status.Error = "verification evidence persistence failed: " + verified.EvidenceError
	}
	s.verify = verified.Result
	s.verifyRan = true
	if verified.Result.Passed {
		s.status.VerifyOutcome = OutcomeSuccess
	} else {
		s.status.VerifyOutcome = OutcomeFailed
	}

	// ── 8. Did the gates mutate the tree? ───────────────────────────────────
	// Recorded as evidence, never folded into the deliverable.
	s.collect(ctx, acts, req, VerificationPhase)

	// ── 9. Decide the factory result ────────────────────────────────────────
	// Only deterministic exit codes decide this. The model never does.
	if !verified.Result.Passed {
		s.status.State = StateVerificationFailed
		return fmt.Errorf("verification failed: gates [%s] did not pass",
			strings.Join(verified.Result.FailingSteps(), ", "))
	}
	s.status.State = StateSucceeded
	return nil
}

// collect extracts the patch and post-run repository state.
//
// The empty phase captures the AGENT's contribution and is called BEFORE
// verification, so a gate that rewrites a tracked file cannot be mistaken for
// part of the agent's change. The verification phase captures the tree
// afterwards purely to report such drift.
//
// It runs on failure paths too, so a failed run's evidence is as complete as a
// successful one's.
func (s *workflowState) collect(ctx workflow.Context, acts *Activities, req RunRequest, phase string) {
	if phase == "" {
		s.status.CurrentStep = "artifacts.collect"
	} else {
		s.status.CurrentStep = "artifacts.verify-drift"
	}
	var collected CollectArtifactsOutput
	if err := executeActivity(ctx, acts.CollectArtifacts, infraRetry(), CollectArtifactsInput{
		RunID:         req.RunID,
		SandboxID:     s.sandboxID,
		RepositoryDir: req.EffectiveRepositoryDir(),
		Phase:         phase,
	}).Get(ctx, &collected); err != nil {
		s.collectionFailed = true
		// Artifact collection failure must not erase the run's verdict, but the
		// gap is recorded so it is visible.
		if s.status.Error == "" {
			s.status.Error = "artifact collection failed: " + err.Error()
		}
		return
	}
	if phase == VerificationPhase {
		s.postPatchPath = collected.PatchArtifact
		// Drift means the tree changed AFTER the agent's patch was captured.
		// The post-verification diff is non-empty whenever the agent changed
		// anything, so comparing it to the agent's own patch is the only
		// correct test. The comparison is a pure string operation, so it is
		// safe in workflow code.
		s.verificationDrift = collected.Patch != s.patch
		return
	}
	s.patch = collected.Patch
	s.patchPath = collected.PatchArtifact
	s.resultingSHA = collected.ResultingSHA
}

// cleanupSandbox destroys the sandbox on every exit path.
//
// A disconnected context is used so cleanup still runs when the workflow has
// been cancelled — the case in which a leaked VM is most likely.
func cleanupSandbox(ctx workflow.Context, acts *Activities, state *workflowState) {
	// Idempotent: the explicit call and the deferred safety net must not both
	// destroy (and both report) the same sandbox.
	if state.cleanupDone {
		return
	}
	state.cleanupDone = true

	if !state.createAttempted {
		// Nothing was created, so there is nothing to clean up. That is a
		// success, not an omission.
		state.cleanup = CleanupResult{
			Attempted:  false,
			Outcome:    OutcomeSkipped,
			FinishedAt: workflow.Now(ctx),
		}
		return
	}

	disconnected, _ := workflow.NewDisconnectedContext(ctx)
	actx := workflow.WithActivityOptions(disconnected, workflow.ActivityOptions{
		StartToCloseTimeout: 10 * time.Minute,
		// Cleanup is retried hard: a leaked VM is expensive and Destroy is
		// idempotent.
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    2 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    60 * time.Second,
			MaximumAttempts:    8,
		},
	})

	var cleanup CleanupResult
	if err := workflow.ExecuteActivity(actx, acts.DestroySandbox, DestroySandboxInput{
		ExecutionID: state.executionID,
		RunID:       state.status.RunID,
		SandboxID:   state.sandboxID,
	}).Get(disconnected, &cleanup); err != nil {
		cleanup = CleanupResult{
			Attempted:  true,
			Outcome:    OutcomeError,
			SandboxID:  state.sandboxID,
			Error:      err.Error(),
			FinishedAt: workflow.Now(ctx),
		}
		workflow.GetLogger(ctx).Error("sandbox cleanup failed; a sandbox may have leaked",
			"run_id", state.status.RunID, "sandbox_id", state.sandboxID, "error", err)
	}
	state.cleanup = cleanup
	state.status.SandboxGone = cleanup.Verified
}

// finalizeRun writes the durable manifest.
//
// It also uses a disconnected context so the record survives cancellation: a
// cancelled run that leaves no trace is not auditable.
func finalizeRun(
	ctx workflow.Context,
	acts *Activities,
	state *workflowState,
	req RunRequest,
	info *workflow.Info,
	startedAt time.Time,
) (RunManifest, error) {
	disconnected, _ := workflow.NewDisconnectedContext(ctx)
	actx := workflow.WithActivityOptions(disconnected, workflow.ActivityOptions{
		StartToCloseTimeout: 2 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Second,
			BackoffCoefficient: 2.0,
			MaximumAttempts:    5,
		},
	})

	agentOutcome := OutcomeSkipped
	if state.agentRan {
		agentOutcome = state.agentOutcome
		if agentOutcome == "" {
			agentOutcome = OutcomeError
		}
	}
	verifyOutcome := OutcomeSkipped
	if state.verifyRan {
		verifyOutcome = OutcomeFailed
		if state.verify.Passed {
			verifyOutcome = OutcomeSuccess
		}
	}
	// A verification stage that was skipped because the agent failed keeps the
	// SKIPPED outcome that run() already recorded.
	if state.status.VerifyOutcome == OutcomeError {
		verifyOutcome = OutcomeError
	}

	input := FinalizeInput{
		Request:                 req,
		Validate:                state.validate,
		SandboxID:               state.sandboxID,
		SandboxTemplate:         state.template,
		HarnessVersion:          state.harnessVer,
		AgentResult:             state.agent,
		AgentOutcome:            agentOutcome,
		AgentAttempt:            state.agentAttmpt,
		VerificationResult:      state.verify,
		VerificationOutcome:     verifyOutcome,
		Baseline:                state.baseline,
		Patch:                   state.patch,
		PatchArtifact:           state.patchPath,
		ResultingSHA:            state.resultingSHA,
		VerificationMutatedTree: state.verificationDrift,
		PostVerificationPatch:   state.postPatchPath,
		FactoryResult:           state.status.State,
		Cleanup:                 state.cleanup,
		WorkflowID:              info.WorkflowExecution.ID,
		WorkflowRunID:           info.WorkflowExecution.RunID,
		StartedAt:               startedAt,
		CompletedAt:             workflow.Now(ctx),
		Error:                   state.status.Error,
	}

	var out FinalizeOutput
	if err := workflow.ExecuteActivity(actx, acts.FinalizeResult, input).Get(disconnected, &out); err != nil {
		return RunManifest{}, err
	}
	return out.Manifest, nil
}

// ── activity options ──────────────────────────────────────────────────────

// noRetry disables retries for deterministic operations.
func noRetry() workflow.ActivityOptions {
	return workflow.ActivityOptions{
		StartToCloseTimeout: 5 * time.Minute,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 1},
	}
}

// infraRetry retries infrastructure operations that are safe to repeat.
func infraRetry() workflow.ActivityOptions {
	return workflow.ActivityOptions{
		StartToCloseTimeout: 20 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    2 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    2 * time.Minute,
			MaximumAttempts:    5,
		},
	}
}

// agentRetry does not retry agent execution: transport failure can leave a
// completed agent whose acknowledgement was lost. Re-execution is unsafe.
//
// The activity still records its Temporal attempt number for diagnosis.
func agentRetry() workflow.ActivityOptions {
	return workflow.ActivityOptions{
		StartToCloseTimeout: 60 * time.Minute,
		// A heartbeat keeps long agent runs from being declared dead while the
		// sandbox is still working.
		HeartbeatTimeout: 3 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    10 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    2 * time.Minute,
			MaximumAttempts:    1,
		},
	}
}

// verificationRetry retries verification. It is deterministic, so retrying can
// only help.
func verificationRetry() workflow.ActivityOptions {
	return workflow.ActivityOptions{
		StartToCloseTimeout: 45 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    5 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    time.Minute,
			MaximumAttempts:    3,
		},
	}
}

// executeActivity keeps activity invocation consistent across the workflow.
func executeActivity(ctx workflow.Context, activity any, opts workflow.ActivityOptions, args ...any) workflow.Future {
	return workflow.ExecuteActivity(workflow.WithActivityOptions(ctx, opts), activity, args...)
}

// isCancellation reports whether an activity error is a workflow cancellation.
func isCancellation(err error) bool {
	if err == nil {
		return false
	}
	if temporal.IsCanceledError(err) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "canceled") || strings.Contains(msg, "cancelled")
}
