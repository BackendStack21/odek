package mcpclient

import (
	"net/url"
	"testing"
)

func TestRED_SameOriginNormalisesDefaultPorts(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"https://h.example/x", "https://h.example:443/y", true},
		{"http://h.example", "http://H.example:80", true},
		{"https://h.example", "https://h.example:8443", false},
		{"https://h.example", "http://h.example", false},
		{"https://h.example", "http://h.example:443", false},
		{"https://h.example", "https://sub.h.example", false},
	}
	for _, c := range cases {
		a, _ := url.Parse(c.a)
		b, _ := url.Parse(c.b)
		if got := sameOrigin(a, b); got != c.want {
			t.Errorf("sameOrigin(%s, %s) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
