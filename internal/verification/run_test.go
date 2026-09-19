package verification

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mitkox/esf/internal/sandbox"
	"github.com/mitkox/esf/internal/tomlx"
)

func boolPtr(v bool) *bool { return &v }

func TestProfileValidate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		profile Profile
		wantErr bool
	}{
		{
			name:    "valid",
			profile: Profile{Name: "p", Steps: []Step{{ID: "build", Argv: []string{"./build.sh"}}}},
		},
		{
			name:    "missing name",
			profile: Profile{Steps: []Step{{ID: "build", Argv: []string{"./build.sh"}}}},
			wantErr: true,
		},
		{
			name:    "no steps",
			profile: Profile{Name: "p"},
			wantErr: true,
		},
		{
			// Verification is never a shell string: an argv is mandatory.
			name:    "missing argv",
			profile: Profile{Name: "p", Steps: []Step{{ID: "build"}}},
			wantErr: true,
		},
		{
			name:    "duplicate step id",
			profile: Profile{Name: "p", Steps: []Step{{ID: "b", Argv: []string{"x"}}, {ID: "b", Argv: []string{"y"}}}},
			wantErr: true,
		},
		{
			name:    "absolute dir",
			profile: Profile{Name: "p", Steps: []Step{{ID: "b", Argv: []string{"x"}, Dir: "/etc"}}},
			wantErr: true,
		},
		{
			name:    "escaping dir",
			profile: Profile{Name: "p", Steps: []Step{{ID: "b", Argv: []string{"x"}, Dir: "../../etc"}}},
			wantErr: true,
		},
		{
			name:    "negative timeout",
			profile: Profile{Name: "p", Steps: []Step{{ID: "b", Argv: []string{"x"}, Timeout: tomlx.FromStd(-time.Second)}}},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.profile.Validate()
			if tc.wantErr && err == nil {
				t.Fatal("expected a validation error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected validation error: %v", err)
			}
		})
	}
}

func TestStepMandatoryDefaultsToTrue(t *testing.T) {
	t.Parallel()
	if !(Step{ID: "a", Argv: []string{"x"}}).IsMandatory() {
		t.Fatal("a step with no explicit flag must be mandatory: an opt-out gate is the surprising case")
	}
	if (Step{ID: "a", Argv: []string{"x"}, Mandatory: boolPtr(false)}).IsMandatory() {
		t.Fatal("an explicit false must make the step advisory")
	}
}

// TestRunnerVerdictComesFromExitCodesOnly is the central property of the
// verification layer: no output content can change a pass into a fail or the
// reverse.
func TestRunnerVerdictComesFromExitCodesOnly(t *testing.T) {
	t.Parallel()

	profile := Profile{Name: "default", Steps: []Step{
		{ID: "build", Argv: []string{"./build.sh"}},
		{ID: "tests", Argv: []string{"./test.sh"}},
	}}

	// The second gate fails; the first prints alarming text but exits zero.
	fake := sandbox.NewFake()
	fake.ExecuteFunc = func(cmd sandbox.Command) (sandbox.Execution, error) {
		if cmd.Argv[0] == "./build.sh" {
			return sandbox.Execution{ExitCode: 0, Stdout: "ERROR: everything is broken"}, nil
		}
		return sandbox.Execution{ExitCode: 1, Stdout: "tests passed", Stderr: ""}, nil
	}
	sb, _ := fake.Create(context.Background(), sandbox.Spec{})

	result, err := (&Runner{}).Run(context.Background(), sb, profile, "/repo")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Passed {
		t.Fatal("a failing mandatory gate must fail the profile regardless of printed text")
	}
	if got := result.FailingSteps(); len(got) != 1 || got[0] != "tests" {
		t.Fatalf("FailingSteps() = %v, want [tests]", got)
	}

	// Both exit zero: the profile passes even though the output looks alarming.
	fake2 := sandbox.NewFake()
	fake2.ExecuteFunc = func(sandbox.Command) (sandbox.Execution, error) {
		return sandbox.Execution{ExitCode: 0, Stdout: "ALL TESTS FAILED"}, nil
	}
	sb2, _ := fake2.Create(context.Background(), sandbox.Spec{})
	ok, err := (&Runner{}).Run(context.Background(), sb2, profile, "/repo")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !ok.Passed {
		t.Fatal("zero exit codes must pass the profile regardless of printed text")
	}
}

