// Package tomlx holds small helpers for decoding operator configuration.
//
// It exists because TOML has no duration type: `timeout = "30m"` is a string,
// and encoding/json-style decoding into time.Duration fails. Rather than
// leaving configuration as bare strings (which defers every typo to run time),
// Duration decodes with a clear error at startup.
package tomlx

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Duration is a time.Duration that decodes from a TOML string such as "30m" or
// from a bare number of seconds.
type Duration time.Duration

// UnmarshalText implements encoding.TextUnmarshaler.
func (d *Duration) UnmarshalText(text []byte) error {
	value := strings.TrimSpace(string(text))
	if value == "" {
		*d = 0
		return nil
	}
	// Accept the Go duration syntax first: "30m", "1h30m", "500ms".
	if parsed, err := time.ParseDuration(value); err == nil {
		*d = Duration(parsed)
		return nil
	}
	// Fall back to a bare number, interpreted as seconds, which is what the
	// upstream Cube SDK has historically accepted.
	if seconds, err := strconv.ParseFloat(value, 64); err == nil {
		*d = Duration(time.Duration(seconds * float64(time.Second)))
		return nil
	}
	return fmt.Errorf("invalid duration %q: use a Go duration such as \"30m\" or a number of seconds", value)
}

// MarshalText implements encoding.TextMarshaler.
func (d Duration) MarshalText() ([]byte, error) {
	return []byte(time.Duration(d).String()), nil
}

// Std converts to a time.Duration.
func (d Duration) Std() time.Duration { return time.Duration(d) }

// FromStd converts a time.Duration.
func FromStd(d time.Duration) Duration { return Duration(d) }

// String renders the duration.
func (d Duration) String() string { return time.Duration(d).String() }
