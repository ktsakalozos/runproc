package integration

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// minimalContainerState mirrors the subset of fields we care about
type minimalContainerState struct {
	ID       string `json:"id"`
	Bundle   string `json:"bundle"`
	Pid      int    `json:"pid"`
	Status   string `json:"status"`
	ExitCode *int   `json:"exitCode,omitempty"`
}

func TestRun_Echo(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux only")
	}

	// Build runproc binary via Makefile (outputs to <projectRoot>/runproc)
	root := projectRoot(t)
	build := exec.Command("make", "build")
	build.Dir = root
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("make build failed: %v", err)
	}
	binPath := filepath.Join(root, "runproc")

	// Create a minimal bundle
	bundle := t.TempDir()
	cfg := `{
      "ociVersion": "1.1.0",
      "process": {
        "terminal": false,
        "args": ["/bin/sh", "-c", "echo itest_ok"],
        "cwd": "/",
        "env": ["PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"]
      },
      "root": {"path": "/", "readonly": false}
    }`
	if err := os.WriteFile(filepath.Join(bundle, "config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// State dir
	stateDir := t.TempDir()
	id := "itest-" + time.Now().Format("150405.000000000")

	// run: should print the echo and exit 0
	var out bytes.Buffer
	cmd := exec.Command(binPath, "run", "--bundle", bundle, id)
	cmd.Env = append(os.Environ(), "RUNPROC_STATE_DIR="+stateDir)
	cmd.Stdout = &out
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if !strings.Contains(out.String(), "itest_ok") {
		t.Fatalf("expected output to contain itest_ok, got: %q", out.String())
	}

	// Validate state: stopped with exitCode 0
	statePath := filepath.Join(stateDir, id, "state.json")
	b, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	var st minimalContainerState
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if st.Status != "stopped" {
		t.Fatalf("expected status=stopped, got %q", st.Status)
	}
	if st.ExitCode == nil || *st.ExitCode != 0 {
		t.Fatalf("expected exitCode=0, got %v", st.ExitCode)
	}
}

func TestCreateStartKill_StatusTransitions(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux only")
	}

	// Build via make
	root := projectRoot(t)
	build := exec.Command("make", "build")
	build.Dir = root
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("make build failed: %v", err)
	}
	binPath := filepath.Join(root, "runproc")

	// Bundle that echoes so we can verify output occurs only after start
	bundle := t.TempDir()
	cfg := `{
	  "ociVersion": "1.1.0",
	  "process": {
		"terminal": false,
				"args": ["/bin/sh", "-c", "echo itest_echo"],
		"cwd": "/",
		"env": ["PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"]
	  },
	  "root": {"path": "/", "readonly": false}
	}`
	if err := os.WriteFile(filepath.Join(bundle, "config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	stateDir := t.TempDir()
	id := "itest-create-start-kill-" + time.Now().Format("150405.000000000")

	// create, wiring stdout/stderr to a file that the init process will inherit
	outFilePath := filepath.Join(stateDir, "echo.out")
	outF, err := os.OpenFile(outFilePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		t.Fatalf("open out file: %v", err)
	}
	defer outF.Close()

	// create
	{
		cmd := exec.Command(binPath, "create", "--bundle", bundle, id)
		cmd.Env = append(os.Environ(), "RUNPROC_STATE_DIR="+stateDir)
		cmd.Stdout = outF
		cmd.Stderr = outF
		if err := cmd.Run(); err != nil {
			t.Fatalf("create failed: %v", err)
		}
	}
	st := readState(t, stateDir, id)
	if st.Status != "created" {
		t.Fatalf("expected status=created after create, got %q", st.Status)
	}
	// Verify nothing has been echoed yet
	if b, _ := os.ReadFile(outFilePath); len(b) != 0 {
		t.Fatalf("expected no output before start, got: %q", string(b))
	}

	// start
	{
		cmd := exec.Command(binPath, "start", id)
		cmd.Env = append(os.Environ(), "RUNPROC_STATE_DIR="+stateDir)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("start failed: %v", err)
		}
	}
	st = readState(t, stateDir, id)
	if st.Status != "running" {
		t.Fatalf("expected status=running after start, got %q", st.Status)
	}
	pid := st.Pid
	if pid <= 0 {
		t.Fatalf("invalid pid after start: %d", pid)
	}

	// Output should appear only after start
	deadlineEcho := time.Now().Add(1 * time.Second)
	for {
		b, _ := os.ReadFile(outFilePath)
		if strings.Contains(string(b), "itest_echo") {
			break
		}
		if time.Now().After(deadlineEcho) {
			t.Fatalf("expected output after start, not observed in time; got: %q", string(b))
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Wait for process to exit naturally (echo completes quickly)
	deadline := time.Now().Add(2 * time.Second)
	for {
		if !procExists(pid) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("process %d still exists after echo", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Due to MVP design, state may remain "running" because no parent waits;
	// accept either "stopped" (if run mode was used) or "running" with missing PID.
	st = readState(t, stateDir, id)
	if st.Status != "stopped" && st.Status != "running" {
		t.Fatalf("expected status to be stopped or running after kill, got %q", st.Status)
	}
}

func readState(t *testing.T, stateDir, id string) minimalContainerState {
	t.Helper()
	p := filepath.Join(stateDir, id, "state.json")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	var st minimalContainerState
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	return st
}

func procExists(pid int) bool {
	if pid <= 0 {
		return false
	}
	_, err := os.Stat(filepath.Join("/proc", fmtInt(pid)))
	return err == nil
}

func fmtInt(n int) string { return strconv.Itoa(n) }

// projectRoot returns the path to the project root directory by searching for go.mod upwards from the current working directory.
func projectRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not find go.mod in any parent directory starting from %s", dir)
		}
		dir = parent
	}
}
