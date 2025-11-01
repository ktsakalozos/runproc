# Guidance for Automation Agents and Contributors

This document gives a concise, practical checklist for automation agents (and humans) working in this repository.

The project provides a minimal, experimental OCI runtime CLI named `runproc`, intended for basic containerd integration on Linux. It is not production-ready.

## Environment and prerequisites

- OS: Linux only
- Go: 1.21+
- Optional for E2E: `kubectl` and either `docker` or `podman`
- Kind: auto-downloaded by the tests if missing (linux/amd64)

## Build and test workflow

- Build the runtime:

  ```bash
  make build
  ```

- Run integration tests (unit-style, no cluster):

  ```bash
  make integration-test
  ```

- Run Kind E2E tests (creates/uses a Kind cluster; Linux-only):

  ```bash
  RUNPROC_KIND_E2E=1 make integration-test
  ```

The E2E suite will:
- Create (or reuse) a Kind cluster named `runproc-kind-e2e`
- Copy `runproc` into the node, configure containerd to use it via a runtime handler, and restart the node
- Create a `RuntimeClass` named `runproc`
- Run pods that validate runtime behavior, including host-mode and a no-pull variant

## Runtime behavior contract (MVP)

- Subcommands: `create`, `start`, `state`, `kill`, `delete`, `run`
  - `run` is convenience for create+start and then waiting
- Global flags (runc-compatible):
  - `--root <dir>`: state directory (or use env `RUNPROC_STATE_DIR`)
  - `--log <path>`, `--log-format <text|json>`: write minimal OCI-style error logs if provided
- State: stored as JSON under the state dir; `state` self-heals “running” to “stopped” if the PID has exited
- Isolation: none (no namespaces, cgroups, LSM, seccomp) — process is started directly
- Rootfs/chroot:
  - If running as non-root: no chroot, simple bundles like `examples/echo` require no rootfs
  - If running as root: perform a minimal chroot into bundle `rootfs` unless host-mode is enabled; no mounts/pivot_root
- Host mode:
  - Set `RUNPROC_HOST=1` (or OCI annotation `runproc.host: "1"`) to skip chroot and operate on the node filesystem
- Delete semantics:
  - Tests and helpers use graceful deletion only; no forced deletion path

## Containerd integration

Example snippet for `/etc/containerd/config.toml`:

```
[plugins."io.containerd.grpc.v1.cri".containerd.runtimes.runproc]
  runtime_type = "io.containerd.runc.v2"
  [plugins."io.containerd.grpc.v1.cri".containerd.runtimes.runproc.options]
    BinaryName = "/usr/local/bin/runproc"
```

Restart containerd after editing the config, then reference with `ctr ... --runtime runproc` or a Kubernetes `RuntimeClass` named `runproc`.

## Testing notes and expectations

- Integration tests verify:
  - `run` echo behavior
  - `create/start` ordering (no output before `start`)
- Kind E2E tests validate:
  - RuntimeClass pod execution and log capture
  - Host-mode execution reading `/etc/hostname`
  - No-pull host-mode execution using preloaded `registry.k8s.io/pause:3.9`
- Deletion policy:
  - Tests rely on graceful-only deletion (no `--force`, no `--grace-period=0`)

## Troubleshooting tips

- YAML indentation: YAML forbids tabs. Use spaces only or switch to JSON manifests to avoid whitespace issues.
- Image pulls: Some environments block pulling public images. Prefer host-mode tests and the `pause:3.9` no-pull variant.
- Cluster readiness: After runtime handler config injection, the Kind node is restarted and the suite waits for `Ready`.
- Diagnostics: On timeouts, tests log `kubectl describe pod` and `kubectl logs` to aid debugging. If `--log` is provided, runproc will emit minimal OCI-style error logs for the shim to consume.

## Contribution checklist for agents

- Keep changes minimal; avoid adding heavy dependencies
- Always:
  - `make build`
  - `make integration-test`
  - Optionally: `RUNPROC_KIND_E2E=1 make integration-test` (Linux-only)
- Update `README.md` and/or this `AGENTS.md` when changing behavior or workflows
- Preserve Linux-only scope and current MVP limitations unless explicitly extending them

## Non-goals and limitations

- Not production-ready; intended for experimentation
- No namespaces/cgroups/mounts/LSM/seccomp
- No stdio FIFO plumbing to containerd-shim
- No `exec` subcommand
- Linux only
