# Sandboxing

odek runs agent shell commands inside an **isolated Docker container** when `--sandbox` is active. This document covers all configuration options, the `Dockerfile.odek` build system, security guarantees, and best practices.

## Quick start

```bash
# Enable sandbox with no network (default: none)
odek run --sandbox "npm install && npm test"

# Enable sandbox with internet access
odek run --sandbox --sandbox-network bridge "npm install && npm test"

# Use a specific base image
odek run --sandbox --sandbox-image node:20-alpine "echo hello"

# Custom Dockerfile for project-specific tooling
echo 'FROM golang:1.24-alpine
RUN apk add --no-cache protobuf
WORKDIR /workspace' > Dockerfile.odek
odek run --sandbox "protoc --version"
```

## Config reference

All sandbox settings are available in `~/.odek/config.json`, `./odek.json`, `ODEK_*` env vars, and CLI flags, following the same [priority chain](CONFIG.md).

### Config file fields

```json
{
  "sandbox": true,
  "sandbox_image": "node:20-alpine",
  "sandbox_network": "none",
  "sandbox_readonly": false,
  "sandbox_memory": "512m",
  "sandbox_cpus": "2",
  "sandbox_user": "1000:1000",
  "sandbox_env": {
    "HOME": "/home/node",
    "NODE_ENV": "development"
  },
  "sandbox_volumes": [
    "./.npm:/root/.npm"
  ]
}
```

### Field reference

| Field | Env var | CLI flag | Type | Default | Description |
|-------|---------|----------|------|---------|-------------|
| `sandbox` | `ODEK_SANDBOX` | `--sandbox` | bool | `false` | Enable/disable sandbox isolation |
| `sandbox_image` | `ODEK_SANDBOX_IMAGE` | `--sandbox-image` | string | `alpine:latest` | Docker image for the sandbox container |
| `sandbox_network` | `ODEK_SANDBOX_NETWORK` | `--sandbox-network` | string | `none` | Docker network mode |
| `sandbox_readonly` | `ODEK_SANDBOX_READONLY` | `--sandbox-readonly` | bool | `false` | Mount working directory read-only |
| `sandbox_memory` | `ODEK_SANDBOX_MEMORY` | `--sandbox-memory` | string | `""` | Memory limit (e.g. `512m`, `2g`) |
| `sandbox_cpus` | `ODEK_SANDBOX_CPUS` | `--sandbox-cpus` | string | `""` | CPU limit (e.g. `0.5`, `2`) |
| `sandbox_user` | `ODEK_SANDBOX_USER` | `--sandbox-user` | string | `""` | Run as user (`uid:gid` or name) |
| `sandbox_env` | — | — | object | `{}` | Extra env vars injected into container |
| `sandbox_volumes` | — | — | array | `[]` | Extra volume mounts (`host:container`) |

> **Note:** `sandbox_env` and `sandbox_volumes` are config-file-only — they're too complex for flat env vars or CLI flags. For all other fields, env vars and CLI flags follow the standard `ODEK_*` pattern.
>
> **Security restriction on `sandbox_volumes`:** Extra volume host paths must be
> inside the working directory. Absolute paths outside the project (e.g.
> `/var/run/docker.sock`, `/etc`, `/home/user/...`) and paths containing `..`
> or symlinks are rejected. Relative paths are resolved relative to the working
> directory and must stay inside it.

### Env var examples

```bash
ODEK_SANDBOX=true \
ODEK_SANDBOX_IMAGE=python:3.12-slim \
ODEK_SANDBOX_NETWORK=none \
ODEK_SANDBOX_READONLY=true \
ODEK_SANDBOX_MEMORY=1g \
ODEK_SANDBOX_CPUS=4 \
ODEK_SANDBOX_USER=1000:1000 \
  odek run "process untrusted data"
```

### CLI flag examples

```bash
# Run (single-shot)
odek run \
  --sandbox \
  --sandbox-image node:20-alpine \
  --sandbox-network none \
  --sandbox-readonly \
  --sandbox-memory 512m \
  --sandbox-cpus 2 \
  --sandbox-user 1000:1000 \
  "run build"

# REPL (interactive multi-turn)
odek repl \
  --sandbox \
  --sandbox-image golang:1.24-alpine \
  --sandbox-memory 2g \
  --model deepseek-v4-pro
```

## Docker image control

`odek provides two ways to control the sandbox environment:

### 1. `sandbox_image` (simple)

Pick any public or private Docker image:

```bash
# Node.js
odek run --sandbox --sandbox-image node:20-alpine "npm run build"

# Python
odek run --sandbox --sandbox-image python:3.12-slim "pytest"

# Go
odek run --sandbox --sandbox-image golang:1.24-alpine "go test ./..."

# GPU workload
odek run --sandbox --sandbox-image nvidia/cuda:12.2-runtime "nvidia-smi"
```

### 2. `Dockerfile.odek` (advanced)

Place a `Dockerfile.odek` in your working directory for **project-specific, pre-baked tooling**. odek auto-detects it and builds an image with a content-hash tag.

```dockerfile
# Dockerfile.odek
FROM node:20-alpine

