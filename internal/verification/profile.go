// Package verification implements factory-owned, deterministic verification.
//
// The rule this package exists to enforce: an LLM never decides whether a build
// or a test passed. Verification is a list of structured commands with known
// exit codes, executed by the factory inside the sandbox, and the factory result
// is PASSED only when every mandatory gate exits zero.
//
// There is deliberately no `shell: "<string>"` form. A verification step is an
// argument vector, so an API client cannot smuggle shell syntax into a gate.
package verification

import (
	"fmt"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/mitkox/esf/internal/tomlx"
)

// DefaultStepTimeout bounds a single verification step when none is configured.
const DefaultStepTimeout = 10 * time.Minute

// Step is one deterministic gate.
type Step struct {
	// ID identifies the gate in evidence, for example "build" or "unit-tests".
	ID string `toml:"id"`
	// Argv is the command and arguments. This is the only execution form.
	Argv []string `toml:"argv"`
	// Dir is an optional working directory relative to the repository root.
	// It must be a relative, non-escaping path.
	Dir string `toml:"dir"`
	// Timeout bounds this step.
	Timeout tomlx.Duration `toml:"timeout"`
	// Mandatory reports whether failure fails the whole profile. A step that is
	// not mandatory is recorded but does not gate the result.
	Mandatory *bool `toml:"mandatory"`
	// Env is extra environment for this step.
	Env map[string]string `toml:"env"`
	// Description is recorded in evidence.
	Description string `toml:"description"`
}

// IsMandatory reports whether the step gates the factory result. Steps are
// mandatory by default, because an opt-out gate is the surprising case.
func (s Step) IsMandatory() bool { return s.Mandatory == nil || *s.Mandatory }

// EffectiveTimeout returns the step timeout or the default.
func (s Step) EffectiveTimeout() time.Duration {
	if d := s.Timeout.Std(); d > 0 {
		return d
	}
	return DefaultStepTimeout
}

// Profile is a named set of gates.
type Profile struct {
	// Name is the operator-facing profile name.
	Name string `toml:"name"`
	// Steps are executed in order.
	Steps []Step `toml:"steps"`
}

// ValidationError describes why a profile is unusable.
type ValidationError struct {
	Problems []string
}

func (e *ValidationError) Error() string {
	return "invalid verification profile: " + strings.Join(e.Problems, "; ")
}

// Validate rejects a profile that could not be executed deterministically.
func (p Profile) Validate() error {
	var problems []string
	if strings.TrimSpace(p.Name) == "" {
		problems = append(problems, "profile name is required")
	}
	if len(p.Steps) == 0 {
		problems = append(problems, "profile must define at least one step")
	}
	seen := map[string]struct{}{}
	for i, step := range p.Steps {
		where := fmt.Sprintf("step %d", i)
		if strings.TrimSpace(step.ID) == "" {
			problems = append(problems, where+": id is required")
		} else {
			where = fmt.Sprintf("step %q", step.ID)
			if _, dup := seen[step.ID]; dup {
				problems = append(problems, where+": duplicate id")
			}
			seen[step.ID] = struct{}{}
		}
		if len(step.Argv) == 0 {
			problems = append(problems, where+": argv is required (verification is never a shell string)")
		} else if strings.TrimSpace(step.Argv[0]) == "" {
			problems = append(problems, where+": argv[0] must not be empty")
		}
		if strings.Contains(step.Dir, "..") || strings.HasPrefix(step.Dir, "/") {
			problems = append(problems, where+": dir must be a relative path inside the repository")
		}
		if step.Timeout.Std() < 0 {
			problems = append(problems, where+": timeout must not be negative")
		}
	}
	if len(problems) > 0 {
		return &ValidationError{Problems: problems}
	}
	return nil
}

// Profiles is a named collection of profiles, loaded from operator config.
type Profiles struct {
	Profiles map[string]Profile `toml:"verification"`
}

// Load decodes verification profiles from TOML, then validates each one.
func Load(body []byte) (Profiles, error) {
	var set Profiles
	decoder := toml.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&set); err != nil {
		return Profiles{}, fmt.Errorf("parse verification profiles: %w", err)
	}
	for name, profile := range set.Profiles {
		if profile.Name == "" {
			profile.Name = name
			set.Profiles[name] = profile
		}
		if err := profile.Validate(); err != nil {
			return Profiles{}, fmt.Errorf("verification profile %q: %w", name, err)
		}
	}
	return set, nil
}

// Resolve returns the named profile.
func (p Profiles) Resolve(name string) (Profile, error) {
	profile, ok := p.Profiles[name]
	if !ok {
		names := make([]string, 0, len(p.Profiles))
		for n := range p.Profiles {
			names = append(names, n)
		}
		return Profile{}, fmt.Errorf("unknown verification profile %q", name)
	}
	return profile, nil
}
