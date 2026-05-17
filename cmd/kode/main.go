package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime/debug"
	"strings"

	"github.com/BackendStack21/kode"
	"github.com/BackendStack21/kode/internal/render"
)

// version is set at build time via ldflags: -ldflags "-X main.version=v0.2.1"
// Falls back to VCS tag from debug.ReadBuildInfo, then to "dev".
var version string

const defaultSystem = `You are kode, an autonomous AI coding agent. You solve tasks by reasoning step by step, then executing tools.

Rules:
1. Think before acting. Explain your reasoning.
2. When you need information, use the shell tool to read files, list directories, or run commands.
3. After gathering information, produce a final answer with no further tool calls.
4. Be concise. Answer the question, then stop.`

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "run":
		if err := run(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "kode: %v\n", err)
			os.Exit(1)
		}
	case "version":
		fmt.Println("kode", getVersion())
	default:
		fmt.Fprintf(os.Stderr, "kode: unknown command %q\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

// runFlags holds the parsed CLI flags for `kode run`.
type runFlags struct {
	Model    string
	BaseURL  string
	System   string
	Thinking string
	MaxIter  int
	Sandbox  bool
	NoColor  bool
	Task     string
}

// parseRunFlags parses `kode run` arguments and returns the parsed flags.
// Exported for testing.
func parseRunFlags(args []string) (runFlags, error) {
	var f runFlags
	f.MaxIter = 90

	i := 0
	for i < len(args)-1 {
		switch args[i] {
		case "--model":
			f.Model = args[i+1]
			i += 2
		case "--base-url":
			f.BaseURL = args[i+1]
			i += 2
		case "--max-iter":
			fmt.Sscanf(args[i+1], "%d", &f.MaxIter)
			i += 2
		case "--system":
			f.System = args[i+1]
			i += 2
		case "--thinking":
			f.Thinking = args[i+1]
			i += 2
		case "--sandbox":
			f.Sandbox = true
			i++
		case "--no-color":
			f.NoColor = true
			i++
		default:
			// Not a flag — treat remaining as the task
			goto done
		}
	}
done:
	f.Task = strings.Join(args[i:], " ")
	if f.Task == "" {
		return f, fmt.Errorf("no task provided")
	}
	return f, nil
}

func printUsage() {
	fmt.Println(`Usage:
  kode run [flags] <task>
  kode version

Flags:
  --model <name>       LLM model (default: deepseek-chat)
  --base-url <url>     API endpoint (default: https://api.deepseek.com/v1)
  --max-iter <n>       Max think->act cycles (default: 90)
  --thinking <level>   Reasoning depth: enabled|disabled (Deepseek) or low|medium|high (OpenAI o-series)
  --sandbox            Run in isolated Docker container
  --no-color           Disable colored terminal output
  --system <prompt>    System prompt override`)
}

// run executes the `kode run` command and returns an error on failure.
// The caller is responsible for printing the error and calling os.Exit.
func run(args []string) error {
	f, err := parseRunFlags(args)
	if err != nil {
		return err
	}

	if f.System == "" {
		f.System = defaultSystem
	}

	// Sandbox setup
	var sandboxCleanup func() error
	tools := builtinTools()

	if f.Sandbox {
		cleanup, err := setupSandbox(tools)
		if err != nil {
			return fmt.Errorf("sandbox: %w", err)
		}
		sandboxCleanup = cleanup
	}

	// Create terminal renderer for colored step-by-step output.
	modelName := f.Model
	if modelName == "" {
		modelName = "deepseek-chat"
	}
	color := !f.NoColor && render.ColorEnabled()
	rend := render.New(os.Stderr, color)

	agent, err := kode.New(kode.Config{
		Model:          f.Model,
		BaseURL:        f.BaseURL,
		MaxIterations:  f.MaxIter,
		SystemMessage:  f.System,
		Thinking:       f.Thinking,
		Tools:          tools,
		SandboxCleanup: sandboxCleanup,
		Renderer:       rend.WithModel(modelName),
	})
	if err != nil {
		return err
	}
	defer agent.Close()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	result, err := agent.Run(ctx, f.Task)
	if err != nil {
		return err
	}
	_ = result // rendered by the loop engine via Renderer
	return nil
}

// setupSandbox creates a Docker container and wires the shell tool to use it.
// Returns a cleanup function that destroys the container.
func setupSandbox(tools []kode.Tool) (func() error, error) {
	containerName := fmt.Sprintf("kode-%d", os.Getpid())
	fmt.Fprintf(os.Stderr, "kode: starting sandbox container %s...\n", containerName)

	wd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("getwd: %w", err)
	}

	createCmd := exec.Command("docker", "run",
		"--rm",     // destroy on exit
		"--detach", // run in background
		"--name", containerName,
		"--cap-drop", "ALL", // no capabilities
		"--security-opt", "no-new-privileges", // no privilege escalation
		"--network", "none", // no network
		"--tmpfs", "/tmp:noexec", // no executable temp files
		"-v", wd+":/workspace", // working dir (read-write inside sandbox)
		"alpine:latest",
		"sleep", "infinity",
	)
	createCmd.Stderr = os.Stderr
	if err := createCmd.Run(); err != nil {
		return nil, fmt.Errorf("failed to create container: %w", err)
	}

	cleanup := func() error {
		fmt.Fprintf(os.Stderr, "kode: destroying sandbox container %s...\n", containerName)
		return exec.Command("docker", "rm", "-f", containerName).Run()
	}

	// Wire the shell tool to execute commands inside the sandbox.
	tools[0].(*shellTool).containerName = containerName
	return cleanup, nil
}

func builtinTools() []kode.Tool {
	return []kode.Tool{
		&shellTool{},
	}
}

// getVersion returns the version string. Resolution order:
//  1. ldflags override (-X main.version=v0.2.1)
//  2. VCS tag from debug.ReadBuildInfo (when built with go install)
//  3. VCS revision (short commit hash)
//  4. "dev" (local go build without VCS info)
func getVersion() string {
	if version != "" {
		return version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	var revision string
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
			if len(revision) > 7 {
				revision = revision[:7]
			}
		case "vcs.tag":
			if s.Value != "" {
				return s.Value
			}
		}
	}
	if revision != "" {
		return revision
	}
	return "dev"
}
