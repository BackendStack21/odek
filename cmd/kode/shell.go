package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// shellTool lets the agent run shell commands.
// When containerName is set, commands execute inside that Docker container
// via "docker exec". Otherwise they run on the host.
type shellTool struct {
	containerName string // empty = host, non-empty = docker exec into this container
}

func (t *shellTool) Name() string        { return "shell" }
func (t *shellTool) Description() string { return "Run a shell command and return its output. Use for: reading files, listing directories, running tests, building code, git operations. The command runs in the current working directory." }
func (t *shellTool) Schema() any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]any{
				"type":        "string",
				"description": "The shell command to execute",
			},
		},
		"required": []string{"command"},
	}
}

func (t *shellTool) Call(args string) (string, error) {
	var input struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(args), &input); err != nil {
		return "", fmt.Errorf("shell: parse args: %w", err)
	}
	if input.Command == "" {
		return "", fmt.Errorf("shell: empty command")
	}

	var cmd *exec.Cmd
	if t.containerName != "" {
		cmd = exec.Command("docker", "exec", "-w", "/workspace", t.containerName, "sh", "-c", input.Command)
	} else {
		cmd = exec.Command("sh", "-c", input.Command)
	}

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	err := cmd.Run()
	output := strings.TrimSpace(outBuf.String())
	if errBuf.Len() > 0 {
		if output != "" {
			output += "\n"
		}
		output += strings.TrimSpace(errBuf.String())
	}
	if err != nil && output == "" {
		return "", fmt.Errorf("shell: %w", err)
	}
	if output == "" {
		output = "(no output)"
	}
	return output, nil
}
