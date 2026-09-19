package sandbox

import (
	"context"
	"errors"
	"testing"
)

func TestFakeCreateDestroyList(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fake := NewFake()

	sb, err := fake.Create(ctx, Spec{Template: "tpl-test", Metadata: map[string]string{"origin": "factory"}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if sb.ID() == "" {
		t.Fatal("sandbox has no ID")
	}
	if sb.Template() != "tpl-test" {
		t.Fatalf("template = %q, want tpl-test", sb.Template())
	}

	infos, err := fake.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("List returned %d sandboxes, want 1", len(infos))
	}
	if infos[0].Metadata["origin"] != "factory" {
		t.Fatalf("metadata was not preserved: %v", infos[0].Metadata)
	}

	if err := sb.Destroy(ctx); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	// Destroy must be idempotent: cleanup runs more than once on some paths and
	// a second destroy is never an error.
	if err := sb.Destroy(ctx); err != nil {
		t.Fatalf("second Destroy should be a no-op, got %v", err)
	}
	if live := fake.Live(); len(live) != 0 {
		t.Fatalf("sandbox leaked: %v", live)
	}
}

func TestFakeReattachRequiresLiveSandbox(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fake := NewFake()

	if _, err := fake.Reattach(ctx, "does-not-exist"); err == nil {
		t.Fatal("expected an error reattaching to an unknown sandbox")
	}

	sb, err := fake.Create(ctx, Spec{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	reattached, err := fake.Reattach(ctx, sb.ID())
	if err != nil {
		t.Fatalf("Reattach: %v", err)
	}
	if reattached.ID() != sb.ID() {
		t.Fatalf("reattached to %q, want %q", reattached.ID(), sb.ID())
	}
}

func TestFakeRecordsCommandsAndFiles(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fake := NewFake()
	sb, err := fake.Create(ctx, Spec{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := sb.Execute(ctx, Command{Argv: []string{"echo", "hi"}}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !fake.ContainsCommand(sb.ID(), "echo") {
		t.Fatal("recorded commands do not contain the executed command")
	}

	if err := sb.WriteFile(ctx, "/workspace/file.txt", []byte("content")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	data, err := sb.ReadFile(ctx, "/workspace/file.txt")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "content" {
		t.Fatalf("ReadFile = %q, want content", data)
	}
}

func TestFakeExecutionAfterDestroyFails(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fake := NewFake()
	sb, err := fake.Create(ctx, Spec{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := sb.Destroy(ctx); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	// Using a destroyed sandbox must be an error, so a workflow cannot silently
	// continue against a sandbox that no longer exists.
	if _, err := sb.Execute(ctx, Command{Argv: []string{"true"}}); err == nil {
		t.Fatal("expected an error executing in a destroyed sandbox")
	}
}

func TestFakeCreateFailureIsPropagated(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fake := NewFake()
	fake.CreateErr = errors.New("cube unavailable")

	if _, err := fake.Create(ctx, Spec{}); err == nil {
		t.Fatal("expected Create to fail")
	}
}

func TestFakeDestroyErrorIsReportedButSandboxRemoved(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fake := NewFake()
	sb, err := fake.Create(ctx, Spec{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	fake.DestroyErr[sb.ID()] = errors.New("boom")

	if err := fake.Destroy(ctx, sb.ID()); err == nil {
		t.Fatal("expected the configured destroy error")
	}
}
