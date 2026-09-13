package danger

import "testing"

// Routine workspace tools must follow effect, not "this binary is scary."
// Prompt/deny is reserved for hard-to-undo or executing work; reversible
// local porcelain stays allow (safe / local_write / network_egress).
func TestClassify_RoutineWorkspaceToolsStayAllow(t *testing.T) {
	tests := []struct {
		cmd string
		cls RiskClass
	}{
		// Compile/test matches go build / go test.
		{"cargo build", Safe},
		{"cargo build --release", Safe},
		{"cargo test", Safe},
		{"cargo check", Safe},
		{"cargo clippy", Safe},
		{"cargo fmt", Safe},
		{"go test ./...", Safe},
		{"go build -o bin/x .", Safe},

		// Workspace file mutation, including verbs that used to fall
		// through to unknown (deny) because they were missing from
		// writePrefixes.
		{"ln -s a b", LocalWrite},
		{"chown user file", LocalWrite},
		{"chgrp staff file", LocalWrite},
		{"install bin/x dest", LocalWrite},
		{"tar -tzf archive.tar.gz", LocalWrite},
		{"tar -xzf archive.tar.gz", LocalWrite},
		{"unzip -l file.zip", LocalWrite},
		{"unzip file.zip", LocalWrite},
		{"gzip -d f.gz", LocalWrite},
		{"gunzip f.gz", LocalWrite},

		// Process signals except init/broadcast.
		{"kill 123", Safe},
		{"kill -9 123", Safe},
		{"pkill x", Safe},
		{"killall x", Safe},

		// Docker inspect / lifecycle. compose down without -v is
		// disposable container state, like git rm.
		{"docker ps", Safe},
		{"docker images", Safe},
		{"docker logs ctr", Safe},
		{"docker inspect ctr", Safe},
		{"docker compose ps", Safe},
		{"docker compose -f compose.yml ps", Safe},
		{"docker compose down", Safe},
		{"docker stop ctr", Safe},
		{"docker rm ctr", Safe},
		{"docker --version", Safe},

		// uv inspect
		{"uv --help", Safe},
		{"uv tool list", Safe},
	}
	cfg := DangerousConfig{}
	for _, tt := range tests {
		got := Classify(tt.cmd)
		if got != tt.cls {
			t.Errorf("Classify(%q) = %s, want %s", tt.cmd, got, tt.cls)
		}
		if act := cfg.ActionForCommand(tt.cmd); act != Allow {
			t.Errorf("ActionForCommand(%q) = %s, want allow (class %s)", tt.cmd, act, got)
		}
	}
}

func TestClassify_RoutineToolsStillEscalateWhenEffectRequiresIt(t *testing.T) {
	tests := []struct {
		cmd string
		cls RiskClass
	}{
		{"cargo run", CodeExecution},
		{"cargo bench", CodeExecution},
		{"cargo install ripgrep", Install},
		{"ln -s a /etc/foo", SystemWrite},
		{"chown root:root /etc/hosts", SystemWrite},
		{"install bin/x /usr/bin/x", SystemWrite},
		{"tar --to-command=sh -x -f a.tar", CodeExecution},
		{"tar --use-compress-program=sh -xf a.tar", CodeExecution},
		{"kill 1", SystemWrite},
		{"kill -- -1", SystemWrite},
		{"docker compose up", CodeExecution},
		{"docker compose -f compose.yml up -d", CodeExecution},
		{"docker run alpine", CodeExecution},
		{"docker exec ctr sh", CodeExecution},
		{"docker build .", CodeExecution},
		{"docker pull alpine", NetworkEgress},
		{"docker system prune", SystemWrite},
		{"docker rmi alpine", SystemWrite},
		{"docker volume rm v", SystemWrite},
		{"docker compose down -v", SystemWrite},
		{"docker scout", Unknown},
		{"uv run pytest", CodeExecution},
		{"uv tool run ruff", CodeExecution},
		{"uv sync", Install},
		{"uv pip install x", Install},
		{"uv add x", Install},
		{"uv tool install ruff", Install},
		{"env", SystemWrite},
		{"printenv", SystemWrite},
		{"npm test", CodeExecution},
		{"make test", CodeExecution},
		{"pytest", CodeExecution},
	}
	for _, tt := range tests {
		if got := Classify(tt.cmd); got != tt.cls {
			t.Errorf("Classify(%q) = %s, want %s", tt.cmd, got, tt.cls)
		}
	}
}
