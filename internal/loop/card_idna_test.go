package loop

import (
	"testing"

	"github.com/BackendStack21/odek/internal/danger"
)

func TestClassifyToolCall_FullwidthInternalHosts(t *testing.T) {
	risk, _ := classifyToolCall("browser", `{"action":"navigate","url":"http://１２７.０.０.１:8080/admin"}`)
	if risk != danger.SystemWrite {
		t.Errorf("fullwidth loopback on the card = %q, want system_write", risk)
	}
	risk, _ = classifyToolCall("http_request", `{"url":"http://１６９.２５４.１６９.２５４/latest/meta-data/"}`)
	if risk != danger.SystemWrite {
		t.Errorf("fullwidth metadata on the card = %q, want system_write", risk)
	}
}