func TestRunnerAdvisoryStepDoesNotGate(t *testing.T) {
	t.Parallel()
	profile := Profile{Name: "p", Steps: []Step{
		{ID: "optional-lint", Argv: []string{"lint"}, Mandatory: boolPtr(false)},
		{ID: "build", Argv: []string{"build"}},
	}}
	fake := sandbox.NewFake()
	fake.ExecuteFunc = func(cmd sandbox.Command) (sandbox.Execution, error) {
		if cmd.Argv[0] == "lint" {
			return sandbox.Execution{ExitCode: 3}, nil
		}
		return sandbox.Execution{ExitCode: 0}, nil
	}
	sb, _ := fake.Create(context.Background(), sandbox.Spec{})

	result, err := (&Runner{}).Run(context.Background(), sb, profile, "/repo")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !result.Passed {
		t.Fatal("an advisory gate must not fail the profile")
	}
}

// TestRunnerExecutionErrorFailsTheGate ensures an infrastructure problem while
// running a gate can never be mistaken for a pass.
func TestRunnerExecutionErrorFailsTheGate(t *testing.T) {
	t.Parallel()
	profile := Profile{Name: "p", Steps: []Step{{ID: "build", Argv: []string{"build"}}}}
	fake := sandbox.NewFake()
	fake.ExecuteFunc = func(sandbox.Command) (sandbox.Execution, error) {
		return sandbox.Execution{}, errors.New("sandbox unreachable")
	}
	sb, _ := fake.Create(context.Background(), sandbox.Spec{})

	result, err := (&Runner{}).Run(context.Background(), sb, profile, "/repo")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Passed {
		t.Fatal("a gate that could not run must not pass")
	}
	if result.Steps[0].Error == "" {
		t.Fatal("the execution error must be recorded")
	}
}

func TestRunnerRunsEveryStepEvenAfterAFailure(t *testing.T) {
	t.Parallel()
	profile := Profile{Name: "p", Steps: []Step{
		{ID: "one", Argv: []string{"one"}},
		{ID: "two", Argv: []string{"two"}},
		{ID: "three", Argv: []string{"three"}},
	}}
	fake := sandbox.NewFake()
	fake.ExecuteFunc = func(sandbox.Command) (sandbox.Execution, error) {
		return sandbox.Execution{ExitCode: 1}, nil
	}
	sb, _ := fake.Create(context.Background(), sandbox.Spec{})

	result, err := (&Runner{}).Run(context.Background(), sb, profile, "/repo")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(result.Steps) != 3 {
		t.Fatalf("expected all 3 gates to be evaluated, got %d", len(result.Steps))
	}
}

func TestRunnerPersistsOutputThroughSink(t *testing.T) {
	t.Parallel()
	sink := &recordingSink{}
	profile := Profile{Name: "p", Steps: []Step{{ID: "build", Argv: []string{"build"}}}}
	fake := sandbox.NewFake()
	fake.ExecuteFunc = func(sandbox.Command) (sandbox.Execution, error) {
		return sandbox.Execution{ExitCode: 0, Stdout: "out", Stderr: "err"}, nil
	}
	sb, _ := fake.Create(context.Background(), sandbox.Spec{})

	result, err := (&Runner{Sink: sink}).Run(context.Background(), sb, profile, "/repo")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Steps[0].StdoutArtifact != "verification/build.stdout" {
		t.Fatalf("stdout artifact = %q", result.Steps[0].StdoutArtifact)
	}
	if sink.written["verification/build.stdout"] != "out" {
		t.Fatalf("stdout was not persisted: %v", sink.written)
	}
	// Evidence is written for passing gates too: a green build nobody can
	// inspect is not evidence.
	if sink.written["verification/build.stderr"] != "err" {
		t.Fatalf("stderr was not persisted: %v", sink.written)
	}
}

type recordingSink struct {
	written map[string]string
}

func (s *recordingSink) Write(relPath string, data []byte) error {
	if s.written == nil {
		s.written = map[string]string{}
	}
	s.written[relPath] = string(data)
	return nil
}

func TestStepTimeoutDefaultsWhenUnset(t *testing.T) {
	t.Parallel()
	if got := (Step{}).EffectiveTimeout(); got != DefaultStepTimeout {
		t.Fatalf("EffectiveTimeout() = %v, want %v", got, DefaultStepTimeout)
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	t.Parallel()
	body := []byte(`
[verification.default]
name = "default"
surprise = true

[[verification.default.steps]]
id = "build"
argv = ["./build.sh"]
`)
	if _, err := Load(body); err == nil {
		t.Fatal("expected an unknown field to be rejected")
	}
}
