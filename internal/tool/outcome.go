package tool

import (
	"context"
	"errors"
)

// Outcome describes execution independently from tool-returned content.
// A successful read may contain arbitrary error messages as ordinary data.
type Outcome struct {
	Status     string
	ErrorClass string
	Retryable  bool
}

type PermanentError struct{ Message string }

func (e *PermanentError) Error() string      { return e.Message }
func NewPermanentError(message string) error { return &PermanentError{Message: message} }

func OutcomeFor(err error) Outcome {
	if err == nil {
		return Outcome{Status: "completed"}
	}
	var permanent *PermanentError
	if errors.As(err, &permanent) {
		return Outcome{Status: "failed", ErrorClass: "permanent"}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return Outcome{Status: "failed", ErrorClass: "cancelled"}
	}
	return Outcome{Status: "failed", ErrorClass: "tool_error"}
}
