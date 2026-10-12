package danger

import "testing"

func TestIsSensitiveReadPath_KeyFileNames(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	for _, p := range []string{
		"/ws/server.key", "/ws/private.key", "/ws/tls.key", "/ws/client.key",
		"/ws/ssl/foo.key", "/ws/ca.key", "/ws/api.key", "/ws/privkey.key",
		"/ws/server-tls.key", "/ws/jwt_signing.key", "/ws/id_rsa.key", "/ws/app.private.key",
	} {
		if !IsSensitiveReadPath("/ws", p) {
			t.Errorf("%q should be sensitive", p)
		}
	}
	for _, p := range []string{
		"/ws/capital-plan.key", "/ws/presentation-client.key", "/ws/translations.key",
		"/ws/ca-overview.key", "/ws/apiary-pitch.key", "/ws/certification-deck.key",
	} {
		if IsSensitiveReadPath("/ws", p) {
			t.Errorf("%q is a document and should load", p)
		}
	}
}
