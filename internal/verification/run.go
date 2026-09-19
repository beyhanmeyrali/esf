package verification

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/mitkox/esf/internal/sandbox"
)

// Sink receives verification output artifacts.
//
// artifacts.LocalStore satisfies this structurally, so verification does not
// import the artifact store.
type Sink interface {
	// Write stores data at a store-relative path.
	Write(relPath string, data []byte) error
}

// StepResult is the recorded outcome of one gate.
type StepResult struct {
	ID          string        `json:"id"`
	Description string        `json:"description,omitempty"`
	Argv        []string      `json:"argv"`
	Dir         string        `json:"dir,omitempty"`
	Mandatory   bool          `json:"mandatory"`
	StartedAt   time.Time     `json:"started_at"`
	CompletedAt time.Time     `json:"completed_at"`
	Duration    time.Duration `json:"duration"`
	ExitCode    int           `json:"exit_code"`
	TimedOut    bool          `json:"timed_out"`
	Passed      bool          `json:"passed"`
	// StdoutArtifact and StderrArtifact are store-relative paths.
	StdoutArtifact string `json:"stdout_artifact,omitempty"`
	StderrArtifact string `json:"stderr_artifact,omitempty"`
	// Error is set when the step could not be executed at all.
	Error string `json:"error,omitempty"`
}

// Result is the recorded outcome of a whole profile.
type Result struct {
	Profile     string        `json:"profile"`
	Steps       []StepResult  `json:"steps"`
	Passed      bool          `json:"passed"`
	StartedAt   time.Time     `json:"started_at"`
	CompletedAt time.Time     `json:"completed_at"`
	Duration    time.Duration `json:"duration"`
}

// FailingSteps returns the IDs of mandatory steps that did not pass.
func (r Result) FailingSteps() []string {
	var failing []string
	for _, step := range r.Steps {
		if step.Mandatory && !step.Passed {
			failing = append(failing, step.ID)
		}
	}
	return failing
}

// Runner executes verification profiles inside a sandbox.
type Runner struct {
	// Sink stores stdout/stderr artifacts. When nil, output is not persisted
	// (useful for tests).
	Sink Sink
	// ArtifactPrefix is prepended to artifact paths, for example "verification".
	ArtifactPrefix string
	// Now allows tests to control timestamps.
	Now func() time.Time
}

// Run executes every step of the profile in order.
//
// It does not stop at the first failure: a complete picture of which gates pass
// is more useful evidence than an early exit, and the profile's total runtime
// is already bounded per step.
//
// A step that cannot be executed at all marks the result failed and records the
// error; it never silently passes.
func (r *Runner) Run(ctx context.Context, sb sandbox.Sandbox, profile Profile, repositoryDir string) (Result, error) {
	if err := profile.Validate(); err != nil {
		return Result{}, err
	}
	now := r.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}

	result := Result{
		Profile:   profile.Name,
		StartedAt: now(),
		Passed:    true,
	}

	for _, step := range profile.Steps {
		stepResult := r.runStep(ctx, sb, step, repositoryDir, now)
		result.Steps = append(result.Steps, stepResult)
		if stepResult.Mandatory && !stepResult.Passed {
			result.Passed = false
		}
	}

	result.CompletedAt = now()
	result.Duration = result.CompletedAt.Sub(result.StartedAt)
	return result, nil
}

func (r *Runner) runStep(ctx context.Context, sb sandbox.Sandbox, step Step, repositoryDir string, now func() time.Time) StepResult {
	stepResult := StepResult{
		ID:          step.ID,
		Description: step.Description,
		Argv:        append([]string(nil), step.Argv...),
		Dir:         step.Dir,
		Mandatory:   step.IsMandatory(),
		StartedAt:   now(),
	}

	dir := repositoryDir
	if step.Dir != "" {
		dir = path.Join(repositoryDir, step.Dir)
	}

	exec, err := sb.Execute(ctx, sandbox.Command{
		Argv:        step.Argv,
		Dir:         dir,
		Env:         step.Env,
		Timeout:     step.EffectiveTimeout(),
		Description: "verify: " + step.ID,
	})

	stepResult.CompletedAt = now()
	stepResult.Duration = stepResult.CompletedAt.Sub(stepResult.StartedAt)

	if err != nil {
		// The gate could not run. That is a failure, not a pass.
		stepResult.Error = err.Error()
		stepResult.ExitCode = -1
		stepResult.Passed = false
		return stepResult
	}

	stepResult.ExitCode = exec.ExitCode
	stepResult.TimedOut = exec.TimedOut
	stepResult.Passed = exec.Succeeded()
	if !exec.StartedAt.IsZero() {
		stepResult.StartedAt = exec.StartedAt
	}
	if !exec.CompletedAt.IsZero() {
		stepResult.CompletedAt = exec.CompletedAt
		stepResult.Duration = exec.CompletedAt.Sub(stepResult.StartedAt)
	}

	// Evidence is written even for passing gates: a green build that nobody can
	// inspect is not evidence.
	stepResult.StdoutArtifact = r.write(step.ID, "stdout", exec.Stdout)
	stepResult.StderrArtifact = r.write(step.ID, "stderr", exec.Stderr)
	return stepResult
}

func (r *Runner) write(stepID, stream, content string) string {
	if r.Sink == nil {
		return ""
	}
	rel := path.Join(artifactPrefix(r.ArtifactPrefix), fmt.Sprintf("%s.%s", sanitizeID(stepID), stream))
	if err := r.Sink.Write(rel, []byte(content)); err != nil {
		// Artifact persistence problems must be visible but must not change the
		// verification verdict, which is decided by exit codes alone.
		return ""
	}
	return rel
}

func artifactPrefix(prefix string) string {
	if strings.TrimSpace(prefix) == "" {
		return "verification"
	}
	return prefix
}

// sanitizeID makes a step ID safe to use as a file name.
func sanitizeID(id string) string {
	replacer := strings.NewReplacer("/", "_", "\\", "_", " ", "_", "..", "_")
	cleaned := replacer.Replace(strings.TrimSpace(id))
	if cleaned == "" {
		return "step"
	}
	return cleaned
}
