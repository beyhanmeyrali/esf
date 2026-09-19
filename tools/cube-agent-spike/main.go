// Command cube-agent-spike de-risks getting a coding agent running INSIDE a
// CubeSandbox microVM. It answers, with measurements:
//
//  1. how long `git` takes to install from the sandbox's own egress,
//  2. whether the standalone opencode2 ELF binary can be pushed into the
//     sandbox and executes there,
//  3. whether the sandbox can actually reach a model endpoint.
//
// It is a spike: it creates and destroys its own sandbox and never mutates
// the deployment.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	cubesandbox "github.com/tencentcloud/CubeSandbox/sdk/go"
)

func main() {
	localBinary := flag.String("opencode-binary", os.Getenv("OPENCODE_BINARY"), "host path to the opencode2 standalone binary")
	apiURL := flag.String("api-url", envOr("CUBE_API_URL", "http://127.0.0.1:4000"), "Cube API URL")
	templateID := flag.String("template", os.Getenv("CUBE_TEMPLATE_ID"), "Cube template ID")
	skipPush := flag.Bool("skip-push", false, "skip pushing the local opencode binary")
	e2e := flag.Bool("e2e", false, "run a full in-sandbox agent task against a throwaway python repo")
	execCmd := flag.String("exec", "", "run this exact command in a fresh sandbox (with the agent binary pushed) and print the result")
	seedSpec := flag.String("seed", "", "extra file to stage: \"hostPath:sandboxPath\" (repeatable via comma-separated pairs)")
	model := flag.String("model", envOr("OPENCODE_MODEL", "opencode-go/deepseek-v4.1-flash"), "model to drive the agent")
	flag.Parse()

	if *execCmd != "" {
		if err := runExec(*execCmd, *localBinary, *apiURL, *templateID, *seedSpec); err != nil {
			fmt.Fprintf(os.Stderr, "\nEXEC FAILED: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if err := run(*localBinary, *apiURL, *templateID, *skipPush, *e2e, *model); err != nil {
		fmt.Fprintf(os.Stderr, "\nAGENT SPIKE FAILED: %v\n", err)
		os.Exit(1)
	}
}

func run(localBinary, apiURL, templateID string, skipPush, e2e bool, model string) error {
	if templateID == "" {
		return errors.New("CUBE_TEMPLATE_ID is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	cfg := cubesandbox.NewConfigFromEnv()
	cfg.APIURL = apiURL
	cfg.TemplateID = templateID
	client := cubesandbox.NewClient(cfg)
	defer client.Close()

	before, err := client.List(ctx)
	if err != nil {
		return err
	}

	sandbox, err := client.Create(ctx, cubesandbox.CreateOptions{
		TemplateID: templateID,
		Timeout:    cubesandbox.DurationPtr(10 * time.Minute),
		Metadata:   map[string]string{"origin": "factory-agent-spike"},
	})
	if err != nil {
		return fmt.Errorf("create sandbox: %w", err)
	}
	fmt.Printf("sandbox: %s\n\n", sandbox.SandboxID)

	var cleanupErr error
	defer func() {
		killCtx, c := context.WithTimeout(context.Background(), 90*time.Second)
		defer c()
		if err := sandbox.Kill(killCtx); err != nil {
			cleanupErr = fmt.Errorf("destroy: %w", err)
			return
		}
		after, err := client.List(killCtx)
		if err != nil {
			cleanupErr = err
			return
		}
		if len(after) != len(before) {
			cleanupErr = fmt.Errorf("leak: sandbox count %d -> %d", len(before), len(after))
		}
	}()

	// --- 1. toolchain provisioning -------------------------------------
	step("apt-get update + install git/ca-certificates", func() error {
		out, err := sandbox.Commands().Run(ctx,
			"DEBIAN_FRONTEND=noninteractive apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends git ca-certificates >/dev/null 2>&1; git --version",
			cubesandbox.CommandOptions{Timeout: 5 * time.Minute})
		if err != nil {
			return err
		}
		if out.ExitCode != 0 {
			return fmt.Errorf("exit=%d stderr=%s", out.ExitCode, truncate(out.Stderr, 400))
		}
		fmt.Printf("        %s\n", strings.TrimSpace(out.Stdout))
		return nil
	})

	// --- 2. workspace + repo -------------------------------------------
	step("prepare /workspace/repository (git init)", func() error {
		out, err := sandbox.Commands().Run(ctx,
			"mkdir -p /workspace/repository && cd /workspace/repository && git init -q && git config user.email factory@local && git config user.name factory && echo ok",
			cubesandbox.CommandOptions{})
		if err != nil {
			return err
		}
		if out.ExitCode != 0 {
			return fmt.Errorf("exit=%d stderr=%s", out.ExitCode, out.Stderr)
		}
		return nil
	})

	// --- 3. push the agent binary --------------------------------------
	if !skipPush {
		step(fmt.Sprintf("push opencode2 binary (%s) via envd files API", localBinary), func() error {
			data, err := os.ReadFile(localBinary)
			if err != nil {
				return err
			}
			fmt.Printf("        size=%d bytes, %.1f MiB\n", len(data), float64(len(data))/(1<<20))
			start := time.Now()
			if err := sandbox.Files().Write(ctx, "/usr/local/bin/opencode2", data); err != nil {
				return err
			}
			fmt.Printf("        transfer=%s (%.1f MiB/s)\n", time.Since(start).Round(time.Millisecond),
				float64(len(data))/(1<<20)/time.Since(start).Seconds())
			out, err := sandbox.Commands().Run(ctx, "chmod +x /usr/local/bin/opencode2 && /usr/local/bin/opencode2 --version", cubesandbox.CommandOptions{Timeout: 90 * time.Second})
			if err != nil {
				return err
			}
			fmt.Printf("        version output: exit=%d stdout=%q stderr=%q\n", out.ExitCode, strings.TrimSpace(out.Stdout), truncate(strings.TrimSpace(out.Stderr), 300))
			if out.ExitCode != 0 {
				return errors.New("opencode2 did not execute in the sandbox")
			}
			return nil
		})
	}

	// --- 4. model endpoint reachability --------------------------------
	step("model endpoint reachability from sandbox", func() error {
		// Each candidate is probed read-only; failures are informational.
		candidates := []struct{ name, url string }{
			{"host llama-server (192.0.2.10:8000)", "http://192.0.2.10:8000/v1/models"},
			{"host llama-server (127.0.0.1:8000)", "http://127.0.0.1:8000/v1/models"},
			{"tailscale host (100.126.136.61:8000)", "http://100.126.136.61:8000/v1/models"},
			{"public (api.github.com)", "https://api.github.com"},
			{"public (registry.npmjs.org)", "https://registry.npmjs.org/-/ping"},
		}
		for _, c := range candidates {
			cmd := fmt.Sprintf(`code=$(curl -s -m 8 -o /dev/null -w '%%{http_code}' %q 2>/dev/null); echo "$code"`, c.url)
			out, err := sandbox.Commands().Run(ctx, cmd, cubesandbox.CommandOptions{})
			if err != nil {
				fmt.Printf("        %-42s error=%v\n", c.name, err)
				continue
			}
			fmt.Printf("        %-42s http=%s\n", c.name, strings.TrimSpace(out.Stdout))
		}
		return nil
	})

	step("resources", func() error {
		out, err := sandbox.Commands().Run(ctx, "nproc; free -m | head -2; df -h / | tail -1", cubesandbox.CommandOptions{})
		if err != nil {
			return err
		}
		fmt.Print(prepend(strings.TrimRight(out.Stdout, "\n"), "        "))
		return nil
	})

	if e2e {
		step("E2E: agent modifies a repo inside the sandbox", func() error {
			return runAgentE2E(ctx, sandbox, model)
		})
	}

	if cleanupErr != nil {
		return fmt.Errorf("cleanup: %w", cleanupErr)
	}
	fmt.Println("\nAGENT SPIKE OK")
	return nil
}

// runAgentE2E proves the whole in-sandbox loop: baseline, agent edit,
// deterministic verification, and patch extraction.
func runAgentE2E(ctx context.Context, sandbox *cubesandbox.Sandbox, model string) error {
	run := func(cmd string, timeout time.Duration) (*cubesandbox.CommandResult, error) {
		return sandbox.Commands().Run(ctx, cmd, cubesandbox.CommandOptions{Timeout: timeout})
	}

	// A throwaway python repo. python3 is present in the base image, so the
	// verification gate needs no package installation.
	setup := `
set -e
rm -rf /workspace/repository
mkdir -p /workspace/repository
cd /workspace/repository
git init -q
git config user.email factory@local
git config user.name factory
cat > greeting.py <<'PY'
def greeting():
    return "hello"
PY
cat > test_greeting.py <<'PY'
from greeting import greeting

assert greeting() == "hello factory", f"expected 'hello factory', got {greeting()!r}"
print("tests passed")
PY
cat > build.sh <<'SH'
#!/bin/sh
set -e
python3 -c "import greeting; print('build ok')"
SH
chmod +x build.sh
printf '__pycache__/\n*.pyc\n' > .gitignore
git add -A
git commit -qm "baseline"
echo "baseline_sha=$(git rev-parse HEAD)"
`
	out, err := run(setup, 60*time.Second)
	if err != nil {
		return err
	}
	if out.ExitCode != 0 {
		return fmt.Errorf("repo setup failed: %s", truncate(out.Stderr, 300))
	}
	fmt.Print(prepend(strings.TrimSpace(out.Stdout), "        "))
	fmt.Println()

	// Baseline verification must FAIL here (the test expects the new greeting).
	base, err := run("cd /workspace/repository && ./build.sh && python3 test_greeting.py; echo exit=$?", 60*time.Second)
	if err != nil {
		return err
	}
	fmt.Printf("        baseline verification (expected to fail): %s\n", strings.TrimSpace(base.Stdout))

	// Factory-owned harness configuration, written by the operator (not the task).
	config := fmt.Sprintf(`{
  "model": %q,
  "default_agent": "build",
  "permissions": [
    { "action": "*", "resource": "*", "effect": "allow" }
  ]
}
`, model)
	if err := sandbox.Files().Write(ctx, "/root/.config/opencode/opencode.json", []byte(config)); err != nil {
		return fmt.Errorf("write harness config: %w", err)
	}

	task := "Change the greeting from \"hello\" to \"hello factory\". Update the tests appropriately so they pass."
	if err := sandbox.Files().Write(ctx, "/workspace/task.txt", []byte(task)); err != nil {
		return fmt.Errorf("write task: %w", err)
	}

	// The task is delivered from a file the sandbox already holds; the command
	// string itself is a fixed operator-owned template.
	agentCmd := `cd /workspace/repository && timeout 600 opencode2 run --standalone --auto --format json "$(cat /workspace/task.txt)"`
	start := time.Now()
	agent, err := run(agentCmd, 11*time.Minute)
	if err != nil {
		return fmt.Errorf("agent run: %w", err)
	}
	fmt.Printf("        agent exit=%d duration=%s\n", agent.ExitCode, time.Since(start).Round(time.Second))
	fmt.Printf("        agent stdout (tail): %s\n", truncate(strings.TrimSpace(tail(agent.Stdout, 600)), 600))
	if strings.TrimSpace(agent.Stderr) != "" {
		fmt.Printf("        agent stderr (tail): %s\n", truncate(strings.TrimSpace(tail(agent.Stderr, 400)), 400))
	}

	// Deterministic verification, owned by the factory.
	verify, err := run("cd /workspace/repository && ./build.sh && python3 test_greeting.py; echo exit=$?", 120*time.Second)
	if err != nil {
		return err
	}
	fmt.Printf("        verification: %s\n", strings.TrimSpace(verify.Stdout))

	diff, err := run("cd /workspace/repository && git add -A && git diff --cached", 60*time.Second)
	if err != nil {
		return err
	}
	fmt.Printf("        resulting sha=%s\n", strings.TrimSpace(mustRun(ctx, sandbox, "cd /workspace/repository && git rev-parse HEAD")))
	patch := strings.TrimSpace(diff.Stdout)
	if patch == "" {
		return errors.New("agent produced no patch")
	}
	fmt.Printf("        patch (%d bytes):\n%s\n", len(patch), prepend(truncate(patch, 1200), "        | "))

	if !strings.Contains(verify.Stdout, "exit=0") {
		return errors.New("verification did not pass")
	}
	return nil
}

func mustRun(ctx context.Context, sandbox *cubesandbox.Sandbox, cmd string) string {
	out, err := sandbox.Commands().Run(ctx, cmd, cubesandbox.CommandOptions{})
	if err != nil {
		return "error: " + err.Error()
	}
	return out.Stdout
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// runExec provisions a fresh sandbox with the agent binary and runs one exact
// command verbatim. It is the operator's escape hatch for debugging sandbox
// behaviour without a full factory run.
func runExec(command, localBinary, apiURL, templateID, seedSpec string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	cfg := cubesandbox.NewConfigFromEnv()
	cfg.APIURL = apiURL
	cfg.TemplateID = templateID
	client := cubesandbox.NewClient(cfg)
	defer client.Close()

	before, err := client.List(ctx)
	if err != nil {
		return err
	}
	sandbox, err := client.Create(ctx, cubesandbox.CreateOptions{
		TemplateID: templateID,
		Timeout:    cubesandbox.DurationPtr(14 * time.Minute),
		Metadata:   map[string]string{"origin": "factory-cube-exec"},
	})
	if err != nil {
		return err
	}
	fmt.Printf("sandbox: %s\n", sandbox.SandboxID)
	defer func() {
		killCtx, c := context.WithTimeout(context.Background(), 90*time.Second)
		defer c()
		_ = sandbox.Kill(killCtx)
		after, err := client.List(killCtx)
		if err != nil || len(after) != len(before) {
			fmt.Printf("WARNING: cleanup verification: before=%d after=%d err=%v\n", len(before), len(after), err)
		}
	}()

	data, err := os.ReadFile(localBinary)
	if err != nil {
		return err
	}
	if err := sandbox.Files().Write(ctx, "/usr/local/bin/opencode2", data); err != nil {
		return err
	}
	if _, err := sandbox.Commands().Run(ctx, "chmod +x /usr/local/bin/opencode2", cubesandbox.CommandOptions{}); err != nil {
		return err
	}

	// Stage extra files, e.g. the opencode model catalog cache.
	for _, pair := range strings.Split(seedSpec, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		hostPath, sandboxPath, ok := strings.Cut(pair, ":")
		if !ok {
			return fmt.Errorf("invalid -seed %q: want hostPath:sandboxPath", pair)
		}
		seedData, err := os.ReadFile(strings.TrimSpace(hostPath))
		if err != nil {
			return fmt.Errorf("read seed file: %w", err)
		}
		if err := sandbox.Files().Write(ctx, strings.TrimSpace(sandboxPath), seedData); err != nil {
			return err
		}
		fmt.Printf("seeded %s -> %s (%d bytes)\n", hostPath, sandboxPath, len(seedData))
	}

	out, err := sandbox.Commands().Run(ctx, command, cubesandbox.CommandOptions{Timeout: 14 * time.Minute})
	if err != nil {
		return fmt.Errorf("exec: %w", err)
	}
	fmt.Printf("\n--- stdout ---\n%s\n--- stderr ---\n%s\n--- exit=%d ---\n", out.Stdout, out.Stderr, out.ExitCode)
	return nil
}

func step(name string, fn func() error) {
	fmt.Printf("== %s\n", name)
	start := time.Now()
	if err := fn(); err != nil {
		fmt.Printf("   FAILED after %s: %v\n", time.Since(start).Round(time.Millisecond), err)
		return
	}
	fmt.Printf("   ok in %s\n\n", time.Since(start).Round(time.Millisecond))
}

func prepend(text, prefix string) string {
	lines := strings.Split(text, "\n")
	for i := range lines {
		lines[i] = prefix + lines[i]
	}
	return strings.Join(lines, "\n") + "\n"
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func envOr(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return fallback
}
