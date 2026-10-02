package danger

import "testing"

// Workspace tools retain their actual execution, mutation and network effects.
// Routine use does not turn project code or helper execution into inspection.
func TestClassify_RoutineWorkspaceToolsFollowEffects(t *testing.T) {
	tests := []struct {
		cmd string
		cls RiskClass
	}{
		// Compile/test matches go build / go test.
		{"cargo build", CodeExecution},
		{"cargo build --release", CodeExecution},
		{"cargo test", CodeExecution},
		{"cargo check", CodeExecution},
		{"cargo clippy", CodeExecution},
		{"cargo fmt", Safe},
		{"go test ./...", CodeExecution},
		{"go build -o bin/x .", CodeExecution},

		// Workspace file mutation, including verbs that used to fall
		// through to unknown (deny) because they were missing from
		// writePrefixes.
		{"ln -s a b", LocalWrite},
		{"chown user file", LocalWrite},
		{"chgrp staff file", LocalWrite},
		{"install bin/x dest", LocalWrite},
		{"tar -tzf archive.tar.gz", Safe},
		{"tar -xzf archive.tar.gz", LocalWrite},
		{"unzip -l file.zip", Safe},
		{"unzip file.zip", LocalWrite},
		{"gzip -d f.gz", LocalWrite},
		{"gunzip f.gz", LocalWrite},

		// Process signals except init/broadcast.
		{"kill 123", LocalWrite},
		{"kill -9 123", LocalWrite},
		{"pkill x", LocalWrite},
		{"killall x", LocalWrite},

		// Docker inspect / lifecycle. compose down without -v is
		// disposable container state, like git rm.
		{"docker ps", Safe},
		{"docker images", Safe},
		{"docker logs ctr", Safe},
		{"docker inspect ctr", Safe},
		{"docker compose ps", Safe},
		{"docker compose -f compose.yml ps", Safe},
		{"docker compose down", LocalWrite},
		{"docker stop ctr", LocalWrite},
		{"docker rm ctr", LocalWrite},
		{"docker --version", Safe},

		// uv inspect
		{"uv --help", Safe},
		{"uv tool list", Safe},

		// Language toolchains: compile / format / lint.
		{"gofmt -l .", Safe},
		{"gofmt -w .", LocalWrite},
		{"goimports -w .", LocalWrite},
		{"golangci-lint run", CodeExecution},
		{"staticcheck ./...", Safe},
		{"rustc --version", Safe},
		{"rustc src/main.rs", CodeExecution},
		{"rustfmt src/main.rs", LocalWrite},
		{"gcc -o a a.c", LocalWrite},
		{"clang -o a a.c", LocalWrite},
		{"tsc --noEmit", CodeExecution},
		{"eslint src", CodeExecution},
		{"prettier --write .", CodeExecution},
		{"ruff check .", Safe},
		{"black .", LocalWrite},
		{"mypy src", CodeExecution},
		{"javac Main.java", CodeExecution},
		{"cmake --version", Safe},
		{"mvn test", CodeExecution},
		{"gradle test", CodeExecution},
		{"dotnet build", CodeExecution},
		{"dotnet test", CodeExecution},
		{"java -version", Safe},

		// More archives + patch.
		{"xz -d f.xz", LocalWrite},
		{"unxz f.xz", LocalWrite},
		{"bzip2 -d f.bz2", LocalWrite},
		{"zstd -d f.zst", LocalWrite},
		{"7z x archive.7z", LocalWrite},
		{"7z l archive.7z", Safe},
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
		{"rubocop", CodeExecution},
		{"stylua src", LocalWrite},
		{"shfmt -w .", LocalWrite},
		{"shellcheck script.sh", Safe},
		{"hadolint Dockerfile", Safe},
		{"yamllint .", Safe},
		{"swiftc main.swift", CodeExecution},
		{"swift --version", Safe},
		{"swift build", CodeExecution},
		{"kotlinc Hello.kt", CodeExecution},
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
		{"ccache gcc -c a.c", LocalWrite},
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
		if act := cfg.ActionForCommand(tt.cmd); act != cfg.ActionFor(tt.cls) {
			t.Errorf("ActionForCommand(%q) = %s, want the default action for class %s", tt.cmd, act, got)
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
