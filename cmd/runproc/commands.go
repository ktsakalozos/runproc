package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ktsakalozos/runproc/internal/oci"
	"github.com/ktsakalozos/runproc/internal/state"
)

// cmdCreate reads the bundle's config.json, stores state, and forks an init process
// that will exec the process specified in the spec when 'start' is called.
func cmdCreate(stateDir, id, bundle, pidFile string) error {
	if state.Exists(stateDir, id) {
		return fmt.Errorf("container %s already exists", id)
	}
	spec, err := oci.LoadSpec(bundle)
	if err != nil {
		return err
	}
	// Create a pipe: parent blocks until child is ready
	pr, pw, err := os.Pipe()
	if err != nil {
		return err
	}
	defer pr.Close()

	// Start a child process that will block until it receives a start signal via state.
	self, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(self, "init", stateDir, id)
	cmd.Env = os.Environ()
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	// Pass pipe fd to child via ExtraFiles; child will get it as fd 3
	// Child will read from fd 3
	cmd.ExtraFiles = []*os.File{pr}
	// Working directory is bundle per OCI
	cmd.Dir = bundle

	if err := cmd.Start(); err != nil {
		pw.Close()
		return fmt.Errorf("start init: %w", err)
	}
	// Parent no longer needs its copy of read end
	pr.Close()

	st := &state.ContainerState{ID: id, Bundle: bundle, Pid: cmd.Process.Pid}
	if err := state.Create(stateDir, st); err != nil {
		// try to kill child if state write fails
		_ = cmd.Process.Kill()
		_ = cmd.Process.Release()
		return err
	}
	if pidFile != "" {
		if err := os.WriteFile(pidFile, []byte(strconv.Itoa(cmd.Process.Pid)), 0o644); err != nil {
			return fmt.Errorf("write pid-file: %w", err)
		}
	}
	// Send the process spec over the pipe to the child
	enc := json.NewEncoder(pw)
	if err := enc.Encode(spec.Process); err != nil {
		return fmt.Errorf("encode process to child: %w", err)
	}
	pw.Close()
	return nil
}

func cmdStart(stateDir, id string) error {
	st, err := state.Load(stateDir, id)
	if err != nil {
		return err
	}
	if st.Status == state.Running {
		return nil
	}
	// Signal the child to start by touching a start file
	startPath := filepath.Join(stateDir, id, "start")
	if err := os.WriteFile(startPath, []byte("start"), 0o600); err != nil {
		return err
	}
	now := time.Now()
	st.Status = state.Running
	st.StartedAt = &now
	return state.Save(stateDir, st)
}

