package main

import "context"

// Each invocation gets its own context and audit recorder. Configuration is
// fixed before execution; the loop orders operations on overlapping paths.
func (t *searchFilesTool) CallContext(ctx context.Context, args string) (string, error) {
	call := &searchFilesTool{dangerousConfig: t.dangerousConfig, restrictToCWD: t.restrictToCWD}
	call.SetContext(ctx)
	return call.Call(args)
}
func (t *writeFileTool) CallContext(ctx context.Context, args string) (string, error) {
	call := &writeFileTool{dangerousConfig: t.dangerousConfig, trustedClasses: t.trustedClasses, restrictToCWD: t.restrictToCWD, containerName: t.containerName}
	call.SetContext(ctx)
	return call.Call(args)
}
func (t *patchTool) CallContext(ctx context.Context, args string) (string, error) {
	call := &patchTool{dangerousConfig: t.dangerousConfig, trustedClasses: t.trustedClasses, restrictToCWD: t.restrictToCWD, containerName: t.containerName}
	call.SetContext(ctx)
	return call.Call(args)
}
func (t *globTool) CallContext(ctx context.Context, args string) (string, error) {
	call := &globTool{dangerousConfig: t.dangerousConfig, restrictToCWD: t.restrictToCWD}
	call.SetContext(ctx)
	return call.Call(args)
}
func (t *fileInfoTool) CallContext(ctx context.Context, args string) (string, error) {
	call := &fileInfoTool{dangerousConfig: t.dangerousConfig, restrictToCWD: t.restrictToCWD}
	call.SetContext(ctx)
	return call.Call(args)
}
func (t *diffTool) CallContext(ctx context.Context, args string) (string, error) {
	call := &diffTool{dangerousConfig: t.dangerousConfig, restrictToCWD: t.restrictToCWD}
	call.SetContext(ctx)
	return call.Call(args)
}
func (t *jsonQueryTool) CallContext(ctx context.Context, args string) (string, error) {
	call := &jsonQueryTool{dangerousConfig: t.dangerousConfig, restrictToCWD: t.restrictToCWD}
	call.SetContext(ctx)
	return call.Call(args)
}
func (t *treeTool) CallContext(ctx context.Context, args string) (string, error) {
	call := &treeTool{dangerousConfig: t.dangerousConfig, restrictToCWD: t.restrictToCWD}
	call.SetContext(ctx)
	return call.Call(args)
}
func (t *headTailTool) CallContext(ctx context.Context, args string) (string, error) {
	call := &headTailTool{dangerousConfig: t.dangerousConfig, restrictToCWD: t.restrictToCWD}
	call.SetContext(ctx)
	return call.Call(args)
}
func (t *base64Tool) CallContext(ctx context.Context, args string) (string, error) {
	call := &base64Tool{dangerousConfig: t.dangerousConfig, restrictToCWD: t.restrictToCWD}
	call.SetContext(ctx)
	return call.Call(args)
}
