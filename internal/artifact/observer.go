package artifact

import "context"

type observerKey struct{}

// WithObserver attaches an operator-facing artifact sink to a tool context.
// Observers receive only refs validated by the producing MCP client. They must
// independently validate before opening files; refs remain untrusted metadata.
func WithObserver(ctx context.Context, fn func(Ref, []string)) context.Context {
	return context.WithValue(ctx, observerKey{}, fn)
}

// Observe delivers validated refs without changing the model-facing result.
func Observe(ctx context.Context, refs []Ref, roots []string) {
	fn, _ := ctx.Value(observerKey{}).(func(Ref, []string))
	if fn == nil {
		return
	}
	for _, ref := range refs {
		func() { defer func() { _ = recover() }(); fn(ref, append([]string(nil), roots...)) }()
	}
}
