# runproc

A minimal, experimental OCI runtime CLI (MVP) intended to be used by containerd as a very basic, runc-compatible runtime. This MVP intentionally skips namespaces, cgroups, mounts, seccomp/AppArmor/SELinux, hooks, and exec. It spawns the requested process and manages lifecycle JSON state.

Not production-ready. For experimentation only.

## Build

Recommended (produces `./runproc` at the repo root):

```bash
make build
```

Alternative without Makefile:

```bash
go build -o ./runproc ./cmd/runproc
```

Requires Go 1.21+.

## Try locally (without containerd)

Use the example bundle in `examples/echo`:

```bash
# Build
make build

# Choose a state directory for container records
STATE_DIR=$(mktemp -d)
export RUNPROC_STATE_DIR=$STATE_DIR

# Create & start
./runproc create --pid-file $STATE_DIR/pid demo $(pwd)/examples/echo
./runproc state demo
./runproc start demo

# Wait by invoking run (creates a new container, so for demo use a new id)
./runproc run demo2 $(pwd)/examples/echo || true
```

Notes:
- When running as non-root, runproc does not chroot and no rootfs is required for simple examples like `examples/echo`.
- When running as root, runproc will perform a minimal chroot into the bundle's `rootfs` unless host-mode is enabled (see Host mode below). No mounts or pivot_root are performed.

## CLI and behavior

- Subcommands: `create`, `start`, `state`, `kill`, `delete`, `run` (convenience: create+start, then wait).
- Global flags (runc-compatible):
  - `--root <dir>`: state directory (alternatively `RUNPROC_STATE_DIR` env var).
  - `--log <path>`, `--log-format <text|json>`: if provided, runproc writes minimal OCI-style error logs for shim consumption.
- Tolerant runc CLI compatibility: common flag shapes and `kill` signal forms are accepted.
- State is written as JSON files under the state directory; `state` self-heals a "running" record to "stopped" if the PID has exited.

## Host mode

Run commands directly on the host filesystem (skip chroot):

- Set env: `RUNPROC_HOST=1`.
- Or set OCI spec annotation (if available): `runproc.host: "1"`.

This is useful in Kubernetes tests to avoid image pulls and run node-local commands.

## Configure containerd (optional)

In `/etc/containerd/config.toml`, set:

```
[plugins."io.containerd.grpc.v1.cri".containerd.runtimes.runproc]
  runtime_type = "io.containerd.runc.v2"
  [plugins."io.containerd.grpc.v1.cri".containerd.runtimes.runproc.options]
    BinaryName = "/usr/local/bin/runproc"
```

Then restart containerd and try with `ctr` or Kubernetes using `runtimeClassName: runproc`.

## Kind E2E tests (optional)

Run the end-to-end tests against a Kind cluster (Linux-only):

```bash
RUNPROC_KIND_E2E=1 make integration-test
```

What it does:
- Creates (or reuses) a Kind cluster.
- Copies the built `runproc` binary into the node and configures a runtime handler.
- Creates a `RuntimeClass` named `runproc` and runs test Pods.
- Includes host-mode tests (and a no-pull host-mode test using preloaded `pause:3.9`).

Prerequisites: `kubectl`, and either `docker` or `podman`. The test will download `kind` locally if not found.

## Limitations

- No isolation primitives (namespaces, cgroups, LSM, seccomp).
- No mounts/pivot_root; only a minimal chroot when running as root (unless host-mode is enabled).
- No `exec` subcommand; single process lifecycle only.
- No stdio FIFO plumbing with containerd-shim.
- Minimal state schema; not full runc output compatibility.
- Linux only.