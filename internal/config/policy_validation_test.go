package config

import (
	"github.com/BackendStack21/odek/internal/danger"
	"testing"
)

func TestResolveDangerousMalformedEnumsDeny(t *testing.T) {
	allow := "allow"
	for _, cfg := range []danger.DangerousConfig{
		{DefaultAction: &allow, Classes: map[danger.RiskClass]danger.Action{danger.SystemWrite: "denny"}},
		{DefaultAction: &allow, Classes: map[danger.RiskClass]danger.Action{"system_writ": danger.Deny}},
	} {
		for _, warn := range []bool{true, false} {
			resolved := resolveDangerous(&cfg, warn)
			if resolved.ActionFor(danger.Safe) != danger.Deny {
				t.Fatal("invalid policy retained allow default")
			}
		}
	}
}
