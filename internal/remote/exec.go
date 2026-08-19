package remote

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// Exec becomes the provider, on the machine that runs it.
//
// It is the last link in a chain that exists to solve one problem: a
// provider needs the environment of the shell its user actually works
// in, and that shell's syntax is not something the dashboard's machine
// may assume. The daemon spawns /bin/sh with ExecPrelude; that script
// execs the host's login shell; that shell execs Stormlight here; this
// becomes the provider. Every step is an exec, so the process the daemon
// supervises is the provider itself — its signals, its exit status, its
// lifetime, undisturbed by the route taken to start it.
//
// The argv arrives in ExecEnv, JSON-encoded, and that is the point of
// the whole arrangement: no shell ever parses the task text or a custom
// provider's arguments, so no shell's quoting rules can change them.
// What was dispatched is what runs.
func Exec() error {
	argv, err := ExecArgv(os.Getenv(ExecEnv))
	if err != nil {
		return err
	}
	// Resolved here rather than carried here, because here is where the
	// login shell's PATH is finally in effect. The dashboard's own
	// lookup happens before dispatch, so that a provider the host does
	// not have is reported rather than dispatched; this one is what
	// actually decides.
	path, err := exec.LookPath(argv[0])
	if err != nil {
		host, _ := os.Hostname()
		return fmt.Errorf("%s on %s: %w", argv[0], host, err)
	}
	return syscall.Exec(path, argv, os.Environ())
}

// ExecArgv reads back what ExecEnv carried.
func ExecArgv(encoded string) ([]string, error) {
	if encoded == "" {
		return nil, fmt.Errorf("%s is empty: nothing to run", ExecEnv)
	}
	var argv []string
	if err := json.Unmarshal([]byte(encoded), &argv); err != nil {
		return nil, fmt.Errorf("decode %s: %w", ExecEnv, err)
	}
	if len(argv) == 0 {
		return nil, fmt.Errorf("%s names no program", ExecEnv)
	}
	return argv, nil
}

// ExecEnvFor is the environment entry that carries one argv across.
func ExecEnvFor(argv []string) (string, error) {
	encoded, err := json.Marshal(argv)
	if err != nil {
		return "", fmt.Errorf("encode provider argv: %w", err)
	}
	return ExecEnv + "=" + string(encoded), nil
}
