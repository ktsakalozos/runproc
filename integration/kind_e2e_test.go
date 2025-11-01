package integration

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// This test is opt-in: set RUNPROC_KIND_E2E=1 to enable.
// It will:
// - ensure kind (download to temp if missing)
// - create a kind cluster
// - copy runproc into the node and configure containerd to use it as a runtime handler "runproc"
// - create a RuntimeClass and a Pod that uses it
// - verify the pod prints output
func TestKind_E2E_RuntimeClassPod(t *testing.T) {
	if os.Getenv("RUNPROC_KIND_E2E") != "1" {
		t.Skip("set RUNPROC_KIND_E2E=1 to run this test")
	}
	if runtime.GOOS != "linux" {
		t.Skip("linux only")
	}

	// Build runproc
	root := projectRoot(t)
	build := exec.Command("make", "build")
	build.Dir = root
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("make build failed: %v", err)
	}
	_ = filepath.Join(root, "runproc") // build artifact path (not used directly here)

	// Ensure cluster and runtime are ready
	setupKindAndRuntime(t)

	// Create a Pod using the runtime class
	podYAML := `apiVersion: v1
kind: Pod
metadata:
  name: runproc-echo
spec:
  runtimeClassName: runproc
  restartPolicy: Never
  containers:
  - name: echo
    image: busybox
    command: ["/bin/sh","-c","echo hello-from-kind-runproc"]
`
	if err := kubectlApply(strings.NewReader(podYAML)); err != nil {
		t.Fatalf("apply Pod failed: %v", err)
	}

	// Wait for completion or failure
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			desc, _ := exec.Command("kubectl", "describe", "pod", "runproc-echo").CombinedOutput()
			logs, _ := kubectlLogs("runproc-echo")
			t.Fatalf("timeout waiting for pod; describe:\n%s\nlogs:\n%s", string(desc), logs)
		default:
		}
		phase, err := kubectlJsonPath("runproc-echo", "{.status.phase}")
		if err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		// If logs already contain expected output, consider success even if phase hasn't flipped yet
		if logs, _ := kubectlLogs("runproc-echo"); strings.Contains(logs, "hello-from-kind-runproc") {
			t.Logf("pod runproc-echo logs:\n%s", logs)
			t.Logf("pod phase: %s (accepting success based on logs)", phase)
			// Delete the pod gracefully and ensure it is removed
			if err := kubectlDeleteGraceful("runproc-echo", 45*time.Second); err != nil {
				t.Fatalf("delete Pod failed: %v", err)
			}
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// Verifies we can toggle host-mode execution (no chroot) using env/annotation
func TestKind_E2E_HostMode(t *testing.T) {
	if os.Getenv("RUNPROC_KIND_E2E") != "1" {
		t.Skip("set RUNPROC_KIND_E2E=1 to run this test")
	}
	if runtime.GOOS != "linux" {
		t.Skip("linux only")
	}
	// Ensure the Kind cluster and runproc runtime are set up (idempotent)
	setupKindAndRuntime(t)

	// Create a pod that reads the node's hostname from /etc/hostname. We enable host mode via env.
	podYAML := `{
			"apiVersion": "v1",
			"kind": "Pod",
			"metadata": { "name": "runproc-host-mode" },
			"spec": {
				"runtimeClassName": "runproc",
				"restartPolicy": "Never",
				"containers": [
					{
						"name": "hostcat",
						"image": "registry.k8s.io/pause:3.9",
						"env": [ { "name": "RUNPROC_HOST", "value": "1" } ],
						"command": [ "/bin/sh", "-c", "cat /etc/hostname" ]
					}
				]
			}
		}`
	if err := kubectlApply(strings.NewReader(podYAML)); err != nil {
		t.Fatalf("apply Pod failed: %v", err)
	}
	// Wait for logs and ensure they contain some non-empty hostname
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			desc, _ := exec.Command("kubectl", "describe", "pod", "runproc-host-mode").CombinedOutput()
			logs, _ := kubectlLogs("runproc-host-mode")
			t.Fatalf("timeout waiting for host-mode pod; describe:\n%s\nlogs:\n%s", string(desc), logs)
		default:
		}
		logs, _ := kubectlLogs("runproc-host-mode")
		if strings.TrimSpace(logs) != "" {
			t.Logf("host-mode pod logs (hostname): %s", strings.TrimSpace(logs))
			// Delete the pod gracefully and ensure it is removed
			if err := kubectlDeleteGraceful("runproc-host-mode", 45*time.Second); err != nil {
				t.Fatalf("delete Pod failed: %v", err)
			}
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// Ensures we can run on the host without pulling any OCI image by using a preloaded pause image
func TestKind_E2E_NoPull_HostMode(t *testing.T) {
	if os.Getenv("RUNPROC_KIND_E2E") != "1" {
		t.Skip("set RUNPROC_KIND_E2E=1 to run this test")
	}
	if runtime.GOOS != "linux" {
		t.Skip("linux only")
	}
	// Ensure the Kind cluster and runproc runtime are set up (idempotent)
	setupKindAndRuntime(t)

	// Use the preloaded pause image and Never pull policy; rely on host-mode to provide /bin/sh
	podYAML := `{
			"apiVersion": "v1",
			"kind": "Pod",
			"metadata": { "name": "runproc-no-pull" },
			"spec": {
				"runtimeClassName": "runproc",
				"restartPolicy": "Never",
				"containers": [
					{
						"name": "nopull",
						"image": "registry.k8s.io/pause:3.9",
						"imagePullPolicy": "Never",
						"env": [ { "name": "RUNPROC_HOST", "value": "1" } ],
						"command": [ "/bin/sh", "-c", "echo hello-from-kind-host-no-pull" ]
					}
				]
			}
		}`
	if err := kubectlApply(strings.NewReader(podYAML)); err != nil {
		t.Fatalf("apply Pod failed: %v", err)
	}

	// Wait until logs contain our expected line; don't rely on phase
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			desc, _ := exec.Command("kubectl", "describe", "pod", "runproc-no-pull").CombinedOutput()
			logs, _ := kubectlLogs("runproc-no-pull")
			t.Fatalf("timeout waiting for no-pull host-mode pod; describe:\n%s\nlogs:\n%s", string(desc), logs)
		default:
		}
		logs, _ := kubectlLogs("runproc-no-pull")
		if strings.Contains(logs, "hello-from-kind-host-no-pull") {
			t.Logf("no-pull host-mode pod logs:\n%s", logs)
			// Delete the pod gracefully and ensure it is removed
			if err := kubectlDeleteGraceful("runproc-no-pull", 45*time.Second); err != nil {
				t.Fatalf("delete Pod failed: %v", err)
			}
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func downloadKindOrSkip(t *testing.T) string {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("auto-download of kind only implemented for linux/amd64; install kind manually")
	}
	url := "https://kind.sigs.k8s.io/dl/v0.22.0/kind-linux-amd64"
	tmp := filepath.Join(t.TempDir(), "kind")
	resp, err := http.Get(url)
	if err != nil {
		t.Skipf("failed to download kind: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Skipf("failed to download kind: status %s", resp.Status)
	}
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		t.Skipf("open tmp file: %v", err)
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		t.Skipf("write tmp file: %v", err)
	}
	f.Close()
	return tmp
}

// setupKindAndRuntime ensures a Kind cluster named "runproc-kind-e2e" exists,
// installs the locally built runproc binary into the node, configures containerd
// to use it as a runtime handler "runproc", restarts the node to pick up changes,
// waits for readiness, and creates the RuntimeClass. It's safe to call repeatedly.
func setupKindAndRuntime(t *testing.T) {
	t.Helper()

	// Build runproc
	root := projectRoot(t)
	build := exec.Command("make", "build")
	build.Dir = root
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("make build failed: %v", err)
	}
	bin := filepath.Join(root, "runproc")

	// Ensure kind binary
	kindPath, err := exec.LookPath("kind")
	if err != nil {
		t.Log("kind not found, attempting to download a local copy ...")
		kindPath = downloadKindOrSkip(t)
	}

	cluster := "runproc-kind-e2e"

	// Create the cluster if not exists
	if out, err := exec.Command(kindPath, "get", "clusters").CombinedOutput(); err != nil || !strings.Contains(string(out), cluster) {
		cmd := exec.Command(kindPath, "create", "cluster", "--name", cluster, "--wait", "320s")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("kind create cluster failed: %v", err)
		}
	}

	// Get a node name
	out, err := exec.Command(kindPath, "get", "nodes", "--name", cluster).CombinedOutput()
	if err != nil {
		t.Fatalf("kind get nodes failed: %v, out=%s", err, string(out))
	}
	nodes := strings.Fields(string(out))
	if len(nodes) == 0 {
		t.Fatalf("no nodes returned by kind")
	}
	node := nodes[0]

	// Detect container engine for exec/cp
	engine := "docker"
	if _, err := exec.LookPath("docker"); err != nil {
		if _, err := exec.LookPath("podman"); err == nil {
			engine = "podman"
		} else {
			t.Skip("neither docker nor podman is available to interact with kind nodes")
		}
	}

	// Copy runproc into node
	if out, err := exec.Command(engine, "cp", bin, node+":/usr/local/bin/runproc").CombinedOutput(); err != nil {
		t.Fatalf("%s cp failed: %v, out=%s", engine, err, string(out))
	}
	if out, err := exec.Command(engine, "exec", node, "chmod", "+x", "/usr/local/bin/runproc").CombinedOutput(); err != nil {
		t.Fatalf("chmod runproc failed: %v, out=%s", err, string(out))
	}

	// Configure containerd runtime handler and restart node
	containerdCfg := `
[plugins."io.containerd.grpc.v1.cri".containerd.runtimes.runproc]
  runtime_type = "io.containerd.runc.v2"
  [plugins."io.containerd.grpc.v1.cri".containerd.runtimes.runproc.options]
	BinaryName = "/usr/local/bin/runproc"
`
	injectCfgCmd := fmt.Sprintf("bash -lc 'grep -q runtimes.runproc /etc/containerd/config.toml || cat >>/etc/containerd/config.toml <<EOF\n%s\nEOF'", containerdCfg)
	if out, err := exec.Command(engine, "exec", node, "bash", "-lc", injectCfgCmd).CombinedOutput(); err != nil {
		t.Fatalf("inject containerd config failed: %v, out=%s", err, string(out))
	}
	if out, err := exec.Command(engine, "restart", node).CombinedOutput(); err != nil {
		t.Fatalf("failed to restart kind node %s with %s: %v, out=%s", node, engine, err, string(out))
	}

	// Wait for the node to be Ready
	readyCtx, cancelReady := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancelReady()
	for {
		select {
		case <-readyCtx.Done():
			t.Fatalf("timeout waiting for node %s to be Ready after restart", node)
		default:
		}
		phase, err := exec.Command("kubectl", "get", "nodes", "-o", "jsonpath={.items[0].status.conditions[?(@.type==\"Ready\")].status}").CombinedOutput()
		if err == nil && strings.Contains(string(phase), "True") {
			break
		}
		time.Sleep(2 * time.Second)
	}

	// Create or ensure RuntimeClass exists
	rcYAML := `apiVersion: node.k8s.io/v1
kind: RuntimeClass
metadata:
  name: runproc
handler: runproc
`
	_ = kubectlApply(strings.NewReader(rcYAML)) // idempotent; ignore error if already exists
}

