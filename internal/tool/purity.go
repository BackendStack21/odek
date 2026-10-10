package tool

import (
	"reflect"
	"sync"
)

// PureOutput is implemented by first-party tools whose output, for the given
// arguments, is derived only from those model-supplied arguments or from
// operator-controlled runtime state: no file reads, no network, no
// subprocess output, no session or memory content, no third-party metadata.
// The loop does not treat such a call as an untrusted ingest, so it neither
// records an audit ingest nor taints the run for delegation trust.
//
// Implementing the method is not enough: OutputIsPure honours it only for
// concrete types registered with RegisterPureOutputType. That function lives
// in an internal package, so library embedders and MCP servers can never opt
// their tools out of the taint, and a type that embeds a registered one (and
// so inherits the method) is a different, unregistered type.
type PureOutput interface {
	PureOutputFor(args string) bool
}

var pureOutputTypes sync.Map // reflect.Type → struct{}

// RegisterPureOutputType records the concrete type of sample as a first-party
// tool whose PureOutputFor answer is trusted. Call it from package init only,
// and only for tools audited against the PureOutput contract (or for
// first-party adapters that forward the question to the tool they wrap).
func RegisterPureOutputType(sample PureOutput) {
	pureOutputTypes.Store(reflect.TypeOf(sample), struct{}{})
}

// OutputIsPure reports whether calling t with args produces pure output: t's
// concrete type is registered and its PureOutputFor(args) returns true. Every
// other tool, including any embedder or MCP tool, is external. A panicking
// PureOutputFor counts as external and never escapes: the loop asks outside
// the tool call's own recover.
func OutputIsPure(t any, args string) (pure bool) {
	defer func() {
		if recover() != nil {
			pure = false
		}
	}()
	if t == nil {
		return false
	}
	if _, ok := pureOutputTypes.Load(reflect.TypeOf(t)); !ok {
		return false
	}
	p, ok := t.(PureOutput)
	return ok && p.PureOutputFor(args)
}
