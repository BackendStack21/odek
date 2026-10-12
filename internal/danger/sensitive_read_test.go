package danger

import "testing"

func TestIsSensitiveReadPath_Cases(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	sensitive := []string{
		"/ws/.ſsh/config", "/ws/.ſecrets/a", "/ws/ſecrets.yaml",
		"/ws/.envrc", "/ws/prod.env", "/ws/.env-prod", "/ws/server.key", "/ws/ssl/app.key",
		"/home/user/.bash_history", "/home/user/.zsh_history", "/home/user/.local/share/keyrings/login.keyring",
		"/home/user/.docker/config.json", "/home/user/.ssh/id_rsa", "/etc/shadow",
	}
	for _, p := range sensitive {
		if !IsSensitiveReadPath("/ws", p) && !IsSensitiveReadPath("", p) {
			t.Errorf("%q should be sensitive", p)
		}
	}
	ok := []string{
		"/ws/.docker/Dockerfile", "/ws/translations.key", "/ws/.env.local.example",
		"/ws/.env.example", "/ws/main.go", "/opt/app/main.go",
	}
	for _, p := range ok {
		if IsSensitiveReadPath("/ws", p) {
			t.Errorf("%q should load", p)
		}
	}
	// Ancestors of the root do not count.
	if IsSensitiveReadPath("/home/u/credentials/proj", "/home/u/credentials/proj/app.json") {
		t.Error("ancestor directory name blocked the workspace")
	}
	if IsSensitiveReadPath("/srv/secrets/proj", "/srv/secrets/proj/data.json") {
		t.Error("ancestor secrets directory blocked the workspace")
	}
	if !IsSensitiveReadPath("/home/u/proj", "/home/u/proj/secrets/data.json") {
		t.Error("secrets directory inside the workspace must still block")
	}
}
