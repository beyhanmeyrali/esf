package sandbox

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Fake is an in-memory Provider used by unit tests.
//
// Unit tests must not require a running CubeSandbox, so every test that would
// otherwise reach the network uses this instead. It models the properties the
// factory actually depends on: create/destroy symmetry, execution outcomes,
// a filesystem, and a listable set of live sandboxes for leak detection.
type Fake struct {
	mu sync.Mutex

	// CreateErr, when set, is returned by Create.
	CreateErr error
	// DestroyErr, when set, is returned by Sandbox.Destroy for the matching ID.
	DestroyErr map[string]error
	// ExecuteFunc, when set, overrides the default execution behaviour.
	ExecuteFunc func(cmd Command) (Execution, error)
	// FailCreateAfter makes the Nth Create call (1-based) fail.
	FailCreateAfter int
	// DestroyDelay simulates a slow teardown.
	DestroyDelay time.Duration

	created  []string
	live     map[string]*FakeSandbox
	nextID   int
	createN  int
	specs    map[string]Spec
	commands map[string][]Command
}

// NewFake returns an empty fake provider.
func NewFake() *Fake {
	return &Fake{
		live:       map[string]*FakeSandbox{},
		specs:      map[string]Spec{},
		commands:   map[string][]Command{},
		DestroyErr: map[string]error{},
	}
}

func (f *Fake) Name() string { return "fake" }

func (f *Fake) Capabilities() Capabilities { return Capabilities{} }

func (f *Fake) Ping(context.Context) error { return nil }

func (f *Fake) Create(_ context.Context, spec Spec) (Sandbox, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.CreateErr != nil {
		return nil, f.CreateErr
	}
	f.createN++
	if f.FailCreateAfter > 0 && f.createN >= f.FailCreateAfter {
		return nil, fmt.Errorf("fake: create %d failed", f.createN)
	}
	f.nextID++
	id := fmt.Sprintf("fake-sandbox-%d", f.nextID)
	fs := map[string][]byte{}
	sb := &FakeSandbox{id: id, template: spec.Template, files: fs, provider: f}
	f.live[id] = sb
	f.created = append(f.created, id)
	f.specs[id] = spec
	return sb, nil
}

// Reattach returns a handle to a live sandbox.
func (f *Fake) Reattach(_ context.Context, sandboxID string) (Sandbox, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sb, ok := f.live[sandboxID]
	if !ok {
		return nil, fmt.Errorf("fake: sandbox %s not found", sandboxID)
	}
	return sb, nil
}

func (f *Fake) Destroy(_ context.Context, sandboxID string) error {
	f.mu.Lock()
	err, hasErr := f.DestroyErr[sandboxID]
	delay := f.DestroyDelay
	if _, ok := f.live[sandboxID]; !ok {
		f.mu.Unlock()
		// Idempotent: destroying an unknown sandbox is not an error.
		return nil
	}
	delete(f.live, sandboxID)
	f.mu.Unlock()
	if delay > 0 {
		time.Sleep(delay)
	}
	if hasErr {
		return err
	}
	return nil
}

func (f *Fake) List(context.Context) ([]Info, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	infos := make([]Info, 0, len(f.live))
	for _, sb := range f.live {
		infos = append(infos, Info{
			ID:         sb.id,
			Template:   sb.template,
			State:      "running",
			StartedAt:  sb.startedAt,
			Metadata:   f.specs[sb.id].Metadata,
			ProviderID: "fake",
		})
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].ID < infos[j].ID })
	return infos, nil
}

// Live returns the IDs of sandboxes still alive. Tests use it to assert that
// no sandbox leaked.
func (f *Fake) Live() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	ids := make([]string, 0, len(f.live))
	for id := range f.live {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Created returns the IDs of every sandbox ever created, in creation order.
func (f *Fake) Created() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.created...)
}

// Commands returns every command executed in the given sandbox.
func (f *Fake) Commands(sandboxID string) []Command {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Command(nil), f.commands[sandboxID]...)
}

// Spec returns the creation spec recorded for a sandbox.
func (f *Fake) Spec(sandboxID string) (Spec, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	spec, ok := f.specs[sandboxID]
	return spec, ok
}

// FakeSandbox is a live in-memory sandbox.
type FakeSandbox struct {
	mu        sync.Mutex
	id        string
	template  string
	files     map[string][]byte
	provider  *Fake
	startedAt time.Time
	destroyed bool
	destroyN  int
	execDelay time.Duration
}

func (s *FakeSandbox) ID() string       { return s.id }
func (s *FakeSandbox) Template() string { return s.template }

func (s *FakeSandbox) Execute(ctx context.Context, cmd Command) (Execution, error) {
	s.mu.Lock()
	s.provider.mu.Lock()
	_ = ctx
	if s.destroyed {
		s.provider.mu.Unlock()
		s.mu.Unlock()
		return Execution{}, fmt.Errorf("fake: sandbox %s is destroyed", s.id)
	}
	s.provider.commands[s.id] = append(s.provider.commands[s.id], cmd)
	execFunc := s.provider.ExecuteFunc
	s.provider.mu.Unlock()
	s.mu.Unlock()

	if execFunc != nil {
		return execFunc(cmd)
	}
	// Default: succeed. Tests that care about outcomes supply ExecuteFunc.
	return Execution{ExitCode: 0, StartedAt: time.Now().UTC(), CompletedAt: time.Now().UTC()}, nil
}

func (s *FakeSandbox) WriteFile(_ context.Context, path string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.destroyed {
		return fmt.Errorf("fake: sandbox %s is destroyed", s.id)
	}
	s.files[path] = append([]byte(nil), data...)
	return nil
}

func (s *FakeSandbox) ReadFile(_ context.Context, path string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.destroyed {
		return nil, fmt.Errorf("fake: sandbox %s is destroyed", s.id)
	}
	data, ok := s.files[path]
	if !ok {
		return nil, fmt.Errorf("fake: %s: no such file", path)
	}
	return append([]byte(nil), data...), nil
}

func (s *FakeSandbox) Destroy(ctx context.Context) error {
	s.mu.Lock()
	s.destroyed = true
	s.destroyN++
	s.mu.Unlock()
	return s.provider.Destroy(ctx, s.id)
}

// WriteFileString is a convenience for tests.
func (s *FakeSandbox) WriteFileString(ctx context.Context, path, content string) error {
	return s.WriteFile(ctx, path, []byte(content))
}

// FileString reads a file as a string.
func (s *FakeSandbox) FileString(ctx context.Context, path string) (string, error) {
	data, err := s.ReadFile(ctx, path)
	return string(data), err
}

// ContainsCommand reports whether any recorded command's rendered form contains
// the given substring. Useful for asserting that a factory step ran.
func (f *Fake) ContainsCommand(sandboxID, substr string) bool {
	for _, cmd := range f.Commands(sandboxID) {
		line, err := CommandLine(cmd)
		if err != nil {
			line = strings.Join(cmd.Argv, " ") + cmd.Script
		}
		if strings.Contains(line, substr) {
			return true
		}
	}
	return false
}
