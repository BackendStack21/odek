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

		// Language toolchains: compile / format / lint.
		{"gofmt -l .", Safe},
		{"gofmt -w .", Safe},
		{"goimports -w .", Safe},
		{"golangci-lint run", Safe},
		{"staticcheck ./...", Safe},
		{"rustc --version", Safe},
		{"rustc src/main.rs", Safe},
		{"rustfmt src/main.rs", Safe},
		{"gcc -o a a.c", Safe},
		{"clang -o a a.c", Safe},
		{"tsc --noEmit", Safe},
		{"eslint src", Safe},
		{"prettier --write .", Safe},
		{"ruff check .", Safe},
		{"black .", Safe},
		{"mypy src", Safe},
		{"javac Main.java", Safe},
		{"cmake --version", Safe},
		{"mvn test", Safe},
		{"gradle test", Safe},
		{"dotnet build", Safe},
		{"dotnet test", Safe},
		{"java -version", Safe},

		// More archives + patch.
		{"xz -d f.xz", LocalWrite},
		{"unxz f.xz", LocalWrite},
		{"bzip2 -d f.bz2", LocalWrite},
		{"zstd -d f.zst", LocalWrite},
		{"7z x archive.7z", LocalWrite},
		{"7z l archive.7z", LocalWrite},
		{"unrar x archive.rar", LocalWrite},
		{"patch -p1 < diff.patch", LocalWrite},

		// Container CLI twins.
		{"podman ps", Safe},
		{"nerdctl ps", Safe},

		// Host package-manager inspect.
		{"brew --version", Safe},
		{"brew list", Safe},
		{"brew info git", Safe},
		{"apt list --installed", Safe},
		{"dpkg -l", Safe},

		// Recipe-runner meta queries (the run itself prompts).
		{"just --list", Safe},
		{"just --version", Safe},
		{"jest --version", Safe},
		{"bazel --version", Safe},

		// Binary / host inspect.
		{"objdump -d bin/x", Safe},
		{"nm bin/x", Safe},
		{"otool -L bin/x", Safe},
		{"ldd bin/x", Safe},
		{"readelf -h bin/x", Safe},
		{"ip addr", Safe},
		{"ifconfig", Safe},
		{"openssl version", Safe},
		{"gpg --list-keys", Safe},
		{"gpg --version", Safe},
		{"ssh-add -l", Safe},
		{"rustup show", Safe},
		{"rustup --version", Safe},
		{"watch -n 1 ps", Safe},
		{"ping -c 1 example.com", NetworkEgress},
		{"strip bin/x", LocalWrite},
		{"ssh-keygen -l -f id", LocalWrite},
		{"poetry --version", Safe},
		{"bundle --version", Safe},
		{"composer --version", Safe},
		{"npx --version", Safe},
		{"php -l file.php", Safe},
		{"ruby -c file.rb", Safe},
		{"node --check file.js", Safe},
		{"rubocop", Safe},
		{"stylua src", Safe},
		{"shfmt -w .", Safe},
		{"shellcheck script.sh", Safe},
		{"hadolint Dockerfile", Safe},
		{"yamllint .", Safe},
		{"swiftc main.swift", Safe},
		{"swift --version", Safe},
		{"swift build", Safe},
		{"kotlinc Hello.kt", Safe},
		{"ffprobe in.mp4", Safe},
		{"identify in.png", Safe},
		{"sqlite3 --version", Safe},
		{"sqlite3 db.sqlite .tables", Safe},
		{"direnv status", Safe},
		{"nvm ls", Safe},
		{"fnm list", Safe},
		{"pyenv versions", Safe},
		{"asdf list", Safe},
		{"gdb --version", Safe},
		{"pandoc README.md -o out.html", LocalWrite},
		{"ffmpeg -i in.mp4 out.mp4", LocalWrite},
		{"convert in.png out.jpg", LocalWrite},
		{"env -u FOO ls", Safe},
		{"printenv PATH", Safe},
		{"mktemp", LocalWrite},
		{"truncate -s 0 file", LocalWrite},
		{"dos2unix file", LocalWrite},
		{"uuidgen", Safe},
		{"cloc .", Safe},
		{"tokei .", Safe},
		{"pkg-config --libs libfoo", Safe},
		{"protoc --version", Safe},
		{"buf lint", Safe},
		{"iconv -f utf-8 -t ascii", Safe},
		{"sysctl -a", Safe},
		{"sync", Safe},
		{"ccache gcc -c a.c", Safe},
		{"strace ls", Safe},
		{"strace -e open ls", Safe},
		{"redis-cli --version", Safe},
		{"mysql --version", Safe},
		{"redis-cli ping", NetworkEgress},
		{"kubectl get pods", NetworkEgress},
		{"kubectl logs x", NetworkEgress},
		{"kubectl version --client", NetworkEgress},
		{"helm list", NetworkEgress},
		{"terraform plan", NetworkEgress},
		{"terraform validate", NetworkEgress},
		{"aws --version", Safe},
		{"gcloud --version", Safe},
		{"hugo --help", Safe},
		{"hugo", LocalWrite},
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
		{"java Main", CodeExecution},
		{"dotnet run", CodeExecution},
		{"sbt run", CodeExecution},
		{"podman run alpine", CodeExecution},
		{"nerdctl run alpine", CodeExecution},
		{"brew install git", Install},
		{"apt-get install git", Install},
		{"apt-get update", Install},
		{"yum install httpd", Install},
		{"dpkg -i package.deb", Install},
		{"gofmt -w /etc/x", SystemWrite},
		{"just test", CodeExecution},
		{"jest", CodeExecution},
		{"vitest run", CodeExecution},
		{"bazel test //...", CodeExecution},
		{"rake test", CodeExecution},
		{"mix test", CodeExecution},
		{"poetry run pytest", CodeExecution},
		{"bundle exec rspec", CodeExecution},
		{"poetry install", Install},
		{"bundle install", Install},
		{"composer install", Install},
		{"pipenv install", Install},
		{"rustup install stable", Install},
		{"openssl s_client -connect example.com:443", NetworkEgress},
		{"npx cowsay hi", CodeExecution},
		{"php artisan test", CodeExecution},
		{"swift run", CodeExecution},
		{"gdb ./bin", CodeExecution},
		{"sqlite3 db.sqlite '.shell id'", CodeExecution},
		{"direnv exec . ls", CodeExecution},
		{"direnv allow", Persistence},
		{"nvm install 20", Install},
		{"pyenv install 3.12", Install},
		{"printenv", SystemWrite},
		{"env", SystemWrite},
		{"kubectl apply -f x.yaml", SystemWrite},
		{"kubectl delete pod x", SystemWrite},
		{"kubectl exec -it x -- sh", CodeExecution},
		{"helm install x chart", SystemWrite},
		{"terraform apply", SystemWrite},
		{"terraform destroy", SystemWrite},
		{"hugo server", CodeExecution},
		{"buf generate", CodeExecution},
		{"sysctl -w kern.foo=1", SystemWrite},
		{"aws s3 ls", Unknown},
	}
	for _, tt := range tests {
		if got := Classify(tt.cmd); got != tt.cls {
			t.Errorf("Classify(%q) = %s, want %s", tt.cmd, got, tt.cls)
		}
	}
}