func kubectlApply(r io.Reader) error {
	cmd := exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = r
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("kubectl apply failed: %v, out=%s", err, string(out))
	}
	return nil
}

func kubectlJsonPath(name, path string) (string, error) {
	out, err := exec.Command("kubectl", "get", "pod", name, "-o", "jsonpath="+path).CombinedOutput()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func kubectlLogs(name string) (string, error) {
	out, err := exec.Command("kubectl", "logs", name).CombinedOutput()
	if err != nil {
		return string(out), err
	}
	return string(out), nil
}

// kubectlDeleteGraceful attempts a graceful delete and waits for the pod to disappear
// within gracefulWait. If the pod still exists after the timeout, it returns an error
// without forcing deletion.
func kubectlDeleteGraceful(name string, gracefulWait time.Duration) error {
	// Attempt graceful delete
	out, err := exec.Command("kubectl", "delete", "pod", name, "--ignore-not-found=true").CombinedOutput()
	if err != nil {
		return fmt.Errorf("kubectl delete (graceful) failed: %v, out=%s", err, string(out))
	}
	if err := waitPodDeleted(name, gracefulWait); err != nil {
		return err
	}
	return nil
}

func waitPodDeleted(name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := exec.Command("kubectl", "get", "pod", name).CombinedOutput(); err != nil {
			// kubectl returns non-zero on not found
			return nil
		}
		time.Sleep(1 * time.Second)
	}
	return fmt.Errorf("pod %s still exists after %s", name, timeout)
}
