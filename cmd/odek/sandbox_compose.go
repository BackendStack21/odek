package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/BackendStack21/odek/internal/danger"
	"github.com/BackendStack21/odek/internal/sandbox"
)

// resolveSandboxNetworks expands the Compose project into network names,
// merges them with the explicit list, inspects every network and returns the
// config with Networks and ResolvedNetworks filled. The attached networks are
// listed on w.
func resolveSandboxNetworks(cfg sandboxConfig, w io.Writer) (sandboxConfig, error) {
	if len(cfg.Networks) == 0 && cfg.ComposeProject == "" {
		return cfg, nil
	}
	fail := func(err error) (sandboxConfig, error) {
		return cfg, fmt.Errorf("%w: %w", errSandboxConfig, err)
	}
	names := cfg.Networks
	if cfg.ComposeProject != "" {
		project, err := sandbox.ValidateComposeProject(cfg.ComposeProject)
		if err != nil {
			return fail(err)
		}
		cctx, ccancel := context.WithTimeout(context.Background(), 30*time.Second)
		found, err := sandbox.ComposeNetworks(cctx, project)
		ccancel()
		if err != nil {
			return fail(err)
		}
		names = sandbox.MergeNetworkNames(names, found)
	}
	names, err := sandbox.NormalizeNetworks(names)
	if err != nil {
		return fail(err)
	}
	ictx, icancel := context.WithTimeout(context.Background(), 30*time.Second)
	resolved, err := sandbox.ResolveNetworks(ictx, names)
	icancel()
	if err != nil {
		return fail(err)
	}
	cfg.Networks = names
	cfg.ResolvedNetworks = resolved
	if cfg.ComposeProject != "" {
		fmt.Fprintf(w, "odek: sandbox compose project %s networks: %s\n",
			danger.SanitizeInline(cfg.ComposeProject), danger.SanitizeInline(strings.Join(names, ", ")))
	}
	return cfg, nil
}
