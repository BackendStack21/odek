# Sandboxing

kode runs agent shell commands inside an **isolated Docker container** when `--sandbox` is active. This document covers all configuration options, the `Dockerfile.kode` build system, security guarantees, and best practices.

## Quick start

```bash
# Enable sandbox with internet access (default network: bridge)
kode run --sandbox "npm install && npm test"

# Use a specific base image
kode run --sandbox --sandbox-image node:20-alpine "echo hello"

# Custom Dockerfile for project-specific tooling
echo 'FROM golang:1.24-alpine
RUN apk add --no-cache protobuf
WORKDIR /workspace' > Dockerfile.kode
kode run --sandbox "protoc --version"
```

## Config reference

All sandbox settings are available in `~/kode/config.json`, `./kode.json`, `KODE_*` env vars, and CLI flags, following the same [priority chain](CONFIG.md).

### Config file fields

```json
{
  "sandbox": true,
  "sandbox_image": "node:20-alpine",
  "sandbox_network": "bridge",
  "sandbox_readonly": false,
  "sandbox_memory": "512m",
  "sandbox_cpus": "2",
  "sandbox_user": "1000:1000",
  "sandbox_env": {
    "HOME": "/home/node",
    "NODE_ENV": "development"
  },
  "sandbox_volumes": [
    "/home/user/.npm:/root/.npm"
  ]
}
```

### Field reference

| Field | Env var | CLI flag | Type | Default | Description |
|-------|---------|----------|------|---------|-------------|
| `sandbox` | `KODE_SANDBOX` | `--sandbox` | bool | `false` | Enable/disable sandbox isolation |
| `sandbox_image` | `KODE_SANDBOX_IMAGE` | `--sandbox-image` | string | `alpine:latest` | Docker image for the sandbox container |
| `sandbox_network` | `KODE_SANDBOX_NETWORK` | `--sandbox-network` | string | `bridge` | Docker network mode |
| `sandbox_readonly` | `KODE_SANDBOX_READONLY` | `--sandbox-readonly` | bool | `false` | Mount working directory read-only |
| `sandbox_memory` | `KODE_SANDBOX_MEMORY` | `--sandbox-memory` | string | `""` | Memory limit (e.g. `512m`, `2g`) |
| `sandbox_cpus` | `KODE_SANDBOX_CPUS` | `--sandbox-cpus` | string | `""` | CPU limit (e.g. `0.5`, `2`) |
| `sandbox_user` | `KODE_SANDBOX_USER` | `--sandbox-user` | string | `""` | Run as user (`uid:gid` or name) |
| `sandbox_env` | — | — | object | `{}` | Extra env vars injected into container |
| `sandbox_volumes` | — | — | array | `[]` | Extra volume mounts (`host:container`) |

> **Note:** `sandbox_env` and `sandbox_volumes` are config-file-only — they're too complex for flat env vars or CLI flags. For all other fields, env vars and CLI flags follow the standard `KODE_*` pattern.

### Env var examples

```bash
KODE_SANDBOX=true \
KODE_SANDBOX_IMAGE=python:3.12-slim \
KODE_SANDBOX_NETWORK=none \
KODE_SANDBOX_READONLY=true \
KODE_SANDBOX_MEMORY=1g \
KODE_SANDBOX_CPUS=4 \
KODE_SANDBOX_USER=1000:1000 \
  kode run "process untrusted data"
```

### CLI flag examples

```bash
# Run (single-shot)
kode run \
  --sandbox \
  --sandbox-image node:20-alpine \
  --sandbox-network bridge \
  --sandbox-readonly \
  --sandbox-memory 512m \
  --sandbox-cpus 2 \
  --sandbox-user 1000:1000 \
  "run build"

# REPL (interactive multi-turn)
kode repl \
  --sandbox \
  --sandbox-image golang:1.24-alpine \
  --sandbox-memory 2g \
  --model deepseek-v4-pro
```

## Docker image control

kode provides two ways to control the sandbox environment:

### 1. `sandbox_image` (simple)

Pick any public or private Docker image:

```bash
# Node.js
kode run --sandbox --sandbox-image node:20-alpine "npm run build"

# Python
kode run --sandbox --sandbox-image python:3.12-slim "pytest"

# Go
kode run --sandbox --sandbox-image golang:1.24-alpine "go test ./..."

# GPU workload
kode run --sandbox --sandbox-image nvidia/cuda:12.2-runtime "nvidia-smi"
```

### 2. `Dockerfile.kode` (advanced)

Place a `Dockerfile.kode` in your working directory for **project-specific, pre-baked tooling**. kode auto-detects it and builds an image with a content-hash tag.

```dockerfile
# Dockerfile.kode
FROM node:20-alpine

# Pre-install project dependencies
RUN apk add --no-cache git openssh
RUN npm install -g typescript tsx prettier

# Set up any user-level config
ENV NODE_ENV=development
WORKDIR /workspace
```

Build behavior:
- kode checks for `Dockerfile.kode` in the working directory
- If found and no explicit `sandbox_image` is configured, kode builds it
- The image is tagged as `kode-sandbox:<sha256[:12]>` based on file content hash
- **Cached:** the image is only rebuilt when `Dockerfile.kode` changes
- First build takes ~5–30s depending on the image; subsequent runs are instant

**Priority:**
1. `sandbox_image` config field → use that image directly (explicit wins)
2. `Dockerfile.kode` exists → build and use it
3. Neither → `alpine:latest`

## Network modes

| Mode | Internet | Host access | Use case |
|------|----------|-------------|----------|
| `bridge` (default) | ✅ Yes | ❌ No | `npm install`, `go mod download`, `git clone`, API calls |
| `none` | ❌ No | ❌ No | Fully isolated — untrusted code, malware scans |
| `host` | ✅ Yes | ✅ Yes | Debugging, local services, port sniffing |

**Security note:** `bridge` gives the container internet access but isolates it from the host's network stack (no access to `localhost:port` on the host, no access to your LAN). `host` mode removes that isolation — use only when you need to connect to a service on the host.

## Read-only mode

When `sandbox_readonly` is `true`, the working directory is mounted **read-only** inside the container:

```bash
kode run --sandbox --sandbox-readonly "ls -la /workspace"   # can read
kode run --sandbox --sandbox-readonly "touch /workspace/x"  # fails
```

## Security guarantees

kode's sandbox follows the principle of **least privilege with progressive opt-in**.

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
  "sandbox_network": "bridge",
  "sandbox_readonly": false,
  "sandbox_memory": "2g",
  "sandbox_env": {
    "NODE_ENV": "development",
    "NPM_CONFIG_CACHE": "/tmp/.npm"
  },
  "sandbox_volumes": [
    "/root/.npm:/root/.npm"
  ]
}
```

### CI-style sandbox (Go project)

```json
{
  "sandbox": true,
  "sandbox_image": "golang:1.24-alpine",
  "sandbox_network": "bridge",
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

- **GPU passthrough** is not yet configurable via kode flags — use `Dockerfile.kode` with `nvidia/cuda` images and run the agent without sandbox mode for now
- **Docker-in-Docker** requires special volume mounts (`/var/run/docker.sock`) — not recommended with sandbox mode
- **Windows containers** are not supported (tested on Linux only)
