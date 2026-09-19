package factory

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"go.temporal.io/sdk/activity"

	"go.temporal.io/sdk/client"
	tlog "go.temporal.io/sdk/log"
	"go.temporal.io/sdk/workflow"
)

// NewTemporalClientFunc is the signature used to dial Temporal. It is a seam so
// tests can substitute a client without a server.
type NewTemporalClientFunc func(options client.Options) (client.Client, error)

// workflowRegistrationOptions returns the registration options for the factory
// workflow.
func workflowRegistrationOptions() workflow.RegisterOptions {
	return workflow.RegisterOptions{Name: WorkflowName}
}

// newTemporalLogger adapts a slog.Logger to Temporal's logger interface, so the
// SDK's own diagnostics land in the factory's structured log stream.
func newTemporalLogger(log *slog.Logger) tlog.Logger {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &slogTemporalLogger{log: log}
}

type slogTemporalLogger struct {
	log *slog.Logger
}

func (l *slogTemporalLogger) Debug(msg string, keyvals ...any) {
	l.log.Debug(msg, keyvals...)
}

func (l *slogTemporalLogger) Info(msg string, keyvals ...any) {
	l.log.Info(msg, keyvals...)
}

func (l *slogTemporalLogger) Warn(msg string, keyvals ...any) {
	l.log.Warn(msg, keyvals...)
}

func (l *slogTemporalLogger) Error(msg string, keyvals ...any) {
	l.log.Error(msg, keyvals...)
}

var _ tlog.Logger = (*slogTemporalLogger)(nil)

// WorkflowStatus describes a workflow execution for the CLI.
type WorkflowStatus struct {
	WorkflowID string
	RunID      string
	Status     string
	StartTime  string
	CloseTime  string
	Running    bool
}

// DescribeWorkflow returns a workflow's current Temporal-side status.
func DescribeWorkflow(ctx context.Context, c client.Client, workflowID, runID string) (WorkflowStatus, error) {
	resp, err := c.DescribeWorkflowExecution(ctx, workflowID, runID)
	if err != nil {
		return WorkflowStatus{}, fmt.Errorf("describe workflow %s: %w", workflowID, err)
	}
	info := resp.GetWorkflowExecutionInfo()
	status := WorkflowStatus{WorkflowID: workflowID, RunID: runID}
	if info == nil {
		return status, nil
	}
	status.Status = info.GetStatus().String()
	status.Running = info.GetStatus().String() == "Running"
	if info.GetStartTime() != nil {
		status.StartTime = info.GetStartTime().AsTime().UTC().Format("2006-01-02T15:04:05Z")
	}
	if info.GetCloseTime() != nil {
		t := info.GetCloseTime().AsTime()
		if !t.IsZero() {
			status.CloseTime = t.UTC().Format("2006-01-02T15:04:05Z")
		}
	}
	return status, nil
}

// QueryRunStatus asks a running workflow for its status.
//
// It returns Temporal's not-found error when the workflow has completed and its
// history is no longer queryable, which callers treat as "read the manifest".
func QueryRunStatus(ctx context.Context, c client.Client, workflowID, runID string) (RunStatus, error) {
	resp, err := c.QueryWorkflow(ctx, workflowID, runID, "status")
	if err != nil {
		return RunStatus{}, err
	}
	var status RunStatus
	if err := resp.Get(&status); err != nil {
		return RunStatus{}, fmt.Errorf("decode status query: %w", err)
	}
	return status, nil
}

// startActivityHeartbeat keeps agent activities alive and delivers Temporal
// cancellation while the sandbox call is blocked. Direct activity calls in
// tests do not have a Temporal context.
func startActivityHeartbeat(ctx context.Context) func() {
	if !activity.IsActivity(ctx) {
		return func() {}
	}
	activity.RecordHeartbeat(ctx)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				activity.RecordHeartbeat(ctx)
			case <-ctx.Done():
				return
			case <-stop:
				return
			}
		}
	}()
	return func() { close(stop); <-done }
}
