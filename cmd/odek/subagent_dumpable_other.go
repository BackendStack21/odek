//go:build !linux

package main

// hardenSubagentProcess is a no-op outside Linux: no other supported platform
// lets a same-uid process reopen another process's descriptors through /proc.
func hardenSubagentProcess() error { return nil }
