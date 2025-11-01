package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func usage() {
	fmt.Fprintf(os.Stderr, "runproc - a minimal OCI runtime (MVP)\n")
	fmt.Fprintf(os.Stderr, "Usage:\n")
	fmt.Fprintf(os.Stderr, "  runproc create [--pid-file <path>] <id> <bundle>\n")
	fmt.Fprintf(os.Stderr, "  runproc start <id>\n")
	fmt.Fprintf(os.Stderr, "  runproc state <id>\n")
	fmt.Fprintf(os.Stderr, "  runproc kill <id> <signal>\n")
	fmt.Fprintf(os.Stderr, "  runproc delete <id>\n")
	fmt.Fprintf(os.Stderr, "  runproc run <id> <bundle>\n")
}

func run() int {
	if len(os.Args) < 2 {
		usage()
		return 1
	}
	// Preprocess entire argv (excluding program name) to parse global flags like --root/--log
	preOut, overrides := preprocessRuncCompat("", os.Args[1:])
	if len(preOut) == 0 {
		// No command found; log and exit
		writeOCIErrorLog(overrides.logPath, "no command specified")
		usage()
		return 1
	}
	cmd := preOut[0]
	args := preOut[1:]

	// Special internal command used by create to spawn the init process
	if cmd == "init" {
		if len(args) != 2 {
			fmt.Fprintln(os.Stderr, "init requires <stateDir> <id>")
			return 1
		}
		if err := cmdInit(args[0], args[1]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return 0
	}

	// Determine state dir (RUNPROC_STATE_DIR or default), allow override via global --root
	stateDir := os.Getenv("RUNPROC_STATE_DIR")
	if stateDir == "" {
		stateDir = "/run/runproc"
	}
	if overrides.root != "" {
		stateDir = overrides.root
		os.Setenv("RUNPROC_STATE_DIR", overrides.root)
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "failed to ensure state dir: %v\n", err)
		return 1
	}
	sd, _ := filepath.Abs(stateDir)

	// Preprocess args to be runc-compatible: accept and ignore common flags
	updatedArgs, _ := preprocessRuncCompat(cmd, args)
	if overrides.root != "" {
		os.Setenv("RUNPROC_STATE_DIR", overrides.root)
	}

	switch cmd {
	case "create":
		fs := flag.NewFlagSet("create", flag.ContinueOnError)
		pidFile := fs.String("pid-file", "", "path to write init pid")
		bundleFlag := fs.String("bundle", "", "path to the OCI bundle")
		fs.StringVar(bundleFlag, "b", "", "path to the OCI bundle (shorthand)")
		_ = fs.Parse(updatedArgs)
		rem := fs.Args()
		var id, bundle string
		if *bundleFlag != "" && len(rem) == 1 {
			id = rem[0]
			bundle = *bundleFlag
		} else if len(rem) == 2 {
			id, bundle = rem[0], rem[1]
		} else {
			usage()
			return 1
		}
		if err := cmdCreate(sd, id, bundle, *pidFile); err != nil {
			writeOCIErrorLog(overrides.logPath, err.Error())
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	case "start":
		if len(updatedArgs) != 1 {
			usage()
			return 1
		}
		id := updatedArgs[0]
		if err := cmdStart(sd, id); err != nil {
			writeOCIErrorLog(overrides.logPath, err.Error())
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	case "state":
		if len(updatedArgs) != 1 {
			usage()
			return 1
		}
		id := updatedArgs[0]
		if err := cmdState(sd, id, os.Stdout); err != nil {
			writeOCIErrorLog(overrides.logPath, err.Error())
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	case "kill":
		// accept and drop --all, and support signal-first forms
		// expected inputs we support:
		//   kill <id>
		//   kill <id> <signal|number>
		//   kill <signal|number> <id>
		// and we ignore -a/--all
		args2 := make([]string, 0, len(updatedArgs))
		for _, a := range updatedArgs {
			if a == "--all" || a == "-a" {
				continue
			}
			args2 = append(args2, a)
		}
		if len(args2) == 0 || len(args2) > 2 {
			usage()
			return 1
		}
		id := ""
		sig := ""
		if len(args2) == 1 {
			id = args2[0]
		} else {
			a, b := args2[0], args2[1]
			// if a looks like a signal (starts with '-' digits or 'SIG') then it's the signal
			if strings.HasPrefix(a, "SIG") || strings.HasPrefix(a, "-") && isAllDigits(strings.TrimPrefix(a, "-")) || isAllDigits(a) {
				sig = strings.TrimPrefix(a, "-")
				id = b
			} else {
				id = a
				sig = strings.TrimPrefix(b, "-")
			}
		}
		if err := cmdKill(sd, id, sig); err != nil {
			writeOCIErrorLog(overrides.logPath, err.Error())
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	case "delete":
		// accept and drop --force
		cleaned := make([]string, 0, len(updatedArgs))
		for i := 0; i < len(updatedArgs); i++ {
			if updatedArgs[i] == "--force" || updatedArgs[i] == "-f" {
				continue
			}
			cleaned = append(cleaned, updatedArgs[i])
		}
		if len(cleaned) != 1 {
			usage()
			return 1
		}
		id := cleaned[0]
		if err := cmdDelete(sd, id); err != nil {
			writeOCIErrorLog(overrides.logPath, err.Error())
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	case "run":
		fs := flag.NewFlagSet("run", flag.ContinueOnError)
		pidFile := fs.String("pid-file", "", "path to write init pid")
		bundleFlag := fs.String("bundle", "", "path to the OCI bundle")
		fs.StringVar(bundleFlag, "b", "", "path to the OCI bundle (shorthand)")
		_ = fs.Parse(updatedArgs)
		rem := fs.Args()
		var id, bundle string
		if *bundleFlag != "" && len(rem) == 1 {
			id = rem[0]
			bundle = *bundleFlag
		} else if len(rem) == 2 {
			id, bundle = rem[0], rem[1]
		} else {
			usage()
			return 1
		}
		if err := cmdCreate(sd, id, bundle, *pidFile); err != nil {
			writeOCIErrorLog(overrides.logPath, err.Error())
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if err := cmdStart(sd, id); err != nil {
			writeOCIErrorLog(overrides.logPath, err.Error())
			fmt.Fprintln(os.Stderr, err)
			_ = cmdDelete(sd, id)
			return 1
		}
		if _, err := waitProcess(sd, id); err != nil {
			writeOCIErrorLog(overrides.logPath, err.Error())
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	default:
		writeOCIErrorLog(overrides.logPath, fmt.Sprintf("unknown command: %s", cmd))
		usage()
		return 1
	}
	return 0
}

type compatOverrides struct {
	root      string
	logPath   string
	logFormat string
}

// preprocessRuncCompat strips/normalizes common runc flags containerd passes.
// Returns updated args and overrides for state dir if --root is provided.
func preprocessRuncCompat(cmd string, args []string) ([]string, compatOverrides) {
	ov := compatOverrides{}
	out := make([]string, 0, len(args))
	skipNext := false
	for i := 0; i < len(args); i++ {
		if skipNext {
			skipNext = false
			continue
		}
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			out = append(out, a)
			continue
		}
		// Preserve numeric signals like "-9" so subcommands (kill) can parse them
		if len(a) > 1 && isAllDigits(strings.TrimPrefix(a, "-")) {
			out = append(out, a)
			continue
		}
		// Handle --flag=value forms
		name := a
		value := ""
		if idx := strings.Index(a, "="); idx >= 0 {
			name = a[:idx]
			value = a[idx+1:]
		}
		switch name {
		case "--bundle", "-b":
			if value == "" {
				if i+1 < len(args) {
					value = args[i+1]
					skipNext = true
				}
			}
			out = append(out, "--bundle", value)
		case "--pid-file":
			if value == "" {
				if i+1 < len(args) {
					value = args[i+1]
					skipNext = true
				}
			}
			out = append(out, "--pid-file", value)
		case "--root":
			if value == "" {
				if i+1 < len(args) {
					value = args[i+1]
					skipNext = true
				}
			}
			ov.root = value
		case "--log":
			if value == "" {
				if i+1 < len(args) {
					value = args[i+1]
					skipNext = true
				}
			}
			ov.logPath = value
			// do not forward to parse
		case "--log-format":
			if value == "" {
				if i+1 < len(args) {
					value = args[i+1]
					skipNext = true
				}
			}
			ov.logFormat = value
			// ignore
		case "--systemd-cgroup", "--no-pivot", "--detach", "--console-socket", "--no-new-keyring", "--rootless", "--no-subreaper":
			// Swallow optional value if provided separately
			if value == "" && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				skipNext = true
			}
			// ignore
		default:
			// Unknown flag: if it has an inline value we drop it, if the next arg is not a flag,
			// assume it is the value and skip it. This makes the parser tolerant.
			if value == "" && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				skipNext = true
			}
		}
	}
	return out, ov
}

// isAllDigits reports whether s consists of only ASCII digits 0-9 and non-empty.
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// writeOCIErrorLog writes a minimal OCI runtime error log in JSON format if a log path was provided.
func writeOCIErrorLog(path, msg string) {
	if path == "" {
		return
	}
	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	// Minimal JSON. containerd-shim expects JSON but does not strictly validate schema.
	// We include msg and timestamp.
	now := time.Now().Format(time.RFC3339Nano)
	content := fmt.Sprintf("{\"level\":\"error\",\"msg\":%q,\"time\":%q}\n", msg, now)
	// Best-effort write
	_ = os.WriteFile(path, []byte(content), 0o644)
}
