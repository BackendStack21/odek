package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/BackendStack21/odek/internal/resource"
)

// enrichTask resolves @references in the task prompt and prepends
// --ctx file attachments. Returns the enriched prompt ready for
// the LLM, or an error if any --ctx file can't be read.
//
// @refs that fail to resolve are left as-is in the text.
//
// Examples:
//
//	odek run "@main.go what does this do?"
//	  → resolves @main.go to file content, replaces inline
//
//	odek run --ctx main.go "analyze this"
//	  → prepends file content as context block
//
//	odek run --ctx lib.go,util.go "@main.go compare these"
//	  → both ctx files + @ref resolution
func enrichTask(ctx context.Context, task string, ctxFiles []string, cwd string) (string, error) {
	if len(task) > resource.MaxExpandedPromptBytes {
		return "", fmt.Errorf("resource: expanded prompt exceeds %d bytes", resource.MaxExpandedPromptBytes)
	}
	reg := resource.NewRegistry(resource.NewFileResolver(cwd))

	// Step 1: Resolve @ references in the task
	enriched := task
	refs := resource.ParseRefs(task)
	if len(refs) > 0 {
		resolved := make(map[string]string)
		attempted := make(map[string]struct{})
		resolvedBytes := 0
		for _, ref := range refs {
			if _, seen := attempted[ref.Raw]; seen {
				if content, ok := resolved[ref.Raw]; ok {
					resolvedBytes += 2*len(ref.Raw) + len(content) + 25
					if resolvedBytes > resource.MaxExpandedPromptBytes {
						return "", fmt.Errorf("resource: expanded prompt exceeds %d bytes", resource.MaxExpandedPromptBytes)
					}
				}
				continue
			}
			attempted[ref.Raw] = struct{}{}
			content, err := reg.Load(ctx, ref.Raw)
			if err != nil {
				// Leave unresolved refs as-is
				continue
			}
			wrapped := wrapUntrusted(ctx, "resource:"+ref.Raw, content)
			resolvedBytes += len(wrapped)
			if resolvedBytes > resource.MaxExpandedPromptBytes {
				return "", fmt.Errorf("resource: expanded prompt exceeds %d bytes", resource.MaxExpandedPromptBytes)
			}
			resolved[ref.Raw] = wrapped
		}
		var err error
		enriched, err = resource.ReplaceRefsBounded(task, resolved, resource.MaxExpandedPromptBytes)
		if err != nil {
			return "", err
		}
	}

	// Step 2: Add --ctx files as preamble
	if len(ctxFiles) > 0 {
		var blocks []string
		blocksBytes := 0
		for _, f := range ctxFiles {
			f = strings.TrimSpace(f)
			if f == "" {
				continue
			}
			content, err := reg.Load(ctx, "@"+f)
			if err != nil {
				return "", fmt.Errorf("ctx file %q: %w", f, err)
			}
			block := fmt.Sprintf("--- %s ---\n%s\n--- end %s ---", f, wrapUntrusted(ctx, "ctx:"+f, content), f)
			if len(blocks) > 0 {
				blocksBytes += 2
			}
			blocksBytes += len(block)
			blocks = append(blocks, block)
			if blocksBytes+2+len(enriched) > resource.MaxExpandedPromptBytes {
				return "", fmt.Errorf("resource: expanded prompt exceeds %d bytes", resource.MaxExpandedPromptBytes)
			}
		}
		if len(blocks) > 0 {
			// Log attached files to stderr
			fmt.Fprintf(os.Stderr, "odek: attached %d file(s)\n", len(blocks))
			enriched = strings.Join(blocks, "\n\n") + "\n\n" + enriched
		}
	}
	if len(enriched) > resource.MaxExpandedPromptBytes {
		return "", fmt.Errorf("resource: expanded prompt exceeds %d bytes", resource.MaxExpandedPromptBytes)
	}

	return enriched, nil
}
