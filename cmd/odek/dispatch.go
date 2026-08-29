package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"

	"github.com/BackendStack21/odek/internal/budget"
)

// dispatch routes a top-level CLI invocation to its handler. It takes the
// arguments following the binary name (i.e. os.Args[1:]) and returns the
// process exit code. Splitting this out of main() keeps main() trivial,
// makes the command table explicit, and lets tests exercise routing
// without spawning a subprocess.
//
// Each handler is expected to print its own user-facing diagnostics. This
// function only writes a uniform "odek: %v" prefix for errors that bubble
// up unhandled, and a JSON envelope for the subagent contract.
func dispatch(args []string) int {
	if len(args) < 1 {
		printUsage()
		return 1
	}

	cmd := args[0]
	rest := args[1:]

	switch cmd {
	case "run":
		return runExit(run(rest))
	case "version", "--version", "-v":
		// --version is the form every packaging script, CI preflight, and
		// support-bundle collector reaches for first; treat it as a full
		// alias of the version subcommand instead of an unknown command.
		printVersion()
		return 0
	case "init":
		return cliExit(initConfig(rest))
	case "continue":
		return cliExit(continueCmd(rest))
	case "session":
		return cliExit(sessionCmd(rest))
	case "audit":
		return cliExit(auditCmd(rest))
	case "repl":
		return cliExit(replCmd(rest))
	case "skill":
		return cliExit(skillCmd(rest))
	case "serve":
		return cliExit(serveCmd(rest))
	case "subagent":
		return subagentExit(subagentCmd(rest))
	case "mcp":
		return cliExit(mcpCmd(rest))
	case "telegram":
		return cliExit(telegramCmd(rest))
	case "schedule":
		return cliExit(scheduleCmd(rest))
	case "memory":
		return cliExit(memoryCmd(rest))
	case "cleanup":
		return cliExit(cleanupCmd(rest))
	case "upgrade":
		return cliExit(upgradeCmd(rest))
	default:
		fmt.Fprintf(os.Stderr, "odek: unknown command %q\n", cmd)
		printUsage()
		return 1
	}
}

// cliExit is the default error→exit translator: prints the error to stderr
// with the "odek:" prefix and returns exit code 1, or returns 0 on nil.
func cliExit(err error) int {
	if err == nil {
		return 0
	}
	fmt.Fprintf(os.Stderr, "odek: %v\n", err)
	return 1
}

// runExit is the `odek run` error→exit translator: like cliExit, but maps a
// typed budget.Error to exit code 4 (execution budget exhausted —
// odek-extension/v1, see docs/EXTENSIONS.md) so orchestrators can tell a
// budget stop apart from a task/model/tool failure (1).
func runExit(err error) int {
	if err == nil {
		return 0
	}
	fmt.Fprintf(os.Stderr, "odek: %v\n", err)
	if _, ok := budget.As(err); ok {
		return 4
	}
	return 1
}

// subagentExit honours the sub-agent JSON contract: stderr gets the
// human-readable line, stdout gets a JSON envelope the parent can parse,
// and exit codes follow docs/EXTENSIONS.md: 0 success, 1 task error,
// 2 timeout, 3 setup error. Task errors and timeouts arrive as
// *subagentRunError with their envelope already printed, so they only map
// to an exit code here.
func subagentExit(err error) int {
	if err == nil {
		return 0
	}
	var runErr *subagentRunError
	if errors.As(err, &runErr) {
		if runErr.timeout {
			return 2
		}
		if runErr.budget {
			return 4
		}
		return 1
	}
	fmt.Fprintf(os.Stderr, "odek: %v\n", err)
	_ = json.NewEncoder(os.Stdout).Encode(subagentResult{
		Status: "error",
		Error:  err.Error(),
	})
	return 3
}

// printVersion writes the formatted version block to stdout. The build
// stamp comes from -ldflags or, failing that, from the VCS info embedded
// by the Go toolchain.
func printVersion() {
	fmt.Printf("odek %s\n", getVersion())
	fmt.Printf("  go:      %s\n", runtime.Version())
	fmt.Printf("  os/arch: %s/%s\n", runtime.GOOS, runtime.GOARCH)
	if date := getVCSTime(); date != "" {
		fmt.Printf("  built:   %s\n", date)
	}
}
