package cube

import (
	"errors"
	"fmt"

	cubesandbox "github.com/tencentcloud/CubeSandbox/sdk/go"
)

// Provider-level sentinel errors.
//
// The factory maps these onto its own outcome states, so workflow code never
// imports the Cube SDK and never pattern-matches on vendor error strings.
var (
	// ErrUnavailable means the Cube control plane could not be reached.
	ErrUnavailable = errors.New("cubesandbox: control plane unavailable")
	// ErrSandboxNotFound means the sandbox no longer exists.
	ErrSandboxNotFound = errors.New("cubesandbox: sandbox not found")
	// ErrTemplateNotFound means the configured template does not exist.
	ErrTemplateNotFound = errors.New("cubesandbox: template not found")
	// ErrAuthentication means the Cube API rejected our credentials.
	ErrAuthentication = errors.New("cubesandbox: authentication failed")
	// ErrInvalidSpec means the factory asked for something Cube cannot accept.
	ErrInvalidSpec = errors.New("cubesandbox: invalid sandbox specification")
)

// classify maps a Cube SDK error onto a provider sentinel.
//
// The SDK already exposes typed sentinels; this function keeps the mapping in
// one place so the rest of the factory has a single, vendor-independent
// vocabulary. Unknown errors are wrapped rather than flattened so the original
// message survives into the activity error.
func classify(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, cubesandbox.ErrSandboxNotFound):
		return fmt.Errorf("%w: %v", ErrSandboxNotFound, err)
	case errors.Is(err, cubesandbox.ErrTemplateNotFound):
		return fmt.Errorf("%w: %v", ErrTemplateNotFound, err)
	case errors.Is(err, cubesandbox.ErrAuthentication):
		return fmt.Errorf("%w: %v", ErrAuthentication, err)
	}

	var apiErr *cubesandbox.APIError
	if errors.As(err, &apiErr) {
		if apiErr.StatusCode >= 500 {
			return fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
		if apiErr.StatusCode == 400 || apiErr.StatusCode == 422 {
			return fmt.Errorf("%w: %v", ErrInvalidSpec, err)
		}
	}
	return err
}
