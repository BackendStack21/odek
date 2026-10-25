package sandbox

import (
	"context"
	"errors"
	"testing"
)

// Docker resolves "network inspect <x>" by full ID, then name, then ID
// prefix, so a requested name can come back as a different network —
// including the predefined bridge. Only a network whose own name is the
// requested one may be attached.
func TestRED_ResolveNetworks_RefusesNetworkResolvedUnderAnotherName(t *testing.T) {
	cases := []struct {
		requested, returned string
	}{
		{"ca064c", "bridge"},         // ID prefix of the default bridge
		{id('c'), "bridge"},          // a name equal to bridge's full ID
		{"app_default", "other_net"}, // any other mismatch
	}
	for _, tc := range cases {
		f := &netFakeDocker{handler: func(args []string) ([]byte, error) {
			if isNetInspect(args) {
				return []byte(netJSON(tc.returned, id('c'), "bridge", "local", false, "")), nil
			}
			return nil, errors.New("unexpected docker call")
		}}
		f.install(t)
		nets, err := ResolveNetworks(context.Background(), []string{tc.requested})
		if err == nil || !errors.Is(err, ErrInvalidNetwork) {
			t.Errorf("requested %q resolved to %q: want ErrInvalidNetwork, got nets=%v err=%v", tc.requested, tc.returned, nets, err)
		}
	}
}
