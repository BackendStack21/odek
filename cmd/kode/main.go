package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"github.com/BackendStack21/kode"
)

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
		runCmd()
	case "version":
		fmt.Println("kode v0.1.0")
	default:
		fmt.Fprintf(os.Stderr, "kode: unknown command %q\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
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
  --system <prompt>    System prompt override`)
}

func runCmd() {
	args := os.Args[2:]
	var model, baseURL, system, thinking string
	maxIter := 90
	sandbox := false

	i := 0
	for i < len(args)-1 {
		switch args[i] {
		case "--model":
			model = args[i+1]
			i += 2
		case "--base-url":
			baseURL = args[i+1]
			i += 2
		case "--max-iter":
			fmt.Sscanf(args[i+1], "%d", &maxIter)
			i += 2
		case "--system":
			system = args[i+1]
			i += 2
		case "--thinking":
			thinking = args[i+1]
			i += 2
		case "--sandbox":
			sandbox = true
			i++
		default:
			// Not a flag — treat remaining as the task
			goto done
		}
	}
done:
	task := strings.Join(args[i:], " ")
	if task == "" {
		fmt.Fprintln(os.Stderr, "kode: no task provided")
		os.Exit(1)
	}

	if system == "" {
		system = defaultSystem
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	agent, err := kode.New(kode.Config{
		Model:         model,
		BaseURL:       baseURL,
		MaxIterations: maxIter,
		SystemMessage: system,
		Thinking:      thinking,
		Tools:         builtinTools(),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "kode: %v\n", err)
		os.Exit(1)
	}

	_ = sandbox // TODO

	modelName := model
	if modelName == "" {
		modelName = "deepseek-chat"
	}
	fmt.Fprintf(os.Stderr, "kode: %s thinking...\n", modelName)

	result, err := agent.Run(ctx, task)
	if err != nil {
		fmt.Fprintf(os.Stderr, "kode: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(result)
}

func builtinTools() []kode.Tool {
	return []kode.Tool{
		&shellTool{},
	}
}