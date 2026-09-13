package loop

import (
	"fmt"

	"github.com/BackendStack21/odek/internal/llmclient"
)

// PartialResponseError reports a provider response that did not complete.
// The returned answer and persisted assistant message retain available text.
type PartialResponseError struct {
	Reason llmclient.Termination
	Cause  error
}

func (e *PartialResponseError) Error() string {
	return fmt.Sprintf("model response incomplete: %s", e.Reason)
}

func (e *PartialResponseError) Unwrap() error { return e.Cause }
