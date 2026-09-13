package loop

import "context"

// Legacy retrieval callbacks predate contexts. Keep their compatibility while
// bounding the run's wait by its deadline. Context-aware callback providers
// should propagate cancellation into their own I/O as well.
func contextValue(ctx context.Context, query string, fn func(string) string) string {
	if ctx.Err() != nil {
		return ""
	}
	if ctx.Done() == nil {
		return fn(query)
	}
	type callbackResult struct {
		value      string
		panicValue any
	}
	result := make(chan callbackResult, 1)
	go func() {
		var r callbackResult
		defer func() { r.panicValue = recover(); result <- r }()
		r.value = fn(query)
	}()
	select {
	case r := <-result:
		if r.panicValue != nil {
			panic(r.panicValue)
		} // caller's recovery boundary
		return r.value
	case <-ctx.Done():
		return ""
	}
}
