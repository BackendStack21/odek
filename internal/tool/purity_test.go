package tool

import "testing"

type registeredPure struct{ pure bool }

func (r *registeredPure) PureOutputFor(string) bool { return r.pure }

type unregisteredPure struct{}

func (unregisteredPure) PureOutputFor(string) bool { return true }

// embedsRegistered inherits PureOutputFor from a registered type but is a
// different concrete type, so its claim is not honoured.
type embedsRegistered struct{ registeredPure }

// panickingPure is registered (test-only) and panics when asked.
type panickingPure struct{}

func (*panickingPure) PureOutputFor(string) bool { panic("boom") }

func init() {
	RegisterPureOutputType((*registeredPure)(nil))
	RegisterPureOutputType((*panickingPure)(nil))
}

func TestOutputIsPure_PanicIsExternal(t *testing.T) {
	if OutputIsPure(&panickingPure{}, "{}") {
		t.Fatal("panicking PureOutputFor reported pure output")
	}
}

func TestOutputIsPure(t *testing.T) {
	cases := []struct {
		name string
		t    any
		want bool
	}{
		{"nil", nil, false},
		{"registered pure", &registeredPure{pure: true}, true},
		{"registered impure call", &registeredPure{pure: false}, false},
		{"unregistered claim", unregisteredPure{}, false},
		{"embedding a registered type", &embedsRegistered{registeredPure{pure: true}}, false},
		{"value of a registered pointer type", registeredPure{pure: true}, false},
	}
	for _, tc := range cases {
		if got := OutputIsPure(tc.t, "{}"); got != tc.want {
			t.Errorf("%s: OutputIsPure = %v, want %v", tc.name, got, tc.want)
		}
	}
}
