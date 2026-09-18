// odek-eval runs deterministic, localhost-only runtime evaluations.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/BackendStack21/odek/internal/eval"
)

func main() {
	report := eval.Run(context.Background(), eval.Scenarios())
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(report); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if report.Failed != 0 {
		os.Exit(1)
	}
}
