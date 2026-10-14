package session

import "testing"

func TestRED_ResourcesInRecognisesBareIPEndpoints(t *testing.T) {
	got := ResourcesIn(`nc 203.0.113.9 4444; ssh u@198.51.100.7; curl [2001:db8::1]:8080`)
	want := map[string]bool{"203.0.113.9": false, "198.51.100.7": false, "[2001:db8::1]": false}
	for _, r := range got {
		if _, ok := want[r]; ok {
			want[r] = true
		}
	}
	for r, ok := range want {
		if !ok {
			t.Errorf("ResourcesIn missed %q (got %v)", r, got)
		}
	}
}
