package main

import "errors"

// errSandboxConfig marks a sandbox failure caused by configuration (as
// opposed to Docker being unavailable). It must never degrade to unsandboxed
// host execution.
var errSandboxConfig = errors.New("sandbox configuration error")

// setupSandboxFn is the sandbox starter used by ensureSandbox; tests replace
// it to inject setup outcomes.
var setupSandboxFn = setupSandbox
