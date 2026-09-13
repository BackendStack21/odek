package danger

import "testing"

func TestMalformedPolicyFailsClosed(t *testing.T) {
	for _, cfg := range []DangerousConfig{
		{Classes: map[RiskClass]Action{SystemWrite: "denny"}},
		{Classes: map[RiskClass]Action{"system_writ": Deny}},
		{Classes: map[RiskClass]Action{Safe: ReadOnly}},
		{DefaultAction: strPtr("alow")},
	} {
		cfg.Allowlist = []string{"echo allowed"}
		if cfg.Validate() == nil {
			t.Fatal("malformed policy validated")
		}
		if cfg.ActionFor(Safe) != Deny || cfg.ActionForCommand("echo allowed") != Deny {
			t.Fatal("malformed policy allowed operation")
		}
		if err := cfg.CheckOperation(ToolOperation{Name: "write_file", Resource: "/etc/example", Risk: SystemWrite}, nil); err == nil {
			t.Fatal("malformed policy allowed API operation")
		}
	}
	valid := DangerousConfig{DefaultAction: strPtr("allow")}
	if valid.ActionFor(RiskClass("misspelled")) != Deny {
		t.Fatal("unknown API class allowed")
	}
}
