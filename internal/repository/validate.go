package repository

import (
	"fmt"
	"net/url"
	"strings"
)

// Request validation is a security boundary.
//
// A task or API caller may name an APPROVED repository at an APPROVED revision.
// It may not name an arbitrary host path, an arbitrary URL scheme, or a URL that
// carries embedded credentials.

// validate checks the request against operator policy.
func (r Request) validate(allowed []string) error {
	switch r.Kind {
	case SourceRemote:
		return r.validateRemote(allowed)
	case SourceLocal:
		return r.validateLocal()
	default:
		return fmt.Errorf("%w: unknown source kind %q", ErrRepositoryRejected, r.Kind)
	}
}

func (r Request) validateRemote(allowed []string) error {
	raw := strings.TrimSpace(r.URL)
	if raw == "" {
		return fmt.Errorf("%w: repository URL is required", ErrRepositoryRejected)
	}
	if err := ValidateRevision(r.Revision); err != nil {
		return err
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%w: malformed repository URL: %v", ErrRepositoryRejected, err)
	}
	// Only network git transports are permitted. `file://` and bare paths are
	// rejected because they would let a caller read the factory host.
	switch strings.ToLower(parsed.Scheme) {
	case "https", "ssh", "git":
	default:
		return fmt.Errorf("%w: URL scheme %q is not allowed (use https or ssh)", ErrRepositoryRejected, parsed.Scheme)
	}
	if parsed.Host == "" {
		return fmt.Errorf("%w: repository URL must include a host", ErrRepositoryRejected)
	}
	// Credentials must never travel inside a URL: they end up in process
	// listings, logs, and evidence.
	if parsed.User != nil {
		return fmt.Errorf("%w: repository URL must not embed credentials", ErrRepositoryRejected)
	}
	if len(allowed) == 0 {
		return fmt.Errorf("%w: no repositories are approved for remote access", ErrRepositoryRejected)
	}
	for _, prefix := range allowed {
		if strings.HasPrefix(raw, prefix) {
			return nil
		}
	}
	return fmt.Errorf("%w: repository %q is not in the approved list", ErrRepositoryRejected, raw)
}

func (r Request) validateLocal() error {
	if strings.TrimSpace(r.LocalPath) == "" {
		return fmt.Errorf("%w: local repository path is required", ErrRepositoryRejected)
	}
	if err := ValidateRevision(r.Revision); err != nil {
		return err
	}
	return nil
}

// ValidateRevision rejects a revision that is empty or looks like an option.
//
// The revision is passed as a structured argv element, so this is defence in
// depth rather than the primary control.
func ValidateRevision(revision string) error {
	trimmed := strings.TrimSpace(revision)
	if trimmed == "" {
		return fmt.Errorf("%w: revision is required (runs must be reproducible)", ErrRepositoryRejected)
	}
	if strings.HasPrefix(trimmed, "-") {
		return fmt.Errorf("%w: revision %q must not begin with '-'", ErrRepositoryRejected, revision)
	}
	if strings.ContainsAny(trimmed, "\x00\n\r") {
		return fmt.Errorf("%w: revision contains control characters", ErrRepositoryRejected)
	}
	if len(trimmed) > 256 {
		return fmt.Errorf("%w: revision is unreasonably long", ErrRepositoryRejected)
	}
	return nil
}
