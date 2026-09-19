// Command cube-smoke is a disposable connectivity spike that proves the
// factory host can talk to the already-running CubeSandbox installation.
//
// It is deliberately standalone: it does not import any factory package, so it
// stays useful even if the factory layer is broken. It exits non-zero on any
// failed step so it can be wired directly into CI and `make cube-smoke`.
//
// Usage:
//
//	CUBE_API_URL=http://127.0.0.1:4000 \
//	CUBE_TEMPLATE_ID=tpl-... \
//	go run ./tools/cube-smoke
//
// Flags:
//
//	-probe  additionally report what the sandbox image contains and which
//	        network destinations it can reach (discovery only, never fatal).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	cubesandbox "github.com/tencentcloud/CubeSandbox/sdk/go"
)

const (
	// defaultAPIURL matches the discovered deployment on this host. It is a
	// default, not an assumption: CUBE_API_URL always wins.
	defaultAPIURL = "http://127.0.0.1:4000"

	expectedEcho = "factory-cube-ok"
)

type check struct {
	name   string
	passed bool
	detail string
}

type smoke struct {
	checks []check
	probes []check
}

func (s *smoke) pass(name, detail string) {
	s.checks = append(s.checks, check{name: name, passed: true, detail: detail})
	fmt.Printf("  [ok]   %-28s %s\n", name, detail)
}

func (s *smoke) fail(name, detail string) {
	s.checks = append(s.checks, check{name: name, passed: false, detail: detail})
	fmt.Printf("  [FAIL] %-28s %s\n", name, detail)
}

func (s *smoke) probe(name, detail string) {
	s.probes = append(s.probes, check{name: name, detail: detail})
}

func (s *smoke) failed() bool {
	for _, c := range s.checks {
		if !c.passed {
			return true
		}
	}
	return false
}

func main() {
	probe := flag.Bool("probe", true, "report sandbox image contents and network reachability")
	keepOnFailure := flag.Bool("keep-on-failure", false, "leave the sandbox running on failure for debugging")
	timeout := flag.Duration("timeout", 4*time.Minute, "overall timeout")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	if err := run(ctx, *probe, *keepOnFailure); err != nil {
		fmt.Fprintf(os.Stderr, "\nCUBESANDBOX INTEGRATION FAILED: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, probe, keepOnFailure bool) (returnErr error) {
	apiURL := envOr("CUBE_API_URL", defaultAPIURL)
	templateID := strings.TrimSpace(os.Getenv("CUBE_TEMPLATE_ID"))

	fmt.Println("Cube endpoint: ", apiURL)
	if templateID == "" {
		return errors.New("CUBE_TEMPLATE_ID is not set")
	}
	fmt.Println("Template:      ", templateID)
	fmt.Println()

	s := &smoke{}

	cfg := cubesandbox.NewConfigFromEnv()
	cfg.APIURL = apiURL
	cfg.TemplateID = templateID
	client := cubesandbox.NewClient(cfg)
	defer client.Close()

	// --- 1. reachability -------------------------------------------------
	before, err := client.List(ctx)
	if err != nil {
		s.fail("endpoint", err.Error())
		return errors.New("cannot reach Cube API")
	}
	s.pass("endpoint reachable", fmt.Sprintf("health ok, %d sandbox(es) present", len(before)))

	// Refuse to run if the API has no template inventory at all: that means we
	// are pointed at the wrong service.
	if _, err := client.ListTemplates(ctx); err != nil {
		s.fail("template inventory", err.Error())
		return errors.New("cannot list templates")
	}

	// --- 2. create -------------------------------------------------------
	startedCreate := time.Now()
	sandbox, err := client.Create(ctx, cubesandbox.CreateOptions{
		TemplateID: templateID,
		Timeout:    cubesandbox.DurationPtr(10 * time.Minute),
		Metadata:   map[string]string{"origin": "factory-cube-smoke", "purpose": "connectivity-spike"},
	})
	if err != nil {
		s.fail("sandbox create", err.Error())
		return errors.New("sandbox creation failed")
	}
	s.pass("sandbox created", fmt.Sprintf("id=%s in %s", sandbox.SandboxID, time.Since(startedCreate).Round(time.Millisecond)))

	// Cleanup is registered immediately after creation and is guaranteed to run
	// even on panic or early return. A leaked VM is a failed integration test.
	cleanedUp := false
	destroy := func() error {
		if cleanedUp {
			return nil
		}
		cleanedUp = true
		killCtx, killCancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer killCancel()
		return sandbox.Kill(killCtx)
	}
	defer func() {
		if returnErr != nil && keepOnFailure {
			fmt.Printf("\n  [debug] leaving sandbox %s running (--keep-on-failure)\n", sandbox.SandboxID)
			return
		}
		if err := destroy(); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("destroy sandbox: %w", err))
			fmt.Printf("  [FAIL] %-28s %s\n", "sandbox destroy", err)
		}
	}()

	// --- 3. execute echo -------------------------------------------------
	echo, err := sandbox.Commands().Run(ctx, "echo "+expectedEcho, cubesandbox.CommandOptions{})
	if err != nil {
		s.fail("command execution", err.Error())
		return errors.New("command execution failed")
	}
	if strings.TrimSpace(echo.Stdout) != expectedEcho || echo.ExitCode != 0 {
		s.fail("echo", fmt.Sprintf("exit=%d stdout=%q stderr=%q", echo.ExitCode, echo.Stdout, echo.Stderr))
		return fmt.Errorf("expected stdout %q", expectedEcho)
	}
	s.pass("command execution", fmt.Sprintf("exit=0 stdout=%q", strings.TrimSpace(echo.Stdout)))

	// --- 4. uname -a -----------------------------------------------------
	uname, err := sandbox.Commands().Run(ctx, "uname -a", cubesandbox.CommandOptions{})
	if err != nil {
		s.fail("uname", err.Error())
		return errors.New("uname failed")
	}
	if uname.ExitCode != 0 || strings.TrimSpace(uname.Stdout) == "" {
		s.fail("uname", fmt.Sprintf("exit=%d", uname.ExitCode))
		return errors.New("uname failed")
	}
	s.pass("command execution (2)", strings.TrimSpace(uname.Stdout))

	// --- 5. stderr separation -------------------------------------------
	stderrCheck, err := sandbox.Commands().Run(ctx, "echo to-stdout; echo to-stderr 1>&2; exit 7", cubesandbox.CommandOptions{})
	if err != nil {
		s.fail("stderr capture", err.Error())
		return errors.New("stderr capture failed")
	}
	if stderrCheck.ExitCode != 7 || !strings.Contains(stderrCheck.Stdout, "to-stdout") || !strings.Contains(stderrCheck.Stderr, "to-stderr") {
		s.fail("stderr capture", fmt.Sprintf("exit=%d stdout=%q stderr=%q", stderrCheck.ExitCode, stderrCheck.Stdout, stderrCheck.Stderr))
		return errors.New("stderr capture failed")
	}
	s.pass("stdout/stderr split", "exit code and both streams preserved")

	// --- 6. optional environment discovery -------------------------------
	if probe {
		discover(ctx, sandbox, s)
	}

	// --- 7. destroy + verify cleanup -------------------------------------
	if err := destroy(); err != nil {
		s.fail("sandbox destroy", err.Error())
		return errors.New("sandbox destroy failed")
	}
	s.pass("sandbox destroyed", sandbox.SandboxID)

	after, err := client.List(ctx)
	if err != nil {
		s.fail("cleanup verification", err.Error())
		return errors.New("cleanup verification failed")
	}
	if len(after) != len(before) {
		s.fail("cleanup verification", fmt.Sprintf("sandbox count changed: before=%d after=%d", len(before), len(after)))
		return errors.New("sandbox cleanup verification failed")
	}
	for _, info := range after {
		if info.SandboxID == sandbox.SandboxID {
			s.fail("cleanup verification", "sandbox still present after destroy")
			return errors.New("sandbox leaked")
		}
	}
	s.pass("cleanup verified", fmt.Sprintf("%d sandbox(es), none leaked", len(after)))

	fmt.Println()
	fmt.Println("CUBESANDBOX INTEGRATION OK")
	return nil
}

