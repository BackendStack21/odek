package loop

import (
	"errors"
	"testing"
)

func TestContextLengthErrorIgnoresUnrelatedErrors(t *testing.T) {
	if isContextLengthError(nil) {
		t.Fatal("nil error is not a context-length error")
	}
	for _, msg := range []string{"HTTP 401: invalid api key", "HTTP 429: rate limited", "connection reset by peer"} {
		if isContextLengthError(errors.New(msg)) {
			t.Errorf("%q misclassified as context-length error", msg)
		}
	}
}
