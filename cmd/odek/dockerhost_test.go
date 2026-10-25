package main

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"
)

// pinDockerHostForTests exports the active docker context's endpoint as
// DOCKER_HOST while HOME is still the operator's, so the docker CLI keeps
// reaching the daemon after TestMain swaps in a fixture HOME. An explicit
// DOCKER_HOST or DOCKER_CONTEXT, or no docker CLI, leaves the env alone.
func pinDockerHostForTests() {
	if os.Getenv("DOCKER_HOST") != "" || os.Getenv("DOCKER_CONTEXT") != "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "context", "inspect", "--format", "{{.Endpoints.docker.Host}}").Output()
	if err != nil {
		return
	}
	if host := strings.TrimSpace(string(out)); host != "" {
		os.Setenv("DOCKER_HOST", host)
	}
}
