package sandbox

import (
	"fmt"
	"strings"
)

// QuoteArgv renders a structured argument vector as a single POSIX shell
// command string in which no argument can be interpreted as shell syntax.
//
// This exists because some sandbox data planes (CubeSandbox's envd
// `process.Process/Start`) accept a command *string* that is executed by
// `/bin/bash -lc`. The factory's interface is deliberately structured (`Argv`)
// so that task text, repository content, or any other untrusted value can never
// become shell syntax. This function is the single, tested place where that
// guarantee is enforced.
//
// The escaping rule is the conservative POSIX one: wrap the argument in single
// quotes and replace every embedded single quote with `'\”`. Inside single
// quotes the shell treats every byte literally, so command substitution,
// variable expansion, redirection, pipelines and globbing are all inert.
//
// It returns an error for an empty argument vector or an empty program name,
// because silently producing an empty command would be worse than failing.
func QuoteArgv(argv []string) (string, error) {
	if len(argv) == 0 {
		return "", fmt.Errorf("sandbox: empty argument vector")
	}
	if strings.TrimSpace(argv[0]) == "" {
		return "", fmt.Errorf("sandbox: empty program name")
	}
	quoted := make([]string, 0, len(argv))
	for _, arg := range argv {
		quoted = append(quoted, quoteOne(arg))
	}
	return strings.Join(quoted, " "), nil
}

func quoteOne(arg string) string {
	if arg == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
}

// CommandLine renders a Command into the shell string a shell-based provider
// must execute.
//
// A Command with Argv set is quoted through QuoteArgv. A Command with Script
// set is passed through verbatim: Script is reserved for factory-owned setup
// code, and callers must never populate it from untrusted input.
// CommandLine renders a Command into the shell string a shell-based provider
// must execute.
//
// A Command with Argv set is quoted through QuoteArgv. A Command with Script
// set is passed through verbatim: Script is reserved for factory-owned setup
// code, and callers must never populate it from untrusted input.
//
// A Command with StdinPath set gains a `< 'path'` redirection. Only the path is
// rendered — never the file's contents — so delivering a large or untrusted
// prompt cannot inject shell syntax.
func CommandLine(cmd Command) (string, error) {
	var line string
	switch {
	case len(cmd.Argv) > 0 && cmd.Script != "":
		return "", fmt.Errorf("sandbox: Command sets both Argv and Script")
	case len(cmd.Argv) > 0:
		rendered, err := QuoteArgv(cmd.Argv)
		if err != nil {
			return "", err
		}
		line = rendered
	case cmd.Script != "":
		line = cmd.Script
	default:
		return "", fmt.Errorf("sandbox: Command has neither Argv nor Script")
	}

	if cmd.StdinPath != "" {
		if !strings.HasPrefix(cmd.StdinPath, "/") {
			return "", fmt.Errorf("sandbox: StdinPath %q must be absolute", cmd.StdinPath)
		}
		line += " < " + quoteOne(cmd.StdinPath)
	}
	return line, nil
}