func cmdState(stateDir, id string, w io.Writer) error {
	st, err := state.Load(stateDir, id)
	if err != nil {
		return err
	}
	// Self-heal: if recorded running but process is gone, mark as stopped
	if st.Status == state.Running && !pidAlive(st.Pid) {
		now := time.Now()
		st.Status = state.Stopped
		st.ExitedAt = &now
		_ = state.Save(stateDir, st)
	}
	// runc-compatible-ish minimal JSON state
	out := map[string]any{
		"id":     st.ID,
		"pid":    st.Pid,
		"status": st.Status,
		"bundle": st.Bundle,
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func cmdKill(stateDir, id, signal string) error {
	st, err := state.Load(stateDir, id)
	if err != nil {
		if os.IsNotExist(err) {
			// Already deleted; consider kill a no-op
			return nil
		}
		return err
	}
	if st.Pid <= 0 {
		return errors.New("no pid")
	}
	sig := syscall.SIGTERM
	if signal != "" {
		// Accept names like SIGTERM or numbers
		if strings.HasPrefix(signal, "SIG") {
			m := map[string]syscall.Signal{
				"SIGTERM": syscall.SIGTERM,
				"SIGKILL": syscall.SIGKILL,
				"SIGINT":  syscall.SIGINT,
				"SIGHUP":  syscall.SIGHUP,
			}
			if s, ok := m[signal]; ok {
				sig = s
			}
		} else if n, err := strconv.Atoi(signal); err == nil {
			sig = syscall.Signal(n)
		}
	}
	if err := syscall.Kill(st.Pid, sig); err != nil {
		return err
	}
	return nil
}

func cmdDelete(stateDir, id string) error {
	st, err := state.Load(stateDir, id)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if st.Status == state.Running {
		// If process is no longer alive, flip to stopped; otherwise try a best-effort kill
		alive := pidAlive(st.Pid)
		if !alive {
			now := time.Now()
			st.Status = state.Stopped
			st.ExitedAt = &now
			_ = state.Save(stateDir, st)
		} else {
			// Best-effort SIGKILL then wait briefly for exit
			_ = syscall.Kill(st.Pid, syscall.SIGKILL)
			for i := 0; i < 20; i++ { // wait up to ~2s
				if !pidAlive(st.Pid) {
					now := time.Now()
					st.Status = state.Stopped
					st.ExitedAt = &now
					_ = state.Save(stateDir, st)
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
		}
	}
	// Best-effort delete; ignore if already gone
	if err := state.Delete(stateDir, id); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return nil
}

// pidAlive returns whether a PID currently exists. EPERM means alive; ESRCH means not alive.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true
	}
	if err == syscall.EPERM {
		return true
	}
	return false
}

// cmdInit runs in the child process created during 'create'.
// It reads specs.Process from fd 3, then waits for the 'start' file before execing the program.
func cmdInit(stateDir, id string) error {
	// fd 3 is the pipe from parent where specs.Process is sent
	pipe := os.NewFile(uintptr(3), "parent-pipe")
	defer pipe.Close()
	var p oci.Process
	if err := json.NewDecoder(pipe).Decode(&p); err != nil {
		return fmt.Errorf("init decode process: %w", err)
	}

	// Wait for start signal: file existence
	startPath := filepath.Join(stateDir, id, "start")
	for {
		if _, err := os.Stat(startPath); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Load spec and bundle to determine rootfs for a minimal chroot
	st, err := state.Load(stateDir, id)
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	spec, err := oci.LoadSpec(st.Bundle)
	if err != nil {
		return fmt.Errorf("load spec: %w", err)
	}

	// Determine if host mode is requested via annotation or env var
	hostMode := false
	// Allow toggling via the runtime process env (for direct runs)
	if v := os.Getenv("RUNPROC_HOST"); v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "yes") {
		hostMode = true
	}
	// Allow toggling via the container process env in the OCI spec
	if !hostMode {
		for _, e := range p.Env {
			if strings.HasPrefix(e, "RUNPROC_HOST=") {
				val := strings.TrimPrefix(e, "RUNPROC_HOST=")
				if val == "1" || strings.EqualFold(val, "true") || strings.EqualFold(val, "yes") {
					hostMode = true
					break
				}
			}
		}
	}
	if spec.Annotations != nil {
		if v, ok := spec.Annotations["runproc.host"]; ok {
			if v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "yes") {
				hostMode = true
			}
		}
	}

	// Perform a minimal chroot into the rootfs if specified, unless host mode is requested
	if !hostMode && spec.Root != nil && spec.Root.Path != "" && os.Geteuid() == 0 {
		rootfs := spec.Root.Path
		if !filepath.IsAbs(rootfs) {
			rootfs = filepath.Join(st.Bundle, rootfs)
		}
		if err := syscall.Chroot(rootfs); err != nil {
			return fmt.Errorf("chroot: %w", err)
		}
		if err := os.Chdir("/"); err != nil {
			return fmt.Errorf("chdir after chroot: %w", err)
		}
	}

	// Setup stdio: use current stdio; containerd will have set FIFOs already
	argv := []string{p.Args[0]}
	if len(p.Args) > 1 {
		argv = p.Args
	}
	// If Cwd is set, chdir
	if p.Cwd != "" {
		if err := os.Chdir(p.Cwd); err != nil {
			return fmt.Errorf("chdir: %w", err)
		}
	}
	// Setup env
	if len(p.Env) > 0 {
		os.Clearenv()
		for _, e := range p.Env {
			kv := strings.SplitN(e, "=", 2)
			if len(kv) == 2 {
				os.Setenv(kv[0], kv[1])
			}
		}
	}

	return syscall.Exec(argv[0], argv, os.Environ())
}

// waitProcess polls the pid and records exit code into state once exited.
func waitProcess(stateDir, id string) (int, error) {
	st, err := state.Load(stateDir, id)
	if err != nil {
		return 1, err
	}
	if st.Pid <= 0 {
		return 1, errors.New("no pid")
	}
	var ws syscall.WaitStatus
	for {
		var rusage syscall.Rusage
		wpid, err := syscall.Wait4(st.Pid, &ws, 0, &rusage)
		if err != nil {
			if err == syscall.EINTR {
				continue
			}
			return 1, err
		}
		if wpid == st.Pid {
			break
		}
	}
	code := ws.ExitStatus()
	now := time.Now()
	st.Status = state.Stopped
	st.ExitedAt = &now
	st.ExitCode = &code
	_ = state.Save(stateDir, st)
	return code, nil
}