// discover reports what the sandbox image provides. The probe command is a
// single fixed script; none of its output affects the smoke test outcome.
func discover(ctx context.Context, sandbox *cubesandbox.Sandbox, s *smoke) {
	const script = `
for tool in git node npm python3 curl jq bash uname; do
  if command -v "$tool" >/dev/null 2>&1; then
    printf '%s=%s\n' "$tool" "$("$tool" --version 2>&1 | head -1)"
  else
    printf '%s=MISSING\n' "$tool"
  fi
done
printf 'os=%s\n' "$(cat /etc/os-release 2>/dev/null | sed -n 's/^PRETTY_NAME=//p' | tr -d '"')"
printf 'user=%s\n' "$(id -un)"
printf 'cwd=%s\n' "$(pwd)"
printf 'workspace=%s\n' "$(test -d /workspace && echo exists || echo missing)"
printf 'disk=%s\n' "$(df -h / | awk 'NR==2{print $2" total, "$4" avail"}')"
printf 'cpucount=%s\n' "$(nproc 2>/dev/null || echo unknown)"
printf 'mem=%s\n' "$(awk '/MemTotal/{printf "%.1fGiB", $2/1048576}' /proc/meminfo 2>/dev/null)"
printf 'ip=%s\n' "$(hostname -I 2>/dev/null | awk '{print $1}')"
printf 'egress_host_gateway=%s\n' "$(curl -s -m 5 -o /dev/null -w '%{http_code}' http://192.0.2.10:8089/ 2>/dev/null || echo unreachable)"
printf 'egress_proxy=%s\n' "$(curl -s -m 5 -o /dev/null -w '%{http_code}' http://192.0.2.10:8082/ 2>/dev/null || echo unreachable)"
printf 'dns=%s\n' "$(getent hosts github.com 2>/dev/null | head -1 || echo unresolvable)"
printf 'internet=%s\n' "$(curl -s -m 8 -o /dev/null -w '%{http_code}' https://github.com 2>/dev/null || echo unreachable)"
`
	exec, err := sandbox.Commands().Run(ctx, script, cubesandbox.CommandOptions{})
	if err != nil {
		s.probe("environment probe", "failed: "+err.Error())
		return
	}

	lines := strings.Split(strings.TrimSpace(exec.Stdout), "\n")
	sort.Strings(lines)
	fmt.Println()
	fmt.Println("  -- sandbox image probe (informational) --")
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fmt.Printf("     %s\n", line)
		s.probe(line, "")
	}
	if strings.TrimSpace(exec.Stderr) != "" {
		fmt.Printf("     (stderr) %s\n", strings.TrimSpace(exec.Stderr))
	}
	fmt.Println()
}

func envOr(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return fallback
}