# Pre-install project dependencies
RUN apk add --no-cache git openssh
RUN npm install -g typescript tsx prettier

# Set up any user-level config
ENV NODE_ENV=development
WORKDIR /workspace
```

Build behavior:
- odek check for `Dockerfile.odek` in the working directory
- If found and no explicit `sandbox_image` is configured, odek builds it
- The image is tagged as `odek-sandbox:<sha256[:12]>` based on file content hash
- **Cached:** the image is only rebuilt when `Dockerfile.odek` changes
- First build takes ~5–30s depending on the image; subsequent runs are instant

**Priority:**
1. `sandbox_image` config field → use that image directly (explicit wins)
2. `Dockerfile.odek` exists → build and use it
3. Neither → `alpine:latest`

## Network modes

| Mode | Internet | Host access | Use case |
|------|----------|-------------|----------|
| `bridge` (default) | ✅ Yes | ❌ No | `npm install`, `go mod download`, `git clone`, API calls |
| `none` | ❌ No | ❌ No | Fully isolated — untrusted code, malware scans |
| `host` | ✅ Yes | ✅ Yes | Debugging, local services, port sniffing |

**Security note:** `bridge` gives the container internet access but isolates it from the host's network stack (no access to `localhost:port` on the host, no access to your LAN). `host` mode removes that isolation — use only when you need to connect to a service on the host.

## File injection

When running with `--sandbox --ctx <file>`, odek copies the ctx files into the container via `docker cp`:

- Files within the working directory preserve their relative path (`--ctx subdir/file.txt` → `/workspace/subdir/file.txt`)
- Files outside the working directory use their basename (`--ctx /etc/hosts` → `/workspace/hosts`)
- Missing files and directories are silently skipped
- In read-only mode, injection still works (docker cp writes to the container's overlay, not the volume bind-mount)

This ensures the agent can both see the file content in its context **and** operate on the physical file using `read_file`, `patch`, `shell cat`, etc. without any "content visible but file doesn't exist" gap.

## Read-only mode

When `sandbox_readonly` is `true`, the working directory is mounted **read-only** inside the container:

```bash
odek run --sandbox --sandbox-readonly "ls -la /workspace"   # can read
odek run --sandbox --sandbox-readonly "touch /workspace/x"  # fails
```

## Security guarantees

odek's sandbox follows the principle of **least privilege with progressive opt-in**.

### Default (no sandbox config overrides)

| Hardening | How it's enforced |
|-----------|------------------|
| **No capabilities** | `--cap-drop ALL` — even root has zero Linux capabilities |
| **No privilege escalation** | `--security-opt no-new-privileges` — `setuid` binaries can't escalate |
| **No executable /tmp** | `--tmpfs /tmp:noexec` — can't download+run binaries from temp |
| **Auto-cleanup** | `--rm` — container is destroyed on exit, no state persists |
| **Isolated process** | Detached `sleep infinity` — agent commands run via `docker exec` |
| **Ephemeral** | Container destroyed when agent finishes or is interrupted |

### With `--sandbox-network none`

| Hardening | How it's enforced |
|-----------|------------------|
| **No network** | `--network none` — container cannot reach internet or LAN |

### With `--sandbox-readonly`

| Hardening | How it's enforced |
|-----------|------------------|
| **Read-only workspace** | `-v $PWD:/workspace:ro` — agent can read but not modify project files |

## Use case patterns

### Maximum security (untrusted code analysis)

```json
{
  "sandbox": true,
  "sandbox_image": "alpine:latest",
  "sandbox_network": "none",
  "sandbox_readonly": true
}
```

### Development sandbox (Node.js project)

```json
{
  "sandbox": true,
  "sandbox_image": "node:20-alpine",
  "sandbox_network": "none",
  "sandbox_readonly": false,
  "sandbox_memory": "2g",
  "sandbox_env": {
    "NODE_ENV": "development",
    "NPM_CONFIG_CACHE": "/tmp/.npm"
  },
  "sandbox_volumes": [
    "./.npm:/root/.npm"
  ]
}
```

### CI-style sandbox (Go project)

```json
{
  "sandbox": true,
  "sandbox_image": "golang:1.24-alpine",
  "sandbox_network": "none",
  "sandbox_readonly": false,
  "sandbox_memory": "4g",
  "sandbox_cpus": "4",
  "sandbox_env": {
    "GOMAXPROCS": "4",
    "GOCACHE": "/tmp/go-cache"
  }
}
```

## Limitations

- **GPU passthrough** is not yet configurable via odek flags — use `Dockerfile.odek` with `nvidia/cuda` images and run the agent without sandbox mode for now
- **Docker-in-Docker** requires special volume mounts (`/var/run/docker.sock`) — not recommended with sandbox mode
- **Windows containers** are not supported (tested on Linux only)
