package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/BackendStack21/kode/internal/resource"
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
func enrichTask(task string, ctxFiles []string, cwd string) (string, error) {
	reg := resource.NewRegistry(resource.NewFileResolver(cwd))

	// Step 1: Resolve @ references in the task
	enriched := task
	refs := resource.ParseRefs(task)
	if len(refs) > 0 {
		resolved := make(map[string]string)
		for _, ref := range refs {
			content, err := reg.Load(context.Background(), ref.Raw)
			if err != nil {
				// Leave unresolved refs as-is
				continue
			}
			resolved[ref.Raw] = content
		}
		enriched = resource.ReplaceRefs(task, resolved)
	}

	// Step 2: Add --ctx files as preamble
	if len(ctxFiles) > 0 {
		var blocks []string
		for _, f := range ctxFiles {
			f = strings.TrimSpace(f)
			if f == "" {
				continue
			}
			content, err := reg.Load(context.Background(), "@"+f)
			if err != nil {
				return "", fmt.Errorf("ctx file %q: %w", f, err)
			}
			blocks = append(blocks, fmt.Sprintf("--- %s ---\n%s\n--- end %s ---", f, content, f))
		}
		if len(blocks) > 0 {
			// Log attached files to stderr
			fmt.Fprintf(os.Stderr, "odek: attached %d file(s)\n", len(blocks))
			enriched = strings.Join(blocks, "\n\n") + "\n\n" + enriched
		}
	}

	return enriched, nil
}
