package loop

import (
	"testing"

	"github.com/BackendStack21/odek/internal/danger"
)

// The batch approval card must classify a mutating or credentialed
// http_request as network_upload, like the curl equivalent, so the batch
// gate prompts for it instead of auto-allowing it as plain egress.
func TestRED_ClassifyHTTPRequestUpload(t *testing.T) {
	cases := []struct {
		args string
		want danger.RiskClass
	}{
		{`{"url":"https://api.example.com/x"}`, danger.NetworkEgress},
		{`{"url":"https://api.example.com/x","method":"GET","headers":{"Accept":"*/*"}}`, danger.NetworkEgress},
		{`{"url":"https://api.example.com/x","method":"DELETE"}`, danger.NetworkUpload},
		{`{"url":"https://api.example.com/x","method":"patch"}`, danger.NetworkUpload},
		{`{"url":"https://api.example.com/x","headers":{"Authorization":"Bearer t"}}`, danger.NetworkUpload},
		{`{"url":"https://api.example.com/x","headers":{"PRIVATE-TOKEN":"t"}}`, danger.NetworkUpload},
		// Arguments the classifier cannot read fail closed.
		{`{"url":"https://api.example.com/x","headers":{"Authorization":1}}`, danger.NetworkUpload},
		{`not json`, danger.NetworkUpload},
	}
	for _, c := range cases {
		got, _ := classifyToolCall("http_request", c.args)
		if got != c.want {
			t.Errorf("classifyToolCall(http_request, %s) = %q, want %q", c.args, got, c.want)
		}
	}
}
