package main

import (
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestSetupSandbox_InvalidNetworkIsConfigError(t *testing.T) {
	t.Setenv("ODEK_SANDBOX_NETWORK_LENIENT", "")
	for _, bad := range []string{"host", "container:x", "myapp_default"} {
		_, cleanup, err := setupSandbox(nil, sandboxConfig{Image: "alpine:3", Network: bad})
		if cleanup != nil {
			_ = cleanup()
		}
		if err == nil || !errors.Is(err, errSandboxConfig) {
			t.Errorf("network %q: want error wrapping errSandboxConfig, got %v", bad, err)
		}
		if err != nil && !strings.Contains(err.Error(), bad) {
			t.Errorf("network %q: message must name the value: %v", bad, err)
		}
	}
}

var helpHostNetwork = regexp.MustCompile(`(?i)--sandbox-network[^\n]*\bhost\b`)

func TestHelp_SandboxNetworkDoesNotAdvertiseHost(t *testing.T) {
	usage := captureStdout(printUsage)
	serve := captureStdout(printServeHelp)
	for name, text := range map[string]string{"usage": usage, "serve": serve} {
		if !strings.Contains(text, "--sandbox-network") {
			t.Errorf("%s help lost --sandbox-network", name)
		}
		if m := helpHostNetwork.FindString(text); m != "" {
			t.Errorf("%s help advertises host: %q", name, m)
		}
	}
}

func TestSandboxNetworkFieldComment_DoesNotAdvertiseHost(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(src), "\n") {
		if strings.Contains(line, "SandboxNetwork") && strings.Contains(line, "//") && strings.Contains(line, `"host"`) {
			t.Errorf("SandboxNetwork comment advertises host: %s", line)
		}
	}
}
