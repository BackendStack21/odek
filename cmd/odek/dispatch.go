package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"slices"

	"github.com/BackendStack21/odek/internal/budget"
	"github.com/BackendStack21/odek/internal/diagnostics"
	"github.com/BackendStack21/odek/internal/events"
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
	// Protocol children relay diagnostics through their parent once initialized.
	// Keep version queries and read-only log queries independent of logger writes.
	preview := cmd == "cleanup" && slices.Contains(rest, "--dry-run")
	if !preview && cmd != "logs" && (cmd != "subagent" || subagentDepth() == 0) && cmd != "version" && cmd != "--version" && cmd != "-v" {
		closeLog := startOperationalLogging(commandSurface(cmd))
		defer closeLog()
		defer func() {
			if value := recover(); value != nil {
				diagnostics.Emit(events.Event{Type: "panic_recovered", Data: map[string]any{"component": "cli", "operation": "dispatch", "error_class": "panic"}})
				panic(value) // preserve the original crash behavior after flushing
			}
		}()
	}

	if helpRequested(cmd, rest) {
		printCommandUsage(os.Stdout, cmd)
		return 0
	}
	// exitFor turns a command result into an exit code. A help flag met by
	// a flag parser (anywhere in the command's own flags) prints that
	// command's usage and exits 0, whichever translator the command uses.
	exitFor := func(err error, translate func(error) int) int {
		if errors.Is(err, flag.ErrHelp) {
			if errors.Is(err, errHelpFlag) {
				printCommandUsage(os.Stdout, cmd)
			}
			return 0
		}
		return translate(err)
	}

	switch cmd {
	case "run":
		return exitFor(run(rest), runExit)
	case "help", "--help", "-h":
		printUsage()
		return 0
	case "version", "--version", "-v":
		// --version is the form every packaging script, CI preflight, and
		// support-bundle collector reaches for first; treat it as a full
		// alias of the version subcommand instead of an unknown command.
		printVersion()
		return 0
	case "init":
		return exitFor(initConfig(rest), cliExit)
	case "continue":
		// continue enforces execution budgets like run: a typed budget
		// error maps to exit code 4 (docs/EXTENSIONS.md).
		return exitFor(continueCmd(rest), runExit)
	case "session":
		return exitFor(sessionCmd(rest), cliExit)
	case "audit":
		return exitFor(auditCmd(rest), cliExit)
	case "repl":
		return exitFor(replCmd(rest), cliExit)
	case "skill":
		return exitFor(skillCmd(rest), cliExit)
	case "serve":
		return exitFor(serveCmd(rest), cliExit)
	case "subagent":
		return exitFor(subagentCmd(rest), subagentExit)
	case "mcp":
		return exitFor(mcpCmd(rest), cliExit)
	case "telegram":
		return exitFor(telegramCmd(rest), cliExit)
	case "schedule":
		return exitFor(scheduleCmd(rest), cliExit)
	case "memory":
		return exitFor(memoryCmd(rest), cliExit)
	case "cleanup":
		return exitFor(cleanupCmd(rest), cliExit)
	case "logs":
		return exitFor(logsCmd(rest), cliExit)
	case "upgrade":
		return exitFor(upgradeCmd(rest), cliExit)
	default:
		diagnostics.Warning("cli", "unknown_command", nil)
		fmt.Fprintf(os.Stderr, "odek: unknown command %q\n", cmd)
		printUsage()
		return 1
	}
}

// errHelpFlag marks a help flag (-h/--help) met by a flag parser; it matches
// flag.ErrHelp so every exit translator can treat it as success.
var errHelpFlag error = helpFlagError{}

type helpFlagError struct{}

func (helpFlagError) Error() string        { return "help requested" }
func (helpFlagError) Is(target error) bool { return target == flag.ErrHelp }

// leadingFlagError is the parser error for an unrecognised flag in a
// command's own flag section: -h/--help there is a help request, anything
// else an unknown flag.
func leadingFlagError(arg string) error {
	return leadingFlagError2(arg, unknownFlagError(arg))
}

// leadingFlagError2 is leadingFlagError for parsers with their own
// unknown-flag message.
func leadingFlagError2(arg string, unknown error) error {
	if arg == "-h" || arg == "--help" {
		return errHelpFlag
	}
	return unknown
}

// printCommandUsage writes the usage of a command (its parent command for
// sub-subcommands); commands without a dedicated synopsis get the global one.
func printCommandUsage(w io.Writer, cmd string) {
	switch cmd {
	case "schedule":
		writeScheduleUsage(w)
	case "session":
		fmt.Fprintln(w, "Usage: odek session <list|show [id]|trim <id> <n>|delete <id>|cleanup <days>>")
	case "skill":
		fmt.Fprintln(w, "Usage: odek skill <list|view|delete|promote|import> [args]")
	default:
		printUsage()
	}
}

// helpRequested reports whether the arguments are a request for help on a
// command that does not otherwise handle one: -h, --help or (for the
// command groups) help, as the first argument or, for groups, right after
// the group name. Later positions are task text or operands and never count.
func helpRequested(cmd string, args []string) bool {
	isHelp := func(a string) bool { return a == "-h" || a == "--help" }
	switch cmd {
	case "session", "schedule", "skill":
		if len(args) > 0 && (isHelp(args[0]) || args[0] == "help") {
			return true
		}
		return len(args) > 1 && isHelp(args[1])
	case "run", "continue", "repl", "init", "subagent":
		return len(args) > 0 && isHelp(args[0])
	}
	return false
}

// cliExit is the default error→exit translator: prints the error to stderr
// with the "odek:" prefix and returns exit code 1, or returns 0 on nil.
func cliExit(err error) int {
	if err == nil || errors.Is(err, flag.ErrHelp) {
		// -h/--help on a flag-parsed command: the flag package already
		// printed the usage; asking for help is not a failure.
		return 0
	}
	logCommandFailure(err)
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
	logCommandFailure(err)
	fmt.Fprintf(os.Stderr, "odek: %v\n", err)
	if _, ok := budget.As(err); ok {
		return 4
	}
	return 1
}

// subagentExit honours the sub-agent JSON contract: stderr gets the
// human-readable line, stdout gets a JSON envelope the parent can parse,
// and exit codes follow docs/EXTENSIONS.md: 0 success, 1 task error,
// 2 timeout, 3 setup error, 4 execution-budget stop (both the mid-run
// *subagentRunError case and a pre-run typed budget.Error, e.g. the
// share-mode exhaustion spawn gate). Task errors and timeouts arrive as
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
	if _, ok := budget.As(err); ok {
		// Pre-run budget stop (share-mode exhaustion): the typed budget
		// error arrived before any run started. Same wire contract as a
		// mid-run exhaustion — budget_exhausted envelope, exit code 4.
		logCommandFailure(err)
		fmt.Fprintf(os.Stderr, "odek: %v\n", err)
		_ = json.NewEncoder(os.Stdout).Encode(subagentResult{
			Status:        "budget_exhausted",
			PartialReason: "execution_budget",
			Error:         err.Error(),
		})
		return 4
	}
	logCommandFailure(err)
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

// commandSurface never copies an arbitrary CLI argument into metadata.
func commandSurface(cmd string) string {
	switch cmd {
	case "run", "subagent", "continue", "init", "session", "audit", "repl", "skill", "serve", "mcp", "telegram", "schedule", "memory", "cleanup", "upgrade":
		return cmd
	default:
		return "cli"
	}
}
